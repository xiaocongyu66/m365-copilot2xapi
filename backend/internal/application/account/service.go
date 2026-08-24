package account

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	accountdomain "M365Copilot2ApiX/backend/internal/domain/account"
	"M365Copilot2ApiX/backend/internal/infra/provider"
	"M365Copilot2ApiX/backend/internal/infra/security"
	"M365Copilot2ApiX/backend/internal/pkg/batch"
	"M365Copilot2ApiX/backend/internal/pkg/perfmetrics"
	"M365Copilot2ApiX/backend/internal/repository"
	"golang.org/x/sync/singleflight"
)

var (
	ErrDevicePending       = errors.New("Device OAuth 等待用户授权")
	ErrDeviceSlowDown      = errors.New("Device OAuth 轮询过快")
	ErrDeviceDenied        = errors.New("Device OAuth 已拒绝或过期")
	ErrInvalidFilter       = errors.New("账号筛选条件无效")
	ErrInvalidInput        = errors.New("账号参数无效")
	ErrInvalidImport       = errors.New("账号凭据格式无效")
	ErrImportLimit         = errors.New("导入账号数量超过限制")
	ErrExportLimit         = errors.New("导出账号数量超过限制")
	ErrNotFound            = errors.New("账号不存在")
	ErrUnsupported         = errors.New("账号来源不支持该操作")
	ErrConversionBusy      = errors.New("账号正在转换为 Grok Build")
	ErrConflict            = errors.New("账号操作存在冲突")
	ErrAccountPoolMismatch = errors.New("批量操作包含不属于当前号池的账号")
)

var ErrCredentialRefreshPermanent = errors.New("OAuth refresh token 已永久失效")
var errQuotaRefreshBusy = errors.New("额度同步已由其他实例执行")

const (
	// estimatedFreeTokenLimit is only a fallback until an upstream exhaustion
	// response supplies the account-specific actual/limit pair.
	estimatedFreeTokenLimit         int64         = 500_000
	freeUsageWindow                 time.Duration = 24 * time.Hour
	forcedRefreshMinInterval        time.Duration = 30 * time.Second
	paidProbeRetryInterval          time.Duration = 15 * time.Minute
	credentialRefreshAdvance        time.Duration = 3 * time.Minute
	credentialRefreshSafetyPoll     time.Duration = time.Minute
	credentialRefreshTimeout        time.Duration = 30 * time.Second
	credentialRefreshStateTTL       time.Duration = 5 * time.Second
	credentialStateWriteTimeout     time.Duration = 5 * time.Second
	credentialConfigurationRetry    time.Duration = 30 * time.Minute
	credentialRefreshBatchSize                    = 100
	credentialUnclassifiedAuthLimit               = 5
	managedTaskWorkerCeiling                      = 50
	quotaRefreshQueueSize                         = 4096
	quotaRefreshTimeout                           = 30 * time.Second
	quotaRefreshDirtyTTL                          = 24 * time.Hour
	quotaRefreshPollInterval                      = 500 * time.Millisecond
	quotaRefreshSharedPoll                        = time.Second
	quotaRefreshBackoffBase                       = time.Second
	quotaRefreshBackoffMax                        = time.Minute
	consoleQuotaRefreshMinInterval                = 30 * time.Second
	unknownRemoteQuotaProbeDelay    time.Duration = 5 * time.Minute
	consolePredictedQuotaProbeDelay time.Duration = 24 * time.Hour
	observedModelPersistInterval                  = 30 * time.Minute
	observedModelLocalCacheTTL                    = 5 * time.Second
	observedModelLockShards                       = 64
	maxCredentialExportAccounts                   = 100000
	maxCredentialImportAccounts                   = 100000
	credentialImportChunkSize                     = 100
	credentialImportPrepareWorkers                = 3
	maxQuotaResetAccounts                         = 10000
	quotaResetChunkSize                           = 500
	maxBatchUpdateAccounts                        = 10000
	maxBuildConversionAccounts                    = 1000
	maxWebConsoleSyncAccounts                     = 1000
	accountTaskBatchSize                          = 1000
	linkedDeleteRuntimeCleanupLimit               = 3 * time.Second
)

const permanentRefreshExpiredReason = "OAuth refresh token 已永久失效且 access token 已过期"

type quotaRefreshState struct {
	generation          uint64
	publishedGeneration uint64
	sharedGeneration    uint64
	queued              bool
	running             bool
	pending             bool
	failures            int
	nextAttemptAt       time.Time
}

type observedModelState struct {
	model       string
	persistedAt time.Time
}

type observedModelShard struct {
	sync.Mutex
	values        map[uint64]observedModelState
	lastCleanupAt time.Time
}

type quotaRefreshRequest struct {
	key       string
	accountID uint64
	mode      string
}

type quotaRefreshResult struct {
	Credential accountdomain.Credential
	Windows    []accountdomain.QuotaWindow
	Modes      []string
}

type QuotaRefreshStats struct {
	Pending int
	Queued  int
	Running int
}

type QuotaType string
type QuotaStatus string

const (
	QuotaTypeUnknown        QuotaType   = "unknown"
	QuotaTypeFree           QuotaType   = "free"
	QuotaTypePaid           QuotaType   = "paid"
	QuotaStatusActive       QuotaStatus = "active"
	QuotaStatusWaitingReset QuotaStatus = "waitingReset"
	QuotaStatusProbing      QuotaStatus = "probing"
)

type QuotaView struct {
	Type            QuotaType
	Source          string
	Confidence      string
	Unit            string
	Used            float64
	Limit           float64
	Remaining       float64
	UsagePercent    float64
	LimitKnown      bool
	WindowHours     int
	Observed        bool
	Confirmed       bool
	Status          QuotaStatus
	PeriodStart     string
	PeriodEnd       string
	ExhaustedAt     *time.Time
	NextProbeAt     *time.Time
	LastConfirmedAt *time.Time
}

type View struct {
	Credential         accountdomain.Credential
	Billing            *accountdomain.Billing
	Quota              QuotaView
	QuotaWindows []accountdomain.QuotaWindow
	// EnabledChanged is request-scoped update metadata. It is not persisted or
	// serialized directly; the HTTP layer uses it to avoid warning when a PATCH
	// merely repeats the account's existing enabled value.
	EnabledChanged bool
}

type UpdateInput struct {
	Name             *string
	Enabled          *bool
	Priority         *int
	MaxConcurrent    *int
	MinimumRemaining *float64
}

type CleanupStatus string

const (
	CleanupStatusCooldown       CleanupStatus = "cooldown"
	CleanupStatusDisabled       CleanupStatus = "disabled"
	CleanupStatusReauthRequired CleanupStatus = "reauthRequired"
)

type DeviceStartResult struct {
	SessionID               string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Interval                time.Duration
	ExpiresAt               time.Time
}

type ImportResult struct {
	Created    int
	Updated    int
	Skipped    int
	Failed     int
	AccountIDs []uint64
}

type BuildConversionStrategy string

const (
	BuildConversionAll     BuildConversionStrategy = "all"
	BuildConversionMissing BuildConversionStrategy = "missing"
)

type WebConsoleSyncStrategy string

const (
	WebConsoleSyncAll     WebConsoleSyncStrategy = "all"
	WebConsoleSyncMissing WebConsoleSyncStrategy = "missing"
)

type ImportedAccountObserver func(accountID uint64) error

// BatchProgressObserver 在单个账号任务结束后报告批次完成数。
type BatchProgressObserver func(completed, total int) error

type ExportResult struct {
	Data  []byte
	Count int
}

type ExportPageResult struct {
	ExportResult
	NextID        uint64
	SnapshotMaxID uint64
	HasMore       bool
}

type BuildConversionResult struct {
	Created         int
	Linked          int
	Skipped         int
	Failed          int
	BuildAccountIDs []uint64
}

type ListFilter struct {
	Provider  string
	QuotaType string
	Status    string
	Egress    string
	Renewal   string
	Risk      string
	// Agreement applies only to grok_web accounts.
	Agreement string
	// Association values are provider-specific: Web supports build, console, and combined filters;
	// Build and Console support only webLinked and webUnlinked.
	Association string
	Sort        repository.SortQuery
}

type Summary struct {
	Total      int64
	Available  int64
	Recovering int64
	Attention  int64
	Risk       int64
	Providers  map[string]ProviderSummary
	Recovery   RecoverySummary
	Issues     IssueSummary
}

type ProviderSummary struct {
	Total     int64
	Available int64
}

type RecoverySummary struct {
	Cooldown     int64
	WaitingReset int64
	Probing      int64
}

type IssueSummary struct {
	Disabled       int64
	ReauthRequired int64
}

func (s *Service) Summary(ctx context.Context) (Summary, error) {
	now := s.now()
	rows, err := s.accounts.Summarize(ctx, now)
	if err != nil {
		return Summary{}, err
	}
	result := Summary{Providers: make(map[string]ProviderSummary, len(accountdomain.Providers()))}
	for _, providerValue := range accountdomain.Providers() {
		result.Providers[string(providerValue)] = ProviderSummary{}
	}
	for _, row := range rows {
		result.Total += row.Total
		result.Available += row.Available
		result.Recovery.Cooldown += row.Cooldown
		result.Recovery.WaitingReset += row.WaitingReset
		result.Recovery.Probing += row.Probing
		result.Issues.Disabled += row.Disabled
		result.Issues.ReauthRequired += row.ReauthRequired
		result.Providers[row.Provider] = ProviderSummary{Total: row.Total, Available: row.Available}
	}
	result.Recovering = result.Recovery.Cooldown + result.Recovery.WaitingReset + result.Recovery.Probing
	result.Attention = result.Issues.Disabled + result.Issues.ReauthRequired
	return result, nil
}

// Service 负责 OAuth 账号接入、刷新、额度和持久化生命周期。
type Service struct {
	accounts            repository.AccountRepository
	audits              repository.AuditRepository
	deviceSessions      repository.DeviceSessionRepository
	sticky              repository.StickySessionRepository
	refreshLock         repository.DistributedLock
	concurrency         repository.ConcurrencyLimiter
	providers           *provider.Registry
	cipher              *security.Cipher
	refreshes           singleflight.Group
	billingSyncs        singleflight.Group
	quotaSyncs          singleflight.Group
	identitySyncs       singleflight.Group
	observedModelWrites singleflight.Group
	observedModelStore  repository.ObservedModelStateRepository
	refreshMu           sync.Mutex
	lastRefreshAt       map[uint64]time.Time
	observedModelShards [observedModelLockShards]observedModelShard
	quotaRefreshMu      sync.Mutex
	quotaRefreshes      map[string]*quotaRefreshState
	quotaRefreshQueue   chan quotaRefreshRequest
	quotaRefreshWake    chan struct{}
	conversionPool      *batch.Pool
	syncPool            *batch.Pool
	refreshPool         *batch.Pool
	// detectPool 专用于管理端「检测账号」，与额度同步/续期隔离，默认并发 32。
	detectPool             *batch.Pool
	credentialRefreshWake  chan struct{}
	autoCleanMu            sync.RWMutex
	autoClean              AutoCleanConfig
	autoCleanRevision      uint64
	autoCleanWake          chan struct{}
	logger                 *slog.Logger
	now                    func() time.Time
}

func (s *Service) QuotaRefreshStats() QuotaRefreshStats {
	s.quotaRefreshMu.Lock()
	defer s.quotaRefreshMu.Unlock()
	result := QuotaRefreshStats{}
	for _, state := range s.quotaRefreshes {
		if state == nil {
			continue
		}
		if state.pending || state.queued || state.running {
			result.Pending++
		}
		if state.queued {
			result.Queued++
		}
		if state.running {
			result.Running++
		}
	}
	return result
}

// SetConcurrencyLimiter 让账号维护任务读取与推理路由相同的活动租约。
func (s *Service) SetConcurrencyLimiter(value repository.ConcurrencyLimiter) {
	s.concurrency = value
}

// SetObservedModelStore enables best-effort cross-instance duplicate suppression.
func (s *Service) SetObservedModelStore(value repository.ObservedModelStateRepository) {
	s.observedModelStore = value
}

func NewService(accounts repository.AccountRepository, audits repository.AuditRepository, deviceSessions repository.DeviceSessionRepository, sticky repository.StickySessionRepository, providers *provider.Registry, cipher *security.Cipher, refreshLock repository.DistributedLock) *Service {
	return &Service{
		accounts: accounts, audits: audits, deviceSessions: deviceSessions, sticky: sticky,
		providers: providers, cipher: cipher, refreshLock: refreshLock,
		lastRefreshAt: make(map[uint64]time.Time), quotaRefreshes: make(map[string]*quotaRefreshState),
		quotaRefreshQueue:     make(chan quotaRefreshRequest, quotaRefreshQueueSize),
		quotaRefreshWake:      make(chan struct{}, 1),
		credentialRefreshWake: make(chan struct{}, 1),
		autoClean: AutoCleanConfig{
			Enabled: false, Interval: 10 * time.Minute, MinAge: time.Hour, IncludeDisabled: false,
		},
		autoCleanWake:     make(chan struct{}, 1),
		conversionPool:    batch.NewPool(25), syncPool: batch.NewPool(25), refreshPool: batch.NewPool(25), detectPool: batch.NewPool(32),
		logger: slog.Default(),
		now:    func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) SetBulkPool(pool *batch.Pool) {
	if pool != nil {
		s.conversionPool, s.syncPool, s.refreshPool = pool, pool, pool
	}
}

// SetTaskPools 为转换、同步和凭据刷新绑定独立分类并发池。
func (s *Service) SetTaskPools(conversion, syncPool, refresh *batch.Pool) {
	if conversion != nil {
		s.conversionPool = conversion
	}
	if syncPool != nil {
		s.syncPool = syncPool
	}
	if refresh != nil {
		s.refreshPool = refresh
	}
}

// SetDetectPool 绑定管理端「检测账号」专用并发池；nil 时保留现有池。
func (s *Service) SetDetectPool(pool *batch.Pool) {
	if pool != nil {
		s.detectPool = pool
	}
}

func (s *Service) SetLogger(logger *slog.Logger) {
	if logger != nil {
		s.logger = logger
	}
}

// ProviderDefinition 向账号同步编排层暴露只读生命周期策略，不泄露具体 Adapter。
func (s *Service) ProviderDefinition(value accountdomain.Provider) (provider.Definition, bool) {
	if s.providers == nil {
		return provider.Definition{}, false
	}
	return s.providers.Definition(value)
}


func (s *Service) Get(ctx context.Context, id uint64) (View, error) {
	value, err := s.accounts.Get(ctx, id)
	if err != nil {
		return View{}, mapRepositoryError(err)
	}
	view := View{Credential: value}
	if billing, err := s.accounts.GetBilling(ctx, id); err == nil {
		view.Billing = &billing
	} else if !errors.Is(err, repository.ErrNotFound) {
		return View{}, err
	}
	observedTokens, err := s.audits.SumTokensByAccountsSince(ctx, []uint64{id}, time.Now().UTC().Add(-freeUsageWindow))
	if err != nil {
		return View{}, err
	}
	view.Quota = newQuotaView(view.Billing, observedTokens[id], nil, value.ObservedModel, false)
	if windows, err := s.accounts.GetQuotaWindows(ctx, []uint64{id}); err == nil {
		view.QuotaWindows = windows[id]
	} else {
		return View{}, err
	}
	return view, nil
}

func (s *Service) credentialMetadata(value accountdomain.Credential) provider.CredentialMetadata {
	if s.providers == nil {
		return provider.CredentialMetadata{}
	}
	return s.providers.CredentialMetadata(value)
}

func (s *Service) ObserveResponseModel(ctx context.Context, id uint64, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	_, err, _ := s.observedModelWrites.Do(strconv.FormatUint(id, 10)+"\x00"+model, func() (any, error) {
		now := s.now()
		shard := s.observedModelShard(id)
		shard.Lock()
		if shard.values == nil {
			shard.values = make(map[uint64]observedModelState)
		}
		if shard.lastCleanupAt.IsZero() || now.Sub(shard.lastCleanupAt) >= observedModelPersistInterval {
			for accountID, state := range shard.values {
				if now.Sub(state.persistedAt) >= observedModelPersistInterval {
					delete(shard.values, accountID)
				}
			}
			shard.lastCleanupAt = now
		}
		state, exists := shard.values[id]
		localFresh := exists && state.model == model && observedModelStateIsFresh(now, state.persistedAt)
		if localFresh && (s.observedModelStore == nil || now.Sub(state.persistedAt) < observedModelLocalCacheTTL) {
			shard.Unlock()
			return nil, nil
		}
		shard.Unlock()
		if s.observedModelStore != nil {
			shared, ok, sharedErr := s.observedModelStore.GetObservedModelState(ctx, id)
			if sharedErr == nil && ok && shared.Model == model && observedModelStateIsFresh(now, shared.ObservedAt) {
				shard.Lock()
				shard.values[id] = observedModelState{model: model, persistedAt: now}
				shard.Unlock()
				return nil, nil
			}
		}
		updated := true
		if writer, ok := s.accounts.(repository.ObservedModelWriter); ok {
			var err error
			updated, err = writer.UpdateObservedModelIfNewer(ctx, id, model, now)
			if err != nil {
				return nil, err
			}
		} else if err := s.accounts.UpdateObservedModel(ctx, id, model, now); err != nil {
			return nil, err
		}
		if updated && s.observedModelStore != nil {
			_ = s.observedModelStore.SetObservedModelState(ctx, id, repository.ObservedModelState{Model: model, ObservedAt: now}, observedModelPersistInterval)
		}
		shard.Lock()
		current, exists := shard.values[id]
		if !exists || !current.persistedAt.After(now) {
			shard.values[id] = observedModelState{model: model, persistedAt: now}
		}
		shard.Unlock()
		return nil, nil
	})
	return err
}

func (s *Service) observedModelShard(id uint64) *observedModelShard {
	return &s.observedModelShards[id%observedModelLockShards]
}

func observedModelStateIsFresh(now, persistedAt time.Time) bool {
	elapsed := now.Sub(persistedAt)
	return elapsed >= 0 && elapsed < observedModelPersistInterval
}

func newQuotaView(billing *accountdomain.Billing, observedTokens int64, recovery any, observedModel string, buildSuperEntitled bool) QuotaView {
	// Upstream paid billing takes precedence and preserves reported quota values.
	if billing != nil && false {
		periodStart, periodEnd := billing.BillingPeriodStart, billing.BillingPeriodEnd
		if billing.UsagePeriodType != "" {
			periodStart, periodEnd = billing.UsagePeriodStart, billing.UsagePeriodEnd
		}
		result := QuotaView{Type: QuotaTypePaid, Source: "upstreamBilling", Confidence: "observed", Unit: "credits", UsagePercent: billing.CreditUsagePercent, Status: QuotaStatusActive, PeriodStart: periodStart, PeriodEnd: periodEnd}
		switch {
		case billing.MonthlyLimit > 0:
			result.Used = billing.Used
			result.Limit = billing.MonthlyLimit
			result.Remaining = billing.Remaining()
			result.UsagePercent = billing.Used / billing.MonthlyLimit * 100
			result.LimitKnown = true
		case billing.OnDemandCap > 0:
			result.Limit = billing.OnDemandCap
			result.Used = billing.OnDemandUsed
			if result.Used == 0 && billing.CreditUsagePercent > 0 {
				result.Used = billing.OnDemandCap * billing.CreditUsagePercent / 100
			}
			result.Remaining = billing.OnDemandCap - result.Used
			result.LimitKnown = true
			if result.Remaining < 0 {
				result.Remaining = 0
			}
		case billing.PrepaidBalance > 0:
			result.Remaining = billing.PrepaidBalance
		case billing.UsagePeriodType != "":
			result.Unit = "percent"
			result.Used = billing.CreditUsagePercent
			result.Limit = 100
			result.Remaining = max(0, 100-billing.CreditUsagePercent)
			result.LimitKnown = true
		}
		return result
	}
	// 管理员确认的 Build Super entitlement：覆盖 Free recovery / profile / observed free 等弱信号。
	// 不伪造额度、余额、使用率或账期；Billing 数值保持未知/零。
	if buildSuperEntitled {
		return QuotaView{
			Type: QuotaTypePaid, Source: "buildSuperEntitlement", Confidence: "confirmed",
			Confirmed: true, Status: QuotaStatusActive,
		}
	}
	freeSource := ""
	confidence := ""
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(observedModel)), "-build-free") {
		freeSource = "responseModel"
		confidence = "observed"
	} else if isEstimatedFreeBillingProfile(billing) {
		freeSource = "billingProfile"
		confidence = "estimated"
	}
	if freeSource == "" {
		return QuotaView{Type: QuotaTypeUnknown, Source: "unknown", Status: QuotaStatusActive}
	}
	if observedTokens < 0 {
		observedTokens = 0
	}
	remaining := estimatedFreeTokenLimit - observedTokens
	if remaining < 0 {
		remaining = 0
	}
	return QuotaView{
		Type:         QuotaTypeFree,
		Source:       freeSource,
		Confidence:   confidence,
		Unit:         "tokens",
		Used:         float64(observedTokens),
		Limit:        float64(estimatedFreeTokenLimit),
		Remaining:    float64(remaining),
		UsagePercent: float64(observedTokens) / float64(estimatedFreeTokenLimit) * 100,
		LimitKnown:   false,
		WindowHours:  int(freeUsageWindow / time.Hour),
		Observed:     true,
		Status:       QuotaStatusActive,
	}
}

func isEstimatedFreeBillingProfile(billing *accountdomain.Billing) bool {
	return false
}

// StartDeviceLogin 启动短期 Device OAuth，会话只保存在有界运行态存储中。

func (s *Service) Update(ctx context.Context, id uint64, input UpdateInput) (View, error) {
	value, err := s.accounts.Get(ctx, id)
	if err != nil {
		return View{}, mapRepositoryError(err)
	}
	enabledChanged := input.Enabled != nil && value.Enabled != *input.Enabled
	if input.Name != nil {
		value.Name = strings.TrimSpace(*input.Name)
		if value.Name == "" {
			return View{}, invalidInput("账号名称不能为空")
		}
	}
	if input.Enabled != nil {
		value.Enabled = *input.Enabled
	}
	if input.Priority != nil {
		value.Priority = *input.Priority
	}
	if input.MaxConcurrent != nil {
		if *input.MaxConcurrent < 1 || *input.MaxConcurrent > accountdomain.MaxConcurrent {
			return View{}, invalidInput("maxConcurrent 必须在 1 到 256 之间")
		}
		value.MaxConcurrent = *input.MaxConcurrent
	}
	if input.MinimumRemaining != nil {
		if *input.MinimumRemaining < 0 {
			return View{}, invalidInput("minimumRemaining 不能小于零")
		}
		value.MinimumRemaining = *input.MinimumRemaining
	}
	updated, err := s.accounts.Update(ctx, value)
	if err != nil {
		return View{}, mapRepositoryError(err)
	}
	if !updated.Enabled && s.sticky != nil {
		_ = s.sticky.DeleteByAccount(ctx, updated.ID)
	} else if updated.Enabled && s.providers != nil && s.providers.SupportsCredentialRefresh(updated.Provider) {
		s.WakeCredentialRefresh()
	}
	view, err := s.Get(ctx, updated.ID)
	if err != nil {
		return View{}, err
	}
	view.EnabledChanged = enabledChanged
	return view, nil
}

// ClearCooldown resets request-path health so a cooled account can be
// scheduled again. UpdateHealth publishes InvalidationAccountHealthChanged,
// which overwrites the selector memory overlay (runtimeStore=memory).
func (s *Service) ClearCooldown(ctx context.Context, id uint64) (View, error) {
	value, err := s.accounts.Get(ctx, id)
	if err != nil {
		return View{}, mapRepositoryError(err)
	}
	// missing_thinking is a durable quality strike, not a transient cooldown
	// error. Clearing the timer must not turn the next miss into another first
	// strike and bypass the second-miss disable policy.
	healthMarker := accountdomain.NormalizeHealthMarker(value.LastError)
	if err := s.accounts.UpdateHealth(ctx, value.ID, value.Provider, 0, nil, healthMarker, false); err != nil {
		return View{}, mapRepositoryError(err)
	}
	return s.Get(ctx, id)
}

// MarkBuildAPIFallback is retained for API compatibility but is a no-op for M365.
func (s *Service) MarkBuildAPIFallback(ctx context.Context, id uint64, enabled bool) error {
	return nil
}

func (s *Service) Delete(ctx context.Context, id uint64) error {
	// Single-account delete must preserve ErrNotFound when the root row is gone
	// (BatchDeleteWithLinked/DeleteMany return deleted=0, nil for missing IDs).
	result, err := s.DeleteWithLinked(ctx, accountdomain.Provider(""), id, nil)
	if err != nil {
		return err
	}
	if result.Deleted == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWithLinked deletes one account and optional linked peers.
// A single delete is rejected if any account in the final group has an active video job.
func (s *Service) DeleteWithLinked(ctx context.Context, providerValue accountdomain.Provider, id uint64, targets []accountdomain.Provider) (AccountDeleteResult, error) {
	if id == 0 {
		return AccountDeleteResult{}, invalidInput("账号 ID 无效")
	}
	result, err := s.batchDeleteWithLinkedMode(ctx, providerValue, []uint64{id}, targets, false)
	if err != nil {
		return result, err
	}
	// Fail closed for the single-root API: missing root must not report success.
	if result.Deleted == 0 {
		return result, ErrNotFound
	}
	return result, nil
}

// PreviewLinkedDelete returns root/linked counts for the delete confirmation UI.
func (s *Service) PreviewLinkedDelete(ctx context.Context, providerValue accountdomain.Provider, ids []uint64, targets []accountdomain.Provider) (repository.LinkedDeleteResolution, error) {
	ids, err := normalizeBatchIDs(ids)
	if err != nil {
		return repository.LinkedDeleteResolution{}, err
	}
	if !providerValue.IsValid() {
		return repository.LinkedDeleteResolution{}, invalidInput("账号来源无效")
	}
	resolution, err := s.accounts.ResolveLinkedDeleteIDs(ctx, providerValue, ids, targets)
	if err != nil {
		return repository.LinkedDeleteResolution{}, mapLinkedDeleteError(err)
	}
	return resolution, nil
}

func (s *Service) MarkReauthRequired(ctx context.Context, id uint64, reason string) error {
	value, err := s.accounts.Get(ctx, id)
	if err != nil {
		return mapRepositoryError(err)
	}
	value.AuthStatus = accountdomain.AuthStatusReauthRequired
	value.LastError = reason
	if len(value.LastError) > 512 {
		value.LastError = value.LastError[:512]
	}
	if _, err := s.accounts.Update(ctx, value); err != nil {
		return mapRepositoryError(err)
	}
	if s.sticky != nil {
		_ = s.sticky.DeleteByAccount(ctx, id)
	}
	return nil
}

func (s *Service) EnsureCredential(ctx context.Context, value accountdomain.Credential, force bool) (accountdomain.Credential, error) {
	return s.ensureCredential(ctx, value, ensureCredentialOptions{force: force})
}

type ensureCredentialOptions struct {
	force              bool
	bypassCooldown     bool
	respectSchedule    bool
	retryPermanentOnce bool
}

func (s *Service) ensureCredential(ctx context.Context, value accountdomain.Credential, options ensureCredentialOptions) (accountdomain.Credential, error) {
	if s.providers == nil || !s.providers.SupportsCredentialRefresh(value.Provider) {
		if options.force {
			return accountdomain.Credential{}, ErrUnsupported
		}
		return value, nil
	}
	now := s.now()
	if credential, err, handled := s.resolvePermanentRefreshFailure(ctx, value, now, options.force, options.retryPermanentOnce); handled {
		return credential, err
	}
	if !options.force && value.ExpiresAt.IsZero() && value.EncryptedAccessToken != "" {
		return value, nil
	}
	if !options.force && value.EncryptedAccessToken != "" && !value.ExpiresAt.IsZero() && now.Add(credentialRefreshAdvance).Before(value.ExpiresAt) {
		return value, nil
	}
	refreshKey := strconv.FormatUint(value.ID, 10)
	if options.respectSchedule {
		refreshKey += ":scheduled"
	}
	if options.retryPermanentOnce {
		refreshKey += ":manual-retry"
	}
	result, err, _ := s.refreshes.Do(refreshKey, func() (any, error) {
		latest, err := s.accounts.Get(ctx, value.ID)
		if err != nil {
			return nil, err
		}
		currentTime := s.now()
		if credential, err, handled := s.resolvePermanentRefreshFailure(ctx, latest, currentTime, options.force, options.retryPermanentOnce); handled {
			if err != nil {
				return nil, err
			}
			return credential, nil
		}
		if options.respectSchedule && latest.RefreshDueAt != nil && latest.RefreshDueAt.After(currentTime) {
			return latest, nil
		}
		if options.force && latest.EncryptedAccessToken != "" && latest.EncryptedAccessToken != value.EncryptedAccessToken {
			return latest, nil
		}
		if !options.force && latest.EncryptedAccessToken != "" && !latest.ExpiresAt.IsZero() && currentTime.Add(credentialRefreshAdvance).Before(latest.ExpiresAt) {
			return latest, nil
		}
		if options.force && !options.bypassCooldown && s.credentialRefreshCoolingDown(latest, currentTime) {
			return latest, nil
		}
		release, err := s.acquireRefreshLock(ctx, latest.ID)
		if err != nil {
			return nil, err
		}
		if release != nil {
			defer release()
			latest, err = s.accounts.Get(ctx, value.ID)
			if err != nil {
				return nil, err
			}
			currentTime = s.now()
			if credential, err, handled := s.resolvePermanentRefreshFailure(ctx, latest, currentTime, options.force, options.retryPermanentOnce); handled {
				if err != nil {
					return nil, err
				}
				return credential, nil
			}
			if options.respectSchedule && latest.RefreshDueAt != nil && latest.RefreshDueAt.After(currentTime) {
				return latest, nil
			}
			if options.force && !options.bypassCooldown && s.credentialRefreshCoolingDown(latest, currentTime) {
				return latest, nil
			}
			if latest.EncryptedAccessToken != "" && latest.EncryptedAccessToken != value.EncryptedAccessToken {
				return latest, nil
			}
			if !options.force && latest.EncryptedAccessToken != "" && !latest.ExpiresAt.IsZero() && currentTime.Add(credentialRefreshAdvance).Before(latest.ExpiresAt) {
				return latest, nil
			}
		}
		adapter, ok := s.providers.CredentialRefresh(latest.Provider)
		if !ok {
			return nil, fmt.Errorf("Provider %s 未注册", latest.Provider)
		}
		refreshed, err := adapter.RefreshCredential(ctx, latest)
		if err != nil {
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialRefreshStateTTL)
			s.recordCredentialRefreshFailure(persistCtx, latest, err, !options.retryPermanentOnce, release != nil)
			cancel()
			return nil, err
		}
		persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), credentialStateWriteTimeout)
		updated, err := s.accounts.UpdateTokens(persistCtx, latest.ID, refreshed.EncryptedAccessToken, refreshed.EncryptedRefreshToken, refreshed.ExpiresAt)
		cancelPersist()
		if err != nil {
			s.logger.Error("credential_refresh_token_write_failed",
				"account_id", latest.ID,
				"provider", latest.Provider,
				"refresh_token_rotated", refreshed.RefreshTokenRotated,
				"egress_node_id", latest.EgressNodeID,
				"build_api_fallback_marked", false,
				"distributed_lock", release != nil,
				"error", err,
			)
			return nil, err
		}
		s.markRefreshSuccess(latest.ID, currentTime)
		s.WakeCredentialRefresh()
		return updated, nil
	})
	if err != nil {
		return accountdomain.Credential{}, err
	}
	credential, ok := result.(accountdomain.Credential)
	if !ok {
		return accountdomain.Credential{}, fmt.Errorf("账号凭据刷新返回类型无效")
	}
	return credential, nil
}

// acquireRefreshLock 在 Redis 模式下等待其他实例完成刷新，锁租约过期后可自动接管。
func (s *Service) acquireRefreshLock(ctx context.Context, accountID uint64) (func(), error) {
	if s.refreshLock == nil {
		return nil, nil
	}
	key := "credential-refresh:" + strconv.FormatUint(accountID, 10)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		release, acquired, err := s.refreshLock.Acquire(ctx, key, 2*time.Minute)
		if err != nil {
			return nil, err
		}
		if acquired {
			return release, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) RefreshToken(ctx context.Context, id uint64) (View, error) {
	value, err := s.accounts.Get(ctx, id)
	if err != nil {
		return View{}, mapRepositoryError(err)
	}
	if _, err := s.ensureCredential(ctx, value, ensureCredentialOptions{force: true, bypassCooldown: true, retryPermanentOnce: true}); err != nil {
		return View{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) refreshCoolingDown(accountID uint64, now time.Time) bool {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	last := s.lastRefreshAt[accountID]
	return !last.IsZero() && now.Sub(last) < forcedRefreshMinInterval
}

func (s *Service) credentialRefreshCoolingDown(credential accountdomain.Credential, now time.Time) bool {
	if credential.LastRefreshAt != nil {
		age := now.Sub(*credential.LastRefreshAt)
		if age >= 0 && age < forcedRefreshMinInterval {
			return true
		}
	}
	return s.refreshCoolingDown(credential.ID, now)
}

func (s *Service) markRefreshSuccess(accountID uint64, now time.Time) {
	s.refreshMu.Lock()
	s.lastRefreshAt[accountID] = now
	s.refreshMu.Unlock()
}

func (s *Service) clearRefreshState(accountID uint64) {
	s.refreshMu.Lock()
	delete(s.lastRefreshAt, accountID)
	s.refreshMu.Unlock()
}

func (s *Service) recordCredentialRefreshFailure(ctx context.Context, credential accountdomain.Credential, refreshErr error, preservePermanent, distributedLock bool) {
	if errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	failureCount := credential.RefreshFailureCount + 1
	errorCode := "oauth_transport_error"
	errorMessage := "OAuth request failed"
	errorStatus := 0
	errorResponse := ""
	permanent := false
	retryAfter := time.Duration(0)
	var typed *provider.CredentialRefreshError
	if errors.As(refreshErr, &typed) {
		errorCode = strings.TrimSpace(typed.Code)
		if errorCode == "" {
			errorCode = "oauth_refresh_error"
		}
		errorStatus = typed.Status
		permanent = typed.Permanent
		retryAfter = typed.RetryAfter
		if message := normalizeCredentialRefreshErrorMessage(typed.Message); message != "" {
			errorMessage = message
		}
		errorResponse = normalizeCredentialRefreshErrorResponse(typed.Response)
	} else if errors.Is(refreshErr, context.DeadlineExceeded) {
		errorCode = "oauth_timeout"
		errorMessage = "OAuth request timed out"
	}
	// Defend against adapters or historical rows that classified every OAuth
	// 400/401 as terminal. Only explicit credential-specific terminal codes may
	// stop future refresh attempts.
	if permanent && !provider.IsPermanentCredentialRefreshErrorCode(errorCode) {
		permanent = false
	}
	// 真正的 OAuth 永久失败（invalid_grant 等）只能由成功换 token 清除。
	// 非终态错误不得被旧的 status-only permanent 分类粘住。
	if preservePermanent && credential.RefreshPermanent && !isRecoverableRefreshErrorCode(credential.LastRefreshErrorCode) && !isRecoverableRefreshErrorCode(errorCode) {
		permanent = true
	}
	now := s.now()
	unclassifiedAuthFailure := provider.IsUnclassifiedCredentialAuthRejection(errorStatus, errorCode)
	configurationError := provider.IsCredentialRefreshConfigurationErrorCode(errorCode)
	unclassifiedAuthFailureCount := 0
	if unclassifiedAuthFailure {
		unclassifiedAuthFailureCount = 1
		if credential.LastRefreshErrorStatus == errorStatus && strings.EqualFold(strings.TrimSpace(credential.LastRefreshErrorCode), strings.TrimSpace(errorCode)) {
			unclassifiedAuthFailureCount = credential.RefreshUnclassifiedAuthCount + 1
		}
	}
	retryAt := now.Add(credentialRefreshBackoff(credential.ID, failureCount, retryAfter))
	if configurationError && retryAt.Before(now.Add(credentialConfigurationRetry)) {
		retryAt = now.Add(credentialConfigurationRetry)
	}
	accessTokenAlive := credential.EncryptedAccessToken != "" && !credential.ExpiresAt.IsZero() && credential.ExpiresAt.After(now)
	requiresReauth := unclassifiedAuthFailure && !accessTokenAlive && unclassifiedAuthFailureCount >= credentialUnclassifiedAuthLimit
	if permanent && accessTokenAlive {
		// refresh token 已永久失效时，提前重试没有意义；到 access token 到期时再完成失效收敛。
		retryAt = credential.ExpiresAt
	} else if permanent {
		retryAt = now
	}
	if err := s.accounts.UpdateCredentialRefreshFailure(ctx, credential.ID, repository.CredentialRefreshFailure{
		Count: failureCount, UnclassifiedAuthFailureCount: unclassifiedAuthFailureCount,
		RetryAt: retryAt, Status: errorStatus, Code: errorCode,
		Message: errorMessage, Response: errorResponse, Permanent: permanent,
	}); err != nil {
		s.logger.Warn("credential_refresh_state_write_failed", "account_id", credential.ID, "error", err)
	}
	s.logger.Warn("credential_refresh_failed",
		"account_id", credential.ID,
		"provider", credential.Provider,
		"http_status", errorStatus,
		"error_code", errorCode,
		"error_message", errorMessage,
		"permanent", permanent,
		"failure_count", failureCount,
		"unclassified_auth_failure", unclassifiedAuthFailure,
		"unclassified_auth_failure_count", unclassifiedAuthFailureCount,
		"configuration_error", configurationError,
		"requires_reauth", requiresReauth,
		"retry_at", retryAt,
		"access_token_alive", accessTokenAlive,
		"refresh_token_rotated", false,
		"egress_node_id", credential.EgressNodeID,
		"build_api_fallback_marked", false,
		"distributed_lock", distributedLock,
	)
	if permanent && accessTokenAlive {
		s.logger.Warn("credential_refresh_permanent_but_token_alive", "account_id", credential.ID, "error_code", errorCode, "expires_at", credential.ExpiresAt, "retry_at", retryAt)
		s.WakeCredentialRefresh()
		return
	}
	if permanent {
		if err := s.MarkReauthRequired(ctx, credential.ID, "OAuth refresh failed: "+errorCode); err != nil {
			s.logger.Warn("credential_refresh_reauth_mark_failed", "account_id", credential.ID, "error", err)
		}
		return
	}
	if requiresReauth {
		if err := s.MarkReauthRequired(ctx, credential.ID, "OAuth refresh repeatedly rejected without a classifiable error"); err != nil {
			s.logger.Warn("credential_refresh_unclassified_reauth_mark_failed", "account_id", credential.ID, "error", err)
			return
		}
		s.logger.Warn("credential_refresh_unclassified_reauth_required",
			"account_id", credential.ID,
			"http_status", errorStatus,
			"error_code", errorCode,
			"failure_count", unclassifiedAuthFailureCount,
		)
		return
	}
	s.logger.Warn("credential_refresh_deferred", "account_id", credential.ID, "failure_count", failureCount, "retry_at", retryAt, "error_code", errorCode)
	s.WakeCredentialRefresh()
}

func normalizeCredentialRefreshErrorMessage(value string) string {
	value = strings.Map(func(char rune) rune {
		switch char {
		case '\r', '\n', '\t':
			return ' '
		}
		if char < 0x20 || char == 0x7f {
			return -1
		}
		return char
	}, strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 512 {
		value = string(runes[:511]) + "…"
	}
	return value
}

func normalizeCredentialRefreshErrorResponse(value string) string {
	value = strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return ' '
		}
		return char
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > 4096 {
		value = string(runes[:4095]) + "…"
	}
	return value
}

// resolvePermanentRefreshFailure 阻止自动链路再次请求已确认失效的 refresh token，
// 并在 access token 到期后收敛账号状态。管理员显式刷新可通过
// retryPermanentOnce 绕过一次；credential_decrypt_failed 等可恢复本地错误不受阻断。
func (s *Service) resolvePermanentRefreshFailure(ctx context.Context, credential accountdomain.Credential, now time.Time, force, retryPermanentOnce bool) (accountdomain.Credential, error, bool) {
	if !credential.RefreshPermanent {
		return accountdomain.Credential{}, nil, false
	}
	if isRecoverableRefreshErrorCode(credential.LastRefreshErrorCode) {
		// 允许 force 或到期调度再次尝试解密/刷新；成功后会 clear permanent 标记。
		return accountdomain.Credential{}, nil, false
	}
	if retryPermanentOnce {
		return accountdomain.Credential{}, nil, false
	}
	accessTokenAlive := credential.EncryptedAccessToken != "" && !credential.ExpiresAt.IsZero() && credential.ExpiresAt.After(now)
	if accessTokenAlive && !force {
		return credential, nil, true
	}
	if !accessTokenAlive {
		if err := s.MarkReauthRequired(ctx, credential.ID, permanentRefreshExpiredReason); err != nil {
			return accountdomain.Credential{}, err, true
		}
	}
	if credential.LastRefreshErrorCode == "" {
		return accountdomain.Credential{}, ErrCredentialRefreshPermanent, true
	}
	return accountdomain.Credential{}, fmt.Errorf("%w: %s", ErrCredentialRefreshPermanent, credential.LastRefreshErrorCode), true
}

// isRecoverableRefreshErrorCode 标识“永久标记可被后续成功刷新清除”的本地/临时错误。
func isRecoverableRefreshErrorCode(code string) bool {
	return !provider.IsPermanentCredentialRefreshErrorCode(code)
}

func credentialRefreshBackoff(accountID uint64, failureCount int, retryAfter time.Duration) time.Duration {
	delays := [...]time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute}
	index := max(0, min(failureCount-1, len(delays)-1))
	delay := delays[index]
	if retryAfter > delay {
		delay = min(retryAfter, 30*time.Minute)
	}
	return delay + time.Duration((accountID*37)%16)*time.Second
}

// QueueQuotaRefresh asynchronously refreshes the remote quota window after a successful request.
func (s *Service) QueueQuotaRefresh(id uint64, mode string) {
	mode = strings.TrimSpace(mode)
	if id == 0 || mode == "" {
		return
	}
	key := strconv.FormatUint(id, 10) + ":" + mode
	s.quotaRefreshMu.Lock()
	state := s.quotaRefreshes[key]
	now := s.now().UTC()
	if state != nil && !state.pending && !state.queued && !state.running && !now.Before(state.nextAttemptAt) {
		delete(s.quotaRefreshes, key)
		state = nil
	}
	if state == nil {
		state = &quotaRefreshState{}
		s.quotaRefreshes[key] = state
	}
	state.generation++
	state.pending = true
	enqueued := state.queued || state.running || now.Before(state.nextAttemptAt) || s.enqueueQuotaRefreshLocked(quotaRefreshRequest{key: key, accountID: id, mode: mode}, state)
	s.quotaRefreshMu.Unlock()
	if !enqueued {
		perfmetrics.Default.Add("quota_refresh_events", perfmetrics.Labels{Subsystem: "quota", Stage: "enqueue", Outcome: "queue_full"}, 1)
		s.logger.Warn("quota_refresh_queue_full", "account_id", id, "mode", mode)
		s.wakeQuotaRefreshRecovery()
	}
}

func (s *Service) enqueueQuotaRefreshLocked(request quotaRefreshRequest, state *quotaRefreshState) bool {
	if state == nil || state.queued || state.running {
		return state != nil
	}
	select {
	case s.quotaRefreshQueue <- request:
		state.queued = true
		return true
	default:
		return false
	}
}

func (s *Service) wakeQuotaRefreshRecovery() {
	select {
	case s.quotaRefreshWake <- struct{}{}:
	default:
	}
}

// RunQuotaRefresh uses a fixed worker set to avoid unbounded goroutine creation.
func (s *Service) RunQuotaRefresh(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(managedTaskWorkerCeiling + 1)
	for range managedTaskWorkerCeiling {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case request := <-s.quotaRefreshQueue:
					s.quotaRefreshMu.Lock()
					state := s.quotaRefreshes[request.key]
					if state == nil || state.running {
						s.quotaRefreshMu.Unlock()
						continue
					}
					state.queued = false
					state.running = true
					state.pending = false
					s.quotaRefreshMu.Unlock()
					if err := batch.Do(ctx, func(workCtx context.Context) error {
						s.runQuotaRefresh(workCtx, request)
						return nil
					}); err != nil {
						s.quotaRefreshMu.Lock()
						if state := s.quotaRefreshes[request.key]; state != nil {
							state.running = false
							state.pending = true
							state.failures++
							state.nextAttemptAt = s.now().UTC().Add(quotaRefreshRetryDelay(state.failures))
						}
						s.quotaRefreshMu.Unlock()
						s.wakeQuotaRefreshRecovery()
						if ctx.Err() == nil {
							var panicErr *batch.PanicError
							if errors.As(err, &panicErr) {
								s.logger.Error("quota_refresh_worker_panicked", "account_id", request.accountID, "mode", request.mode, "error", panicErr, "stack", string(panicErr.Stack))
							} else {
								s.logger.Error("quota_refresh_worker_failed", "account_id", request.accountID, "mode", request.mode, "error", err)
							}
						}
					}
				}
			}
		}()
	}
	go func() {
		defer workers.Done()
		s.runQuotaRefreshRecovery(ctx)
	}()
	workers.Wait()
}

func (s *Service) runQuotaRefresh(parent context.Context, request quotaRefreshRequest) {
	for {
		s.quotaRefreshMu.Lock()
		state := s.quotaRefreshes[request.key]
		if state == nil {
			s.quotaRefreshMu.Unlock()
			return
		}
		localGeneration := state.generation
		state.pending = false
		s.quotaRefreshMu.Unlock()

		ctx, cancel := context.WithTimeout(parent, quotaRefreshTimeout)
		refreshMode := request.mode
		consoleMode := isConsoleUsageQuotaMode(request.mode)
		skipUpstream := false
		if windows, err := s.accounts.GetQuotaWindows(ctx, []uint64{request.accountID}); err == nil {
			if consoleMode {
				for _, window := range windows[request.accountID] {
					if window.Mode == request.mode && window.SyncedAt != nil && s.now().UTC().Sub(window.SyncedAt.UTC()) < consoleQuotaRefreshMinInterval {
						skipUpstream = true
						break
					}
				}
			} else if request.mode != "" {
				// Weekly remains a Grok Web capability. Console never inherits this
				// legacy mode and always refreshes its authoritative /usage snapshot.
				// Imagine 配额组走 /rest/media/imagine/quota_info，不可被改刷 weekly。
				for _, window := range windows[request.accountID] {
					if window.Mode == "weekly" {
						refreshMode = "weekly"
						break
					}
				}
			}
		}
		var refreshErr error
		acquired := true
		var release func()
		if !skipUpstream && s.refreshLock != nil {
			effectiveKey := strconv.FormatUint(request.accountID, 10) + ":" + refreshMode
			if consoleMode {
				// Every Console mode reads the same /usage snapshot. Serialize all
				// three kinds across instances to avoid duplicate upstream probes.
				effectiveKey = "console:" + strconv.FormatUint(request.accountID, 10)
			}
			release, acquired, refreshErr = s.refreshLock.Acquire(ctx, "quota-refresh:"+effectiveKey, quotaRefreshTimeout)
		}
		if !skipUpstream && refreshErr == nil && acquired {
			if err := s.syncPool.Do(ctx, func(workCtx context.Context) error {
				if refreshMode == "" {
					var refreshed quotaRefreshResult
					refreshed, refreshErr = s.refreshQuotaGroup(workCtx, request.accountID, refreshMode)
					if refreshErr == nil {
						refreshErr = s.reconcileQuotaGroupWindows(workCtx, refreshed.Credential.Provider, request.accountID, refreshed.Modes, refreshed.Windows)
					}
				} else {
					_, refreshErr = s.RefreshQuotaMode(workCtx, request.accountID, refreshMode)
				}
				return refreshErr
			}); err != nil {
				refreshErr = err
			}
		}
		if release != nil {
			release()
		}
		cancel()
		if refreshErr != nil || !acquired {
			if refreshErr != nil && !errors.Is(refreshErr, context.Canceled) {
				s.logger.Warn("quota_refresh_failed", "account_id", request.accountID, "mode", refreshMode, "error", refreshErr)
			}
			s.deferQuotaRefresh(request.key)
			perfmetrics.Default.Add("quota_refresh_events", perfmetrics.Labels{Subsystem: "quota", Stage: "refresh", Outcome: "retry"}, 1)
			return
		}

		s.quotaRefreshMu.Lock()
		state = s.quotaRefreshes[request.key]
		localChanged := state != nil && state.generation != localGeneration
		s.quotaRefreshMu.Unlock()
		if localChanged {
			perfmetrics.Default.Add("quota_refresh_events", perfmetrics.Labels{Subsystem: "quota", Stage: "refresh", Outcome: "trailing"}, 1)
			if consoleMode {
				s.deferSuccessfulQuotaRefresh(request.key, true)
				return
			}
			continue
		}
		s.quotaRefreshMu.Lock()
		state = s.quotaRefreshes[request.key]
		if state != nil && state.generation == localGeneration {
			if consoleMode {
				state.running = false
				state.pending = false
				state.failures = 0
				state.nextAttemptAt = s.now().UTC().Add(consoleQuotaRefreshMinInterval)
			} else {
				delete(s.quotaRefreshes, request.key)
			}
			s.quotaRefreshMu.Unlock()
			perfmetrics.Default.Add("quota_refresh_events", perfmetrics.Labels{Subsystem: "quota", Stage: "refresh", Outcome: "success"}, 1)
			return
		}
		if consoleMode && state != nil {
			state.running = false
			state.pending = true
			state.failures = 0
			state.nextAttemptAt = s.now().UTC().Add(consoleQuotaRefreshMinInterval)
			s.quotaRefreshMu.Unlock()
			s.wakeQuotaRefreshRecovery()
			return
		}
		s.quotaRefreshMu.Unlock()
	}
}

func (s *Service) deferQuotaRefresh(key string) {
	s.quotaRefreshMu.Lock()
	if state := s.quotaRefreshes[key]; state != nil {
		state.running = false
		state.pending = true
		state.failures++
		state.nextAttemptAt = s.now().UTC().Add(quotaRefreshRetryDelay(state.failures))
	}
	s.quotaRefreshMu.Unlock()
	s.wakeQuotaRefreshRecovery()
}

func (s *Service) deferSuccessfulQuotaRefresh(key string, pending bool) {
	s.quotaRefreshMu.Lock()
	if state := s.quotaRefreshes[key]; state != nil {
		state.running = false
		state.pending = pending
		state.failures = 0
		state.nextAttemptAt = s.now().UTC().Add(consoleQuotaRefreshMinInterval)
	}
	s.quotaRefreshMu.Unlock()
	s.wakeQuotaRefreshRecovery()
}

func quotaRefreshRetryDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	shift := min(failures-1, 6)
	delay := quotaRefreshBackoffBase * time.Duration(1<<shift)
	if delay > quotaRefreshBackoffMax {
		delay = quotaRefreshBackoffMax
	}
	// Equal jitter keeps retries bounded away from zero while preventing a
	// shared upstream outage from synchronizing every account worker.
	half := delay / 2
	if half <= 0 {
		return delay
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func (s *Service) runQuotaRefreshRecovery(ctx context.Context) {
	retryTicker := time.NewTicker(quotaRefreshPollInterval)
	sharedTicker := time.NewTicker(quotaRefreshSharedPoll)
	defer retryTicker.Stop()
	defer sharedTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.quotaRefreshWake:
			s.requeueQuotaRefreshes()
		case <-retryTicker.C:
			s.requeueQuotaRefreshes()
		case now := <-sharedTicker.C:
			s.recoverSharedQuotaRefreshes(ctx, now.UTC())
			s.requeueQuotaRefreshes()
		}
	}
}

func (s *Service) requeueQuotaRefreshes() {
	now := s.now().UTC()
	s.quotaRefreshMu.Lock()
	for key, state := range s.quotaRefreshes {
		if state == nil {
			delete(s.quotaRefreshes, key)
			continue
		}
		if !state.pending {
			if !state.queued && !state.running && !now.Before(state.nextAttemptAt) {
				delete(s.quotaRefreshes, key)
			}
			continue
		}
		if state.queued || state.running || now.Before(state.nextAttemptAt) {
			continue
		}
		separator := strings.IndexByte(key, ':')
		if separator <= 0 || separator == len(key)-1 {
			continue
		}
		accountID, err := strconv.ParseUint(key[:separator], 10, 64)
		if err != nil {
			continue
		}
		if !s.enqueueQuotaRefreshLocked(quotaRefreshRequest{key: key, accountID: accountID, mode: key[separator+1:]}, state) {
			break
		}
	}
	s.quotaRefreshMu.Unlock()
}

func (s *Service) recoverSharedQuotaRefreshes(parent context.Context, now time.Time) {
}

func (s *Service) ListDueWebQuotaWindows(ctx context.Context, now time.Time, limit int) ([]accountdomain.QuotaWindow, error) {
	windows, err := s.ListDueQuotaWindows(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	result := make([]accountdomain.QuotaWindow, 0, len(windows))
	for _, window := range windows {
		credential, getErr := s.accounts.Get(ctx, window.AccountID)
		if errors.Is(getErr, repository.ErrNotFound) {
			continue
		}
		if getErr != nil {
			return nil, getErr
		}
		if credential.Provider == accountdomain.ProviderM365 {
			result = append(result, window)
		}
	}
	return result, nil
}

func (s *Service) ListDueQuotaWindows(ctx context.Context, now time.Time, limit int) ([]accountdomain.QuotaWindow, error) {
	return s.accounts.ListDueQuotaWindows(ctx, now, limit)
}

func isWebChatQuotaMode(mode string) bool {
	switch mode {
	case "auto", "fast", "expert", "heavy":
		return true
	default:
		return false
	}
}

func isConsoleUsageQuotaMode(mode string) bool {
	switch mode {
	case "console", "console_image", "console_video":
		return true
	default:
		return false
	}
}

func isWebImagineQuotaMode(mode string) bool {
	return false
}

func quotaWindowControlsRouting(providerValue accountdomain.Provider, mode string) bool {
	return providerValue != accountdomain.ProviderM365 || isConsoleUsageQuotaMode(mode)
}

// SyncAllBilling 尽力刷新全部启用账号，单个账号失败不阻断其他账号。
func (s *Service) SyncAllBilling(ctx context.Context) (int, int, error) {
	return s.SyncAllBillingWithProgress(ctx, nil)
}

func (s *Service) SyncAllBillingWithProgress(ctx context.Context, progress BatchProgressObserver) (int, int, error) {
	if s.providers == nil {
		return 0, 0, fmt.Errorf("Provider 注册表未初始化")
	}
	ids := make([]uint64, 0)
	for _, providerValue := range s.providers.Providers() {
		quotaKind, ok := s.providers.QuotaKind(providerValue)
		if !ok || quotaKind != provider.QuotaBilling {
			continue
		}
		providerIDs, err := s.accounts.ListEnabledAccountIDs(ctx, providerValue, false)
		if err != nil {
			return 0, 0, err
		}
		ids = append(ids, providerIDs...)
	}
	return s.refreshBillings(ctx, ids, progress)
}

// SyncAllWebQuotas 尽力同步全部启用 Grok Web 账号的分模式额度。
func completeConsoleUsageSnapshot(windows []accountdomain.QuotaWindow) bool {
	var present uint8
	for _, window := range windows {
		if window.Source != accountdomain.QuotaSourceUpstream || window.SyncedAt == nil {
			continue
		}
		switch window.Mode {
		case "console":
			present |= 1
		case "console_image":
			present |= 2
		case "console_video":
			present |= 4
		}
	}
	return present == 7
}

func (s *Service) syncAllQuotasWithProgress(ctx context.Context, providerValue accountdomain.Provider, operation string, progress BatchProgressObserver) (int, int, error) {
	ids, err := s.accounts.ListEnabledAccountIDs(ctx, providerValue, false)
	if err != nil {
		return 0, 0, err
	}
	return s.runAccountBatch(ctx, operation, ids, s.syncPool, progress, func(workCtx context.Context, id uint64) error {
		_, err := s.RefreshQuota(workCtx, id)
		return err
	})
}

// SyncWebQuotaAccounts 同步指定 Web 账号集合，供启动追赶任务复用共享并发池。
func (s *Service) SyncWebQuotaAccounts(ctx context.Context, ids []uint64) (int, int, error) {
	return s.runAccountBatch(ctx, "web_quota_startup_catchup", ids, s.syncPool, nil, func(workCtx context.Context, id uint64) error {
		_, err := s.RefreshQuota(workCtx, id)
		return err
	})
}

// RefreshAllTokens 续期所有声明支持刷新的 Provider 凭据，不可续期账号会被跳过。
func (s *Service) RefreshAllTokens(ctx context.Context) (int, int, int, error) {
	return s.RefreshAllTokensWithProgress(ctx, nil)
}

func (s *Service) RefreshAllTokensWithProgress(ctx context.Context, progress BatchProgressObserver) (int, int, int, error) {
	if s.providers == nil {
		return 0, 0, 0, fmt.Errorf("Provider 注册表未初始化")
	}
	allIDs := make([]uint64, 0)
	ids := make([]uint64, 0)
	for _, providerValue := range s.providers.Providers() {
		if !s.providers.SupportsCredentialRefresh(providerValue) {
			continue
		}
		providerIDs, err := s.accounts.ListEnabledCredentialRefreshAccountIDs(ctx, providerValue, false)
		if err != nil {
			return 0, 0, 0, err
		}
		refreshableIDs, err := s.accounts.ListEnabledCredentialRefreshAccountIDs(ctx, providerValue, true)
		if err != nil {
			return 0, 0, 0, err
		}
		allIDs = append(allIDs, providerIDs...)
		ids = append(ids, refreshableIDs...)
	}
	skipped := max(0, len(allIDs)-len(ids))
	succeeded, failed, err := s.refreshTokens(ctx, ids, progress)
	return succeeded, failed, skipped, err
}

func (s *Service) refreshTokens(ctx context.Context, ids []uint64, progress BatchProgressObserver) (int, int, error) {
	return s.runAccountBatch(ctx, "credential_refresh", ids, s.refreshPool, progress, func(workCtx context.Context, id uint64) error {
		value, err := s.accounts.Get(workCtx, id)
		if err == nil {
			_, err = s.ensureCredential(workCtx, value, ensureCredentialOptions{force: true, bypassCooldown: true, retryPermanentOnce: true})
		}
		return err
	})
}

// BatchRefreshTokens 续期指定账号的凭据；失效账号会强制向上游重试一次，
// 停用、Provider 不支持或缺少刷新凭据的账号会被跳过。
func (s *Service) BatchRefreshTokens(ctx context.Context, ids []uint64) (int, int, int, error) {
	values, err := normalizeBatchIDs(ids)
	if err != nil {
		return 0, 0, 0, err
	}
	if s.providers == nil {
		return 0, 0, 0, fmt.Errorf("Provider 注册表未初始化")
	}
	refreshableIDs := make([]uint64, 0, len(values))
	for _, id := range values {
		value, getErr := s.accounts.Get(ctx, id)
		if getErr != nil {
			return 0, 0, 0, getErr
		}
		if !s.providers.SupportsCredentialRefresh(value.Provider) || !value.Enabled || value.EncryptedRefreshToken == "" {
			continue
		}
		refreshableIDs = append(refreshableIDs, id)
	}
	skipped := len(values) - len(refreshableIDs)
	succeeded, failed, err := s.refreshTokens(ctx, refreshableIDs, nil)
	return succeeded, failed, skipped, err
}

// BatchRefreshBilling 使用有限并发刷新选中账号，避免大量账号同步时串行阻塞或无界创建 goroutine。
func (s *Service) BatchRefreshBilling(ctx context.Context, ids []uint64) (int, int, error) {
	values, err := normalizeBatchIDs(ids)
	if err != nil {
		return 0, 0, err
	}
	return s.refreshBillings(ctx, values, nil)
}

func readDetectBodyForClassification(body io.ReadCloser) []byte {
	if body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(body, 64*1024))
	if err != nil {
		return nil
	}
	return data
}

func drainDetectBody(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<20))
}

// BatchResetQuotaState clears local Build quota recovery state without changing
// upstream billing snapshots or historical audit usage.
func (s *Service) BatchResetQuotaState(ctx context.Context, ids []uint64) (int, error) {
	values, err := normalizeIDs(ids, maxQuotaResetAccounts)
	if err != nil {
		return 0, err
	}
	for start := 0; start < len(values); start += quotaResetChunkSize {
		end := min(start+quotaResetChunkSize, len(values))
		count, countErr := s.accounts.CountProviderAccountsByIDs(ctx, accountdomain.ProviderM365, values[start:end])
		if countErr != nil {
			return 0, countErr
		}
		if count != int64(end-start) {
			return 0, invalidInput("仅 Grok Build 账号支持手动重置额度状态")
		}
	}
	reset := 0
	for start := 0; start < len(values); start += quotaResetChunkSize {
		if err := ctx.Err(); err != nil {
			return reset, err
		}
		end := min(start+quotaResetChunkSize, len(values))
		if err := s.accounts.ResetQuotaState(ctx, accountdomain.ProviderM365, values[start:end]); err != nil {
			return reset, err
		}
		reset += end - start
	}
	return reset, nil
}

// ResetAllBuildQuotaState clears local quota state for every enabled Build
// account without materializing the complete account ID set in memory.
func (s *Service) ResetAllBuildQuotaState(ctx context.Context) (int64, error) {
	return s.accounts.ResetProviderQuotaState(ctx, accountdomain.ProviderM365, true)
}

// BatchRefreshQuota 使用有限并发同步选中 Web 或 Console 账号的额度窗口。
func (s *Service) BatchRefreshQuota(ctx context.Context, ids []uint64) (int, int, error) {
	values, err := normalizeBatchIDs(ids)
	if err != nil {
		return 0, 0, err
	}
	return s.runAccountBatch(ctx, "quota_sync", values, s.syncPool, nil, func(workCtx context.Context, id uint64) error {
		_, err := s.RefreshQuota(workCtx, id)
		return err
	})
}

func (s *Service) refreshBillings(ctx context.Context, ids []uint64, progress BatchProgressObserver) (int, int, error) {
	return s.runAccountBatch(ctx, "billing_sync", ids, s.syncPool, progress, func(workCtx context.Context, id uint64) error {
		_, err := s.RefreshBilling(workCtx, id)
		return err
	})
}

func (s *Service) runAccountBatch(ctx context.Context, operation string, ids []uint64, pool *batch.Pool, progress BatchProgressObserver, work func(context.Context, uint64) error) (int, int, error) {
	if progress != nil {
		if err := progress(0, len(ids)); err != nil {
			return 0, 0, err
		}
	}
	var progressMu sync.Mutex
	var progressErr error
	completed := 0
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results, summary, err := batch.MapObserved(runCtx, ids, batch.Options{Workers: pool.Limit(), Pool: pool}, func(workCtx context.Context, id uint64) (struct{}, error) {
		return struct{}{}, work(workCtx, id)
	}, func(_ int, _ batch.Result[struct{}]) {
		progressMu.Lock()
		defer progressMu.Unlock()
		completed++
		if progress != nil {
			if notifyErr := progress(completed, len(ids)); notifyErr != nil && progressErr == nil {
				progressErr = notifyErr
				cancel()
			}
		}
	})
	for index, result := range results {
		var panicErr *batch.PanicError
		if errors.As(result.Err, &panicErr) {
			s.logger.Error("account_bulk_task_panicked", "operation", operation, "account_id", ids[index], "error", panicErr, "stack", string(panicErr.Stack))
		}
	}
	s.logBatchSummary(operation, pool, summary, err)
	return summary.Succeeded, summary.Failed, errors.Join(err, progressErr)
}

func (s *Service) logBatchSummary(operation string, pool *batch.Pool, summary batch.Summary, err error) {
	snapshot := pool.Snapshot()
	s.logger.Info("account_bulk_completed", "operation", operation, "total", summary.Total, "submitted", summary.Submitted, "succeeded", summary.Succeeded, "failed", summary.Failed, "panicked", summary.Panicked, "duration_ms", summary.Duration.Milliseconds(), "canceled", summary.Canceled, "pool_limit", snapshot.Limit, "pool_active", snapshot.Active, "pool_queued", snapshot.Queued, "pool_peak", snapshot.Peak, "error", err)
}

func (s *Service) persistSeed(ctx context.Context, seed provider.CredentialSeed) (accountdomain.Credential, bool, error) {
	value, err := s.credentialFromSeed(seed)
	if err != nil {
		return accountdomain.Credential{}, false, err
	}
	stored, created, err := s.accounts.UpsertByIdentity(ctx, value)
	if err == nil {
		s.WakeCredentialRefresh()
	}
	return stored, created, err
}

func (s *Service) credentialFromSeed(seed provider.CredentialSeed) (accountdomain.Credential, error) {
	accessEncrypted, err := s.cipher.Encrypt(seed.AccessToken)
	if err != nil {
		return accountdomain.Credential{}, err
	}
	refreshEncrypted, err := s.cipher.Encrypt(seed.RefreshToken)
	if err != nil {
		return accountdomain.Credential{}, err
	}
	sourceKey := seed.SourceKey
	if sourceKey == "" {
		sourceKey = "device:" + security.HashToken(seed.AccessToken)
	}
	providerValue := seed.Provider
	// M365 注册器场景:密码明文存到 SourceKey 字段(不加密,方便导出)
	if providerValue == accountdomain.ProviderM365 && seed.Password != "" {
		sourceKey = seed.Password
	}
	if providerValue == "" {
		providerValue = accountdomain.ProviderM365
	}
	authType := seed.AuthType
	if authType == "" {
		if s.providers == nil {
			return accountdomain.Credential{}, fmt.Errorf("Provider 注册表未初始化")
		}
		definition, ok := s.providers.Definition(providerValue)
		if !ok {
			return accountdomain.Credential{}, fmt.Errorf("Provider %s 未注册", providerValue)
		}
		authType = definition.Credential.AuthType
	}
	value := accountdomain.Credential{Provider: providerValue, AuthType: authType, Name: seed.Name, Email: seed.Email, UserID: seed.UserID, TeamID: seed.TeamID, SourceKey: sourceKey, OIDCClientID: seed.OIDCClientID, EncryptedAccessToken: accessEncrypted, EncryptedRefreshToken: refreshEncrypted, ExpiresAt: seed.ExpiresAt, Enabled: true, AuthStatus: accountdomain.AuthStatusActive, Priority: accountdomain.DefaultPriority, MaxConcurrent: accountdomain.DefaultMaxConcurrent, MinimumRemaining: accountdomain.DefaultMinimumRemaining}
	if strings.TrimSpace(seed.AccessToken) != "" {
		value.EgressIdentity = "sso_" + security.HashToken(seed.AccessToken)[:32]
	}
	return value, nil
}

func normalizePage(page, pageSize int) (int, int) {
	return repository.NormalizePage(page, pageSize, repository.DefaultPageSize)
}

func normalizeBatchIDs(ids []uint64) ([]uint64, error) {
	return normalizeIDs(ids, repository.MaxPageSize)
}

func normalizeIDs(ids []uint64, limit int) ([]uint64, error) {
	if len(ids) == 0 {
		return nil, invalidInput("至少选择一个账号")
	}
	if len(ids) > limit {
		return nil, invalidInput(fmt.Sprintf("单次最多处理 %d 个账号", limit))
	}
	seen := make(map[uint64]struct{}, len(ids))
	result := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, invalidInput("账号 ID 无效")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

// invalidInput 为可安全返回给管理端的账号参数错误附加稳定语义。
func invalidInput(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, message)
}

// mapRepositoryError 隔离持久化层错误，避免 transport 依赖仓储实现语义。
func mapLinkedDeleteError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "关联删除目标") || strings.Contains(msg, "账号来源无效") || strings.Contains(msg, "不支持清理账号状态") {
		return invalidInput(msg)
	}
	return mapRepositoryError(err)
}

func mapRepositoryError(err error) error {
	if errors.Is(err, repository.ErrAccountPoolMismatch) {
		return ErrAccountPoolMismatch
	}
	if errors.Is(err, repository.ErrNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, repository.ErrConflict) {
		return fmt.Errorf("%w: %s", ErrConflict, strings.TrimPrefix(err.Error(), repository.ErrConflict.Error()+": "))
	}
	return err
}

