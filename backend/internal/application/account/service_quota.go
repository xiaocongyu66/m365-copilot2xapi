package account

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	accountdomain "m365-copilot2xapi/backend/internal/domain/account"
	"m365-copilot2xapi/backend/internal/infra/provider"
	"m365-copilot2xapi/backend/internal/repository"
)

func (s *Service) RefreshBilling(ctx context.Context, id uint64) (accountdomain.Billing, error) {
	result, err, _ := s.billingSyncs.Do(strconv.FormatUint(id, 10), func() (any, error) {
		return s.refreshBilling(ctx, id)
	})
	if err != nil {
		return accountdomain.Billing{}, err
	}
	billing, ok := result.(accountdomain.Billing)
	if !ok {
		return accountdomain.Billing{}, fmt.Errorf("额度同步返回类型无效")
	}
	return billing, nil
}

func (s *Service) refreshBilling(ctx context.Context, id uint64) (accountdomain.Billing, error) {
	value, billing, err := s.fetchAndSaveBilling(ctx, id)
	if err != nil {
		return accountdomain.Billing{}, err
	}
	if err := s.reconcilePaidQuotaRecovery(ctx, value, billing, false); err != nil {
		return accountdomain.Billing{}, err
	}
	return billing, nil
}

func (s *Service) fetchAndSaveBilling(ctx context.Context, id uint64) (accountdomain.Credential, accountdomain.Billing, error) {
	value, err := s.accounts.Get(ctx, id)
	if err != nil {
		return accountdomain.Credential{}, accountdomain.Billing{}, mapRepositoryError(err)
	}
	value, err = s.EnsureCredential(ctx, value, false)
	if err != nil {
		return accountdomain.Credential{}, accountdomain.Billing{}, err
	}
	adapter, ok := s.providers.Billing(value.Provider)
	if !ok {
		return accountdomain.Credential{}, accountdomain.Billing{}, fmt.Errorf("Provider %s 未注册", value.Provider)
	}
	billing, err := adapter.GetBilling(ctx, value)
	if err != nil {
		return accountdomain.Credential{}, accountdomain.Billing{}, err
	}
	billing.AccountID = id
	if err := s.accounts.SaveBilling(ctx, billing); err != nil {
		return accountdomain.Credential{}, accountdomain.Billing{}, err
	}
	return value, billing, nil
}

// ProbePaidQuota 在真实账期到期后执行一次 Billing 探测，不消耗模型额度。
func (s *Service) ProbePaidQuota(ctx context.Context, value accountdomain.Credential) (bool, error) {
	latest, billing, err := s.fetchAndSaveBilling(ctx, value.ID)
	if err != nil {
		return false, err
	}
	if err := s.reconcilePaidQuotaRecovery(ctx, latest, billing, true); err != nil {
		return false, err
	}
	return !billing.IsExhausted(latest.MinimumRemaining), nil
}

func (s *Service) reconcilePaidQuotaRecovery(ctx context.Context, credential accountdomain.Credential, billing accountdomain.Billing, afterProbe bool) error {
	return nil
}

// HasBillingSnapshot 判断账号是否已经完成过一次额度同步，不触发任何上游请求。
func (s *Service) HasBillingSnapshot(ctx context.Context, id uint64) (bool, error) {
	_, err := s.accounts.GetBilling(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) HasQuotaWindows(ctx context.Context, id uint64) (bool, error) {
	return s.accounts.HasQuotaWindows(ctx, id)
}

func (s *Service) DecrementQuota(ctx context.Context, id uint64, mode string, amount int) (bool, error) {
	if amount <= 0 {
		amount = 1
	}
	if repository, ok := s.accounts.(interface {
		DecrementQuotaWindowBy(context.Context, uint64, string, int, time.Time) (bool, error)
	}); ok {
		return repository.DecrementQuotaWindowBy(ctx, id, mode, amount, s.now())
	}
	updated := false
	for range amount {
		decremented, err := s.accounts.DecrementQuotaWindow(ctx, id, mode, s.now())
		if err != nil {
			return updated, err
		}
		if !decremented {
			break
		}
		updated = true
	}
	return updated, nil
}

func (s *Service) DecrementWebQuota(ctx context.Context, id uint64, mode string, amount int) (bool, error) {
	return s.DecrementQuota(ctx, id, mode, amount)
}

func (s *Service) ExhaustQuota(ctx context.Context, id uint64, mode string, resetAt *time.Time) error {
	if resetAt == nil {
		windows, err := s.accounts.GetQuotaWindows(ctx, []uint64{id})
		if err == nil {
			for _, window := range windows[id] {
				if window.Mode != mode {
					continue
				}
				resetAt = quotaRecoveryDueAt(window, s.now(), true)
				break
			}
		}
	}
	if err := s.accounts.ExhaustQuotaWindow(ctx, id, mode, resetAt, s.now()); err != nil {
		return err
	}
	return nil
}

func (s *Service) ExhaustWebQuota(ctx context.Context, id uint64, mode string, resetAt *time.Time) error {
	return s.ExhaustQuota(ctx, id, mode, resetAt)
}

func (s *Service) RefreshQuota(ctx context.Context, id uint64) ([]accountdomain.QuotaWindow, error) {
	result, err, _ := s.quotaSyncs.Do("all:"+strconv.FormatUint(id, 10), func() (any, error) {
		return s.refreshQuota(ctx, id)
	})
	if err != nil {
		return nil, err
	}
	refreshed, ok := result.(quotaRefreshResult)
	if !ok {
		return nil, fmt.Errorf("Provider 额度同步返回类型无效")
	}
	if err := s.reconcileQuotaRecoveryWindows(ctx, refreshed.Credential.Provider, id, refreshed.Windows); err != nil {
		return refreshed.Windows, err
	}
	return refreshed.Windows, nil
}

func (s *Service) refreshQuota(ctx context.Context, id uint64) (quotaRefreshResult, error) {
	value, err := s.accounts.Get(ctx, id)
	if err != nil {
		return quotaRefreshResult{}, mapRepositoryError(err)
	}
	adapter, ok := s.providers.Quota(value.Provider)
	if !ok {
		return quotaRefreshResult{}, fmt.Errorf("%s Quota Provider 未注册", value.Provider)
	}
	snapshot, err := adapter.SyncQuota(ctx, value)
	if err != nil {
		if errors.Is(err, provider.ErrUnauthorized) {
		}
		return quotaRefreshResult{}, err
	}
	quotaKind, _ := s.providers.QuotaKind(value.Provider)
	if quotaKind == provider.QuotaLocalWindow {
		existing, loadErr := s.accounts.GetQuotaWindows(ctx, []uint64{id})
		if loadErr != nil {
			return quotaRefreshResult{}, loadErr
		}
		snapshot.Windows = preserveActiveQuotaWindows(existing[id], snapshot.Windows, s.now())
	}
	if err := s.accounts.ReplaceQuotaWindowGroup(ctx, id, snapshot.SyncedAt, nil, snapshot.Windows); err != nil {
		return quotaRefreshResult{}, err
	}
	return quotaRefreshResult{Credential: value, Windows: snapshot.Windows}, nil
}

func preserveActiveQuotaWindows(existing, incoming []accountdomain.QuotaWindow, now time.Time) []accountdomain.QuotaWindow {
	byMode := make(map[string]accountdomain.QuotaWindow, len(existing))
	for _, window := range existing {
		byMode[window.Mode] = window
	}
	result := append([]accountdomain.QuotaWindow(nil), incoming...)
	for index, window := range result {
		current, ok := byMode[window.Mode]
		if !ok || current.ResetAt == nil || !current.ResetAt.After(now) {
			continue
		}
		result[index] = current
	}
	return result
}

// ReconcileRateLimit 根据额度模式核实 429；Web 周池继续以上游快照为准。
func (s *Service) ReconcileRateLimit(ctx context.Context, id uint64, mode string, retryAfter time.Duration) (bool, error) {
	if mode == "weekly" {
		window, err := s.RefreshQuotaMode(ctx, id, mode)
		if err != nil {
			return false, err
		}
		return window.Remaining == 0 || window.UsagePercent >= 100, nil
	}
	var resetAt *time.Time
	if retryAfter > 0 {
		value := s.now().Add(retryAfter)
		resetAt = &value
	}
	if err := s.ExhaustQuota(ctx, id, mode, resetAt); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) ReconcileWebRateLimit(ctx context.Context, id uint64, mode string, retryAfter time.Duration) (bool, error) {
	return s.ReconcileRateLimit(ctx, id, mode, retryAfter)
}

func (s *Service) RefreshQuotaMode(ctx context.Context, id uint64, mode string) (accountdomain.QuotaWindow, error) {
	mode = strings.TrimSpace(mode)
	key := quotaSyncKey(id, mode)
	result, err, _ := s.quotaSyncs.Do(key, func() (any, error) {
		if isWebImagineQuotaMode(mode) {
			return s.refreshQuotaGroup(ctx, id, "")
		}
		return s.refreshQuotaMode(ctx, id, mode)
	})
	if err != nil {
		return accountdomain.QuotaWindow{}, err
	}
	refreshed, ok := result.(quotaRefreshResult)
	if !ok {
		return accountdomain.QuotaWindow{}, fmt.Errorf("Provider 模式额度同步返回类型无效")
	}
	if len(refreshed.Modes) > 0 {
		if err := s.reconcileQuotaGroupWindows(ctx, refreshed.Credential.Provider, id, refreshed.Modes, refreshed.Windows); err != nil {
			return accountdomain.QuotaWindow{}, err
		}
	}
	window, ok := quotaWindowByMode(refreshed.Windows, mode)
	if !ok {
		return accountdomain.QuotaWindow{}, fmt.Errorf("Provider usage 响应缺少 %s 额度", mode)
	}
	if len(refreshed.Modes) == 0 && refreshed.Credential.Provider == accountdomain.ProviderM365 {
		// One Console request refreshes all three authoritative windows. Reconcile
		// every matching recovery event so externally consumed media quota cannot
		// remain unscheduled merely because a different kind triggered the refresh.
		if err := s.reconcileQuotaRecoveryWindows(ctx, refreshed.Credential.Provider, id, refreshed.Windows); err != nil {
			return window, err
		}
	} else if len(refreshed.Modes) == 0 {
		if err := s.reconcileQuotaRecoveryWindow(ctx, refreshed.Credential.Provider, id, window); err != nil {
			return window, err
		}
	}
	return window, nil
}

// ProbeQuotaMode refreshes a claimed recovery event without scheduling a
// second event for the same account and mode. The recovery worker owns the
// current claim and is responsible for acknowledging or rescheduling it.
func (s *Service) ProbeQuotaMode(ctx context.Context, id uint64, mode string) (accountdomain.QuotaWindow, error) {
	mode = strings.TrimSpace(mode)
	key := quotaSyncKey(id, mode)
	result, err, _ := s.quotaSyncs.Do(key, func() (any, error) {
		if isWebImagineQuotaMode(mode) {
			return s.refreshQuotaGroup(ctx, id, "")
		}
		return s.refreshQuotaMode(ctx, id, mode)
	})
	if err != nil {
		return accountdomain.QuotaWindow{}, err
	}
	refreshed, ok := result.(quotaRefreshResult)
	if !ok {
		return accountdomain.QuotaWindow{}, fmt.Errorf("Provider 模式额度探测返回类型无效")
	}
	window, ok := quotaWindowByMode(refreshed.Windows, mode)
	if !ok {
		return accountdomain.QuotaWindow{}, fmt.Errorf("Provider usage 响应缺少 %s 额度", mode)
	}
	return window, nil
}

func (s *Service) refreshQuotaGroup(ctx context.Context, id uint64, group string) (quotaRefreshResult, error) {
	value, err := s.accounts.Get(ctx, id)
	if err != nil {
		return quotaRefreshResult{}, mapRepositoryError(err)
	}
	adapter, ok := s.providers.QuotaGroup(value.Provider)
	if !ok {
		return quotaRefreshResult{}, fmt.Errorf("%s quota group Provider 未注册", value.Provider)
	}
	snapshot, err := adapter.SyncQuotaGroup(ctx, value, group)
	if err != nil {
		if errors.Is(err, provider.ErrUnauthorized) {
		}
		return quotaRefreshResult{}, err
	}
	if snapshot.Group != group || len(snapshot.Modes) == 0 {
		return quotaRefreshResult{}, fmt.Errorf("Provider quota group %s 返回无效快照", group)
	}
	if snapshot.SyncedAt.IsZero() {
		snapshot.SyncedAt = s.now()
	}
	if err := s.accounts.ReplaceQuotaWindowGroup(ctx, id, snapshot.SyncedAt, snapshot.Modes, snapshot.Windows); err != nil {
		return quotaRefreshResult{}, err
	}
	return quotaRefreshResult{Credential: value, Windows: snapshot.Windows, Modes: snapshot.Modes}, nil
}

func (s *Service) refreshQuotaMode(ctx context.Context, id uint64, mode string) (quotaRefreshResult, error) {
	value, err := s.accounts.Get(ctx, id)
	if err != nil {
		return quotaRefreshResult{}, mapRepositoryError(err)
	}
	adapter, ok := s.providers.Quota(value.Provider)
	if !ok {
		return quotaRefreshResult{}, fmt.Errorf("%s Quota Provider 未注册", value.Provider)
	}
	var window accountdomain.QuotaWindow
	var windows []accountdomain.QuotaWindow
	var syncedAt time.Time
	window, err = adapter.SyncQuotaMode(ctx, value, mode)
	windows = []accountdomain.QuotaWindow{window}
	syncedAt = s.now()
	if err != nil {
		return quotaRefreshResult{}, err
	}
	if syncedAt.IsZero() {
		syncedAt = s.now()
	}
	if err := s.accounts.ReplaceQuotaWindowGroup(ctx, id, syncedAt, nil, windows); err != nil {
		return quotaRefreshResult{}, err
	}
	return quotaRefreshResult{Credential: value, Windows: windows}, nil
}

func quotaSyncKey(accountID uint64, mode string) string {
	mode = strings.TrimSpace(mode)
	if isConsoleUsageQuotaMode(mode) {
		return "all:" + strconv.FormatUint(accountID, 10)
	}
	if isWebImagineQuotaMode(mode) || mode == "" {
		return "" + ":" + strconv.FormatUint(accountID, 10)
	}
	return mode + ":" + strconv.FormatUint(accountID, 10)
}

func quotaWindowByMode(windows []accountdomain.QuotaWindow, mode string) (accountdomain.QuotaWindow, bool) {
	for _, window := range windows {
		if window.Mode == mode {
			return window, true
		}
	}
	return accountdomain.QuotaWindow{}, false
}

func (s *Service) reconcileQuotaRecoveryWindows(ctx context.Context, providerValue accountdomain.Provider, accountID uint64, windows []accountdomain.QuotaWindow) error {
	for _, window := range windows {
		if err := s.reconcileQuotaRecoveryWindow(ctx, providerValue, accountID, window); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reconcileQuotaGroupWindows(ctx context.Context, providerValue accountdomain.Provider, accountID uint64, modes []string, windows []accountdomain.QuotaWindow) error {
	byMode := make(map[string]accountdomain.QuotaWindow, len(windows))
	for _, window := range windows {
		byMode[window.Mode] = window
	}
	for _, mode := range modes {
		if window, ok := byMode[mode]; ok {
			if err := s.reconcileQuotaRecoveryWindow(ctx, providerValue, accountID, window); err != nil {
				return err
			}
			continue
		}
	}
	return nil
}

func (s *Service) reconcileQuotaRecoveryWindow(ctx context.Context, providerValue accountdomain.Provider, accountID uint64, window accountdomain.QuotaWindow) error {
	return nil
}

// quotaRecoveryDueAt keeps upstream quota exhaustion recoverable even when
// the Provider reports no reset timestamp. Console uses a conservative
// predicted 24-hour probe window; generic remote windows retain the shorter
// fallback and transport failures use the recovery queue's bounded backoff.
func quotaRecoveryDueAt(window accountdomain.QuotaWindow, now time.Time, exhausted bool) *time.Time {
	if !exhausted {
		return nil
	}
	if window.ResetAt != nil && window.ResetAt.After(now) {
		value := *window.ResetAt
		return &value
	}
	if isConsoleUsageQuotaMode(window.Mode) {
		value := now.Add(consolePredictedQuotaProbeDelay)
		return &value
	}
	if window.Source == accountdomain.QuotaSourceUpstream {
		value := now.Add(unknownRemoteQuotaProbeDelay)
		return &value
	}
	return nil
}

