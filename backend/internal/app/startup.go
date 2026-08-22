package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	accountapp "m365-copilot2xapi/backend/internal/application/account"
	auditapp "m365-copilot2xapi/backend/internal/application/audit"
	accountdomain "m365-copilot2xapi/backend/internal/domain/account"
	"m365-copilot2xapi/backend/internal/infra/provider"
	"m365-copilot2xapi/backend/internal/repository"
	httpserver "m365-copilot2xapi/backend/internal/transport/http"
)

const (
	startupRecoveryBudget = 20 * time.Second
	startupCriticalWindow = 2 * time.Minute
	startupCriticalLimit  = 100
)

type startupReport struct {
	StartedAt           time.Time
	CompletedAt         *time.Time
	Credentials         accountapp.CredentialStartupReport
	CooldownsRestored   int
	ErrorCount          int
}

type startupState struct {
	mu        sync.RWMutex
	phase     string
	updatedAt time.Time
	report    startupReport
}

func newStartupState(restoredQuotaRecoveries int) *startupState {
	now := time.Now().UTC()
	return &startupState{
		phase:     "booting",
		updatedAt: now,
		report: startupReport{
			StartedAt: now,
		},
	}
}

func (s *startupState) setPhase(phase string) {
	s.mu.Lock()
	s.phase = phase
	s.updatedAt = time.Now().UTC()
	if phase == "running" {
		completed := s.updatedAt
		s.report.CompletedAt = &completed
	}
	s.mu.Unlock()
}

func (s *startupState) updateReport(update func(*startupReport)) {
	s.mu.Lock()
	update(&s.report)
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()
}

func (s *startupState) recordError(err error) {
	if err == nil {
		return
	}
	s.updateReport(func(report *startupReport) {
		report.ErrorCount++
	})
}

func (s *startupState) snapshot() (string, time.Time, startupReport) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.phase, s.updatedAt, s.report
}

func (s *startupState) acceptsTraffic() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.phase == "running"
}

func readinessSnapshot(
	ctx context.Context,
	state *startupState,
	runtimeHealth func(context.Context) error,
	models repository.ModelRepository,
	accounts repository.AccountRepository,
	providers *provider.Registry,
	ledger *auditapp.Service,
) httpserver.ReadinessSnapshot {
	phase, updatedAt, report := state.snapshot()
	snapshot := httpserver.ReadinessSnapshot{
		Ready: false, State: phase, UpdatedAt: updatedAt, Startup: newReadinessStartupReport(report),
		Components: map[string]httpserver.ReadinessComponent{
			"runtime_store":  {State: "unknown"},
			"billing_ledger": {State: "unknown"},
		},
	}
	if phase != "running" {
		return snapshot
	}
	ledgerDegraded := false
	healthCtx, cancel := context.WithTimeout(ctx, time.Second)
	err := runtimeHealth(healthCtx)
	cancel()
	if err != nil {
		snapshot.State = "not_ready"
		snapshot.Components["runtime_store"] = httpserver.ReadinessComponent{State: "unavailable", Detail: "运行态存储不可用"}
		return snapshot
	}
	snapshot.Components["runtime_store"] = httpserver.ReadinessComponent{State: "ready"}
	if ledger != nil {
		ledgerState := ledger.LedgerSnapshot()
		if ledgerState.Ready {
			snapshot.Components["billing_ledger"] = httpserver.ReadinessComponent{State: "ready"}
		} else {
			detail := fmt.Sprintf("审计账本不可用；连续失败 %d 次，丢失 %d 条，队列 %d/%d", ledgerState.ConsecutiveFailures, ledgerState.Dropped, ledgerState.QueueDepth, ledgerState.QueueCapacity)
			snapshot.Components["billing_ledger"] = httpserver.ReadinessComponent{State: "degraded", Detail: detail}
			if ledgerState.Irrecoverable || ledgerState.Mode == auditapp.LedgerModeEnforce {
				snapshot.State = "not_ready"
				return snapshot
			}
			ledgerDegraded = true
		}
	}

	routes, err := models.ListConfiguredEnabled(ctx)
	if err != nil {
		snapshot.State = "not_ready"
		snapshot.Components["model_routes"] = httpserver.ReadinessComponent{State: "unavailable", Detail: "模型路由读取失败"}
		return snapshot
	}
	if len(routes) == 0 {
		snapshot.State = "not_ready"
		snapshot.Components["model_routes"] = httpserver.ReadinessComponent{State: "unavailable", Detail: "没有启用的模型路由"}
		return snapshot
	}
	snapshot.Components["model_routes"] = httpserver.ReadinessComponent{State: "ready", Detail: fmt.Sprintf("%d 条已启用路由", len(routes))}

	required := make(map[accountdomain.Provider]bool, 1)
	usable := make(map[accountdomain.Provider]bool, 1)
	providerErrors := make(map[accountdomain.Provider]bool, 1)
	now := time.Now().UTC()
	for _, route := range routes {
		required[route.Provider] = true
		if usable[route.Provider] || route.SupportedAccounts == 0 {
			continue
		}
		candidates, listErr := accounts.ListRoutingCandidates(ctx, route.Provider, route.ID, route.UpstreamModel, providers.QuotaMode(route.Provider, route.UpstreamModel))
		if listErr != nil {
			providerErrors[route.Provider] = true
			continue
		}
		for _, candidate := range candidates {
			if !startupCandidateUsable(candidate, now, providers) {
				continue
			}
			material, materialErr := accounts.GetCredentialMaterial(ctx, candidate.Credential.ID, candidate.Credential.Provider)
			if materialErr != nil {
				if !errors.Is(materialErr, repository.ErrNotFound) {
					providerErrors[route.Provider] = true
				}
				continue
			}
			if material.EncryptedAccessToken != "" {
				usable[route.Provider] = true
				break
			}
		}
	}

	readyProviders := 0
	unavailableProviders := 0
	for _, providerValue := range accountdomain.Providers() {
		name := string(providerValue)
		if !required[providerValue] {
			snapshot.Components[name] = httpserver.ReadinessComponent{State: "disabled"}
			continue
		}
		if usable[providerValue] {
			readyProviders++
			snapshot.Components[name] = httpserver.ReadinessComponent{State: "ready"}
			continue
		}
		unavailableProviders++
		detail := "当前没有可用于已启用路由的账号"
		if providerErrors[providerValue] {
			detail = "账号候选状态读取失败"
		}
		snapshot.Components[name] = httpserver.ReadinessComponent{State: "unavailable", Detail: detail}
	}
	if readyProviders == 0 {
		snapshot.State = "not_ready"
		return snapshot
	}
	snapshot.Ready = true
	if unavailableProviders > 0 || ledgerDegraded {
		snapshot.State = "degraded"
	} else {
		snapshot.State = "ready"
	}
	return snapshot
}

// newReadinessStartupReport 只公开稳定统计，不把启动错误原文暴露到无鉴权就绪端点。
func newReadinessStartupReport(report startupReport) *httpserver.ReadinessStartupReport {
	return &httpserver.ReadinessStartupReport{
		StartedAt:         report.StartedAt,
		CompletedAt:       report.CompletedAt,
		Credentials: httpserver.ReadinessCredentialReport{
			SchedulesBackfilled: report.Credentials.SchedulesBackfilled,
			CriticalFound:       report.Credentials.CriticalFound,
			Refreshed:           report.Credentials.Refreshed,
			Failed:              report.Credentials.Failed,
		},
		CooldownsRestored: report.CooldownsRestored,
		ErrorCount:        report.ErrorCount,
	}
}

func startupCandidateUsable(candidate accountdomain.RoutingCandidate, now time.Time, providers *provider.Registry) bool {
	credential := candidate.Credential
	if credential.AuthType == "" || credential.AuthStatus != accountdomain.AuthStatusActive {
		return false
	}
	refreshable := credential.AuthType == accountdomain.AuthTypeOAuth
	if providers != nil {
		refreshable = providers.SupportsCredentialRefresh(credential.Provider)
	}
	if refreshable && !credential.ExpiresAt.IsZero() && !now.Before(credential.ExpiresAt) {
		return false
	}
	if credential.CooldownUntil != nil && now.Before(*credential.CooldownUntil) {
		return false
	}
	if candidate.ModelCapabilityKnown && !candidate.SupportsModel {
		return false
	}
	if candidate.ModelQuotaBlock != nil && now.Before(candidate.ModelQuotaBlock.CooldownUntil) {
		return false
	}
	if candidate.Billing != nil && candidate.Billing.IsExhausted(credential.MinimumRemaining) {
		return false
	}
	return candidate.QuotaWindow == nil || candidate.QuotaWindow.Remaining > 0
}

func (a *Application) reconcileStartup(ctx context.Context) {
	a.startup.setPhase("reconciling")
	recoveryCtx, cancel := context.WithTimeout(ctx, startupRecoveryBudget)
	defer cancel()

	if _, err := a.clientKeys.CleanupExpiredBilling(recoveryCtx, 1000); err != nil {
		a.logger.Warn("billing_reservation_cleanup_failed", "error", err)
		a.startup.recordError(err)
	}
	if _, err := a.accountRepo.PruneExpiredModelQuotaBlocks(recoveryCtx, time.Now().UTC(), 1000); err != nil {
		a.logger.Warn("model_cooldown_cleanup_failed", "error", err)
		a.startup.recordError(err)
	}
	for _, providerValue := range accountdomain.Providers() {
		values, err := a.accountRepo.ListEnabled(recoveryCtx, providerValue)
		if err != nil {
			a.startup.recordError(err)
			continue
		}
		now := time.Now().UTC()
		a.startup.updateReport(func(report *startupReport) {
			for _, value := range values {
				if value.CooldownUntil != nil && now.Before(*value.CooldownUntil) {
					report.CooldownsRestored++
				}
			}
		})
	}
	report, err := a.accounts.RecoverCriticalCredentials(recoveryCtx, startupCriticalWindow, startupCriticalLimit)
	a.startup.updateReport(func(startup *startupReport) { startup.Credentials = report })
	if err != nil && ctx.Err() == nil {
		a.logger.Warn("credential_startup_recovery_incomplete", "error", err, "found", report.CriticalFound, "refreshed", report.Refreshed, "failed", report.Failed)
		a.startup.recordError(err)
	}
	a.startup.setPhase("running")
	a.logger.Info("startup_reconciliation_completed", "credentials_backfilled", report.SchedulesBackfilled, "critical_found", report.CriticalFound, "credentials_refreshed", report.Refreshed, "credentials_failed", report.Failed)
}
