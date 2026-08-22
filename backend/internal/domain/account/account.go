package account

import (
	"crypto/sha256"
	"encoding/binary"
	"time"
)

// Provider 表示上游能力来源。
type Provider string

const (
	ProviderM365 Provider = "m365_copilot"
)

var providers = [...]Provider{ProviderM365}

// Providers 返回按产品展示和后台维护顺序排列的稳定 Provider 集合。
func Providers() []Provider {
	return append([]Provider(nil), providers[:]...)
}

// IsValid 判断 Provider 是否属于当前系统固定支持的渠道。
func (p Provider) IsValid() bool {
	switch p {
	case ProviderM365:
		return true
	default:
		return false
	}
}

// ModelNamespace 返回内部模型路由使用的稳定渠道命名空间。
func (p Provider) ModelNamespace() string {
	switch p {
	case ProviderM365:
		return "M365"
	default:
		return ""
	}
}

type AuthType string

const (
	AuthTypeOAuth AuthType = "oauth"
)

const (
	DefaultPriority          = 1
	DefaultMaxConcurrent     = 8
	DefaultMinimumRemaining = 0
	MaxConcurrent            = 256
)

// AuthStatus 表示账号凭据的认证状态。
type AuthStatus string

const (
	AuthStatusActive         AuthStatus = "active"
	AuthStatusReauthRequired AuthStatus = "reauthRequired"
)

// LastErrorMissingThinking values are durable, non-sensitive quality strike
// markers. Unlike arbitrary upstream errors, they may be propagated through
// runtime invalidation events so every gateway instance preserves the strike.
const (
	LastErrorMissingThinking         = "missing_thinking"
	LastErrorMissingThinkingDisabled = "missing_thinking_disabled"
)

// NormalizeHealthMarker admits only durable non-sensitive health markers.
func NormalizeHealthMarker(value string) string {
	switch value {
	case LastErrorMissingThinking, LastErrorMissingThinkingDisabled:
		return value
	default:
		return ""
	}
}

// EgressAssignmentMode 表示账号出口节点的维护方式。手工绑定绝不会被
// 自动均衡任务迁移，自动绑定才允许在健康或容量变化时重新分配。
type EgressAssignmentMode string

const (
	EgressAssignmentManual EgressAssignmentMode = "manual"
	EgressAssignmentAuto   EgressAssignmentMode = "auto"
)

func (value EgressAssignmentMode) IsValid() bool {
	return value == EgressAssignmentManual || value == EgressAssignmentAuto
}

// Credential 表示持久化的上游 OAuth 账号。
type Credential struct {
	ID                        uint64
	Provider                  Provider
	AuthType                  AuthType
	Name                      string
	Email                     string
	UserID                    string
	TeamID                    string
	SourceKey                 string
	OIDCClientID              string
	EncryptedAccessToken      string
	EncryptedRefreshToken     string
	ExpiresAt                 time.Time
	RefreshDueAt              *time.Time
	LastRefreshAt             *time.Time
	RefreshFailureCount       int
	RefreshUnclassifiedAuthCount int
	LastRefreshErrorStatus       int
	LastRefreshErrorCode         string
	LastRefreshErrorMessage      string
	LastRefreshErrorResponse     string
	RefreshPermanent             bool
	Enabled                      bool
	AuthStatus                   AuthStatus
	// ReauthMarkedAt 仅在切入 reauthRequired 时写入；恢复 active 时清空。自动清理以该时刻为 minAge 锚点。
	ReauthMarkedAt   *time.Time
	Priority         int
	MaxConcurrent    int
	MinimumRemaining float64
	FailureCount     int
	CooldownUntil    *time.Time
	LastError        string
	LastUsedAt       *time.Time
	ObservedModel    string
	ObservedModelAt  *time.Time
	// EgressIdentity 是不含凭据和个人信息的稳定出口身份。
	EgressIdentity string
	// EgressNodeID 是账号显式绑定的出口节点。0 表示沿用当前 scope 的
	// 池选择逻辑；非零值必须优先使用该节点，不能悄悄回退到其他代理。
	EgressNodeID         uint64
	EgressAssignmentMode EgressAssignmentMode
	EgressAssignedAt     *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// CredentialMaterial contains the encrypted provider secrets and refresh
// metadata loaded only after routing selects an account.
type CredentialMaterial struct {
	AccountID                    uint64
	Provider                     Provider
	AuthType                     AuthType
	OIDCClientID                 string
	EncryptedAccessToken         string
	EncryptedRefreshToken        string
	ExpiresAt                    time.Time
	RefreshDueAt                 *time.Time
	LastRefreshAt                *time.Time
	RefreshFailureCount          int
	RefreshUnclassifiedAuthCount int
	LastRefreshErrorStatus       int
	LastRefreshErrorCode         string
	LastRefreshErrorMessage      string
	LastRefreshErrorResponse     string
	RefreshPermanent             bool
	UpdatedAt                    time.Time
}

// ApplyTo merges credential material into the matching routing account.
// A mismatch leaves the value unchanged so callers cannot attach one
// account's secrets to another account.
func (m CredentialMaterial) ApplyTo(value Credential) (Credential, bool) {
	if m.AccountID == 0 || value.ID != m.AccountID || m.Provider == "" || value.Provider != m.Provider {
		return value, false
	}
	value.AuthType = m.AuthType
	value.OIDCClientID = m.OIDCClientID
	value.EncryptedAccessToken = m.EncryptedAccessToken
	value.EncryptedRefreshToken = m.EncryptedRefreshToken
	value.ExpiresAt = m.ExpiresAt
	value.RefreshDueAt = m.RefreshDueAt
	value.LastRefreshAt = m.LastRefreshAt
	value.RefreshFailureCount = m.RefreshFailureCount
	value.RefreshUnclassifiedAuthCount = m.RefreshUnclassifiedAuthCount
	value.LastRefreshErrorStatus = m.LastRefreshErrorStatus
	value.LastRefreshErrorCode = m.LastRefreshErrorCode
	value.LastRefreshErrorMessage = m.LastRefreshErrorMessage
	value.LastRefreshErrorResponse = m.LastRefreshErrorResponse
	value.RefreshPermanent = m.RefreshPermanent
	return value, true
}

// CredentialRefreshDueAt 将账号稳定地分散到到期前 5~8 分钟，避免同批导入账号同时刷新。
func CredentialRefreshDueAt(accountID uint64, expiresAt time.Time) time.Time {
	if expiresAt.IsZero() {
		return time.Time{}
	}
	var identity [8]byte
	binary.BigEndian.PutUint64(identity[:], accountID)
	digest := sha256.Sum256(identity[:])
	jitterSeconds := binary.BigEndian.Uint16(digest[:2]) % 181
	return expiresAt.UTC().Add(-5*time.Minute - time.Duration(jitterSeconds)*time.Second)
}

type QuotaSource string

const (
	QuotaSourceDefault   QuotaSource = "default"
	QuotaSourceEstimated QuotaSource = "estimated"
	QuotaSourceUpstream  QuotaSource = "upstream"
)

// QuotaWindow 表示 Provider 单个模式的额度窗口。
type QuotaWindow struct {
	AccountID     uint64
	Mode          string
	Remaining     int
	Total         int
	UsagePercent  float64
	Breakdown     []QuotaBreakdown
	WindowSeconds int
	ResetAt       *time.Time
	SyncedAt      *time.Time
	Source        QuotaSource
	UpdatedAt     time.Time
}

// QuotaBreakdown 保存上游周额度中的产品枚举及其使用百分比。
type QuotaBreakdown struct {
	ProductCode  int
	UsagePercent float64
}

type BillingHistoryEntry struct {
	Year         int
	Month        int
	PeriodType   string
	PeriodStart  string
	PeriodEnd    string
	IncludedUsed float64
	OnDemandUsed float64
	TotalUsed    float64
}

// Billing 表示账号最近一次额度快照。
type Billing struct {
	AccountID            uint64
	PlanCode             string
	PlanName             string
	MonthlyLimit         float64
	Used                 float64
	OnDemandCap          float64
	OnDemandUsed         float64
	PrepaidBalance       float64
	CreditUsagePercent   float64
	IsUnifiedBillingUser bool
	OnDemandEnabled      *bool
	TopUpMethod          string
	UsagePeriodType      string
	UsagePeriodStart     string
	UsagePeriodEnd       string
	BillingPeriodStart   string
	BillingPeriodEnd     string
	History              []BillingHistoryEntry
	SyncedAt             time.Time
}

// PeriodEnd 返回上游账期结束时间，无法解析时返回 false。
func (b Billing) PeriodEnd() (time.Time, bool) {
	if b.CreditUsagePercent >= 100 {
		if value, ok := parseBillingTime(b.UsagePeriodEnd); ok {
			return value, true
		}
	}
	return parseBillingTime(b.BillingPeriodEnd)
}

func parseBillingTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return value.UTC(), true
}

// Remaining 返回当前月剩余额度。
func (b Billing) Remaining() float64 {
	remaining := b.MonthlyLimit - b.Used
	if remaining < 0 {
		return 0
	}
	return remaining
}

// IsExhausted 判断额度快照是否已达到账号保留阈值。
func (b Billing) IsExhausted(minimum float64) bool {
	if b.MonthlyLimit > 0 && b.Remaining() <= minimum {
		return true
	}
	return b.CreditUsagePercent >= 100 && (b.OnDemandCap > 0 || b.UsagePeriodType != "")
}

// RoutingCandidate 聚合账号选择热路径所需的持久化快照。
type RoutingCandidate struct {
	Credential           Credential
	Billing              *Billing
	QuotaWindow          *QuotaWindow
	EgressLeaseBlock     *EgressLeaseBlock
	ModelQuotaBlock      *ModelQuotaBlock
	ModelCapabilityKnown bool
	SupportsModel        bool
}

// RoutingAccountBase contains provider-level routing state reusable across
// models. Credential material is hydrated only after an account is selected.
type RoutingAccountBase struct {
	Credential       Credential
	Billing          *Billing
	QuotaWindow      *QuotaWindow
	EgressLeaseBlock *EgressLeaseBlock
}

// RoutingAccountOverlay contains model-specific eligibility state.
type RoutingAccountOverlay struct {
	AccountID            uint64
	Bound                bool
	ModelCapabilityKnown bool
	SupportsModel        bool
	ModelQuotaBlock      *ModelQuotaBlock
}

type RoutingOverlaySnapshot struct {
	HasBindings bool
	Values      []RoutingAccountOverlay
}

// ModelQuotaBlock 表示账号的单模型配额暂不可用，不影响该账号上的其他模型。
type ModelQuotaBlock struct {
	AccountID     uint64
	UpstreamModel string
	Reason        string
	CooldownUntil time.Time
	UpdatedAt     time.Time
}

// EgressLeaseBlock temporarily removes one account-bound proxy lease from
// routing without changing the account's health or disabling the physical
// egress node shared by other leases.
type EgressLeaseBlock struct {
	AccountID     uint64
	NodeID        uint64
	Reason        string
	Version       string
	CooldownUntil time.Time
	UpdatedAt     time.Time
}

// EgressLeaseBlockCursor is the stable keyset position used to scan durable
// lease state while rows may be renewed or removed concurrently.
type EgressLeaseBlockCursor struct {
	CooldownUntil time.Time
	AccountID     uint64
	NodeID        uint64
}

// DeviceSession 表示一次短期 Device OAuth 授权流程。
type DeviceSession struct {
	ID                      string
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Interval                time.Duration
	NextPollAt              time.Time
	ExpiresAt               time.Time
}
