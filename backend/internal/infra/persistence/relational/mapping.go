package relational

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"M365Copilot2ApiX/backend/internal/domain/account"
	"M365Copilot2ApiX/backend/internal/domain/admin"
	"M365Copilot2ApiX/backend/internal/domain/audit"
	"M365Copilot2ApiX/backend/internal/domain/clientkey"
	"M365Copilot2ApiX/backend/internal/domain/model"
)

func toAdminDomain(value adminModel) admin.Admin {
	return admin.Admin{ID: value.ID, Username: value.Username, PasswordHash: value.PasswordHash, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func toSessionDomain(value adminSessionModel) admin.Session {
	return admin.Session{ID: value.ID, AdminID: value.AdminID, RefreshTokenHash: value.RefreshTokenHash, ExpiresAt: value.ExpiresAt, LastUsedAt: value.LastUsedAt, CreatedAt: value.CreatedAt}
}

func toAccountDomain(value accountModel) account.Credential {
	var expiresAt time.Time
	var refreshDueAt, lastRefreshAt *time.Time
	var refreshFailures int
	var refreshUnclassifiedAuthFailures int
	var lastRefreshErrorStatus int
	var lastRefreshError string
	var lastRefreshErrorMessage string
	var lastRefreshErrorResponse string
	var refreshPermanent bool
	var authType account.AuthType
	var clientID, encryptedPrimary, encryptedRefresh string
	if value.Credential != nil {
		authType = account.AuthType(value.Credential.AuthType)
		clientID = value.Credential.ClientID
		encryptedPrimary = value.Credential.EncryptedPrimary
		encryptedRefresh = value.Credential.EncryptedRefresh
		if value.Credential.ExpiresAt != nil {
			expiresAt = *value.Credential.ExpiresAt
		}
		refreshDueAt = value.Credential.RefreshDueAt
		lastRefreshAt = value.Credential.LastRefreshAt
		refreshFailures = value.Credential.RefreshFailures
		refreshUnclassifiedAuthFailures = value.Credential.RefreshUnclassifiedAuthFailures
		lastRefreshErrorStatus = value.Credential.LastRefreshErrorStatus
		lastRefreshError = value.Credential.LastRefreshError
		lastRefreshErrorMessage = value.Credential.LastRefreshErrorMessage
		lastRefreshErrorResponse = value.Credential.LastRefreshErrorResponse
		refreshPermanent = value.Credential.RefreshPermanent
	}
	_ = encryptedPrimary
	_ = encryptedRefresh
	return account.Credential{
		ID: value.ID, Provider: account.Provider(value.Provider), AuthType: authType, Name: value.Name, Email: value.Email,
		UserID: value.UserID, TeamID: value.TeamID, SourceKey: value.SourceKey, OIDCClientID: clientID,
		ExpiresAt: expiresAt, RefreshDueAt: refreshDueAt, LastRefreshAt: lastRefreshAt,
		RefreshFailureCount: refreshFailures, RefreshUnclassifiedAuthCount: refreshUnclassifiedAuthFailures, LastRefreshErrorStatus: lastRefreshErrorStatus, LastRefreshErrorCode: lastRefreshError, LastRefreshErrorMessage: lastRefreshErrorMessage, LastRefreshErrorResponse: lastRefreshErrorResponse, RefreshPermanent: refreshPermanent,
		Enabled: value.Enabled, AuthStatus: account.AuthStatus(value.AuthStatus), ReauthMarkedAt: value.ReauthMarkedAt, Priority: value.Priority,
		MaxConcurrent: value.MaxConcurrent, MinimumRemaining: value.MinimumRemaining, FailureCount: value.FailureCount,
		CooldownUntil: value.CooldownUntil, LastError: value.LastError, LastUsedAt: value.LastUsedAt,
		EgressNodeID: valueEgressNodeID(value.EgressNodeID), EgressAssignmentMode: account.EgressAssignmentMode(value.EgressAssignmentMode), EgressAssignedAt: value.EgressAssignedAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func toCredentialMaterialDomain(value accountCredentialModel, provider account.Provider) account.CredentialMaterial {
	var expiresAt time.Time
	if value.ExpiresAt != nil {
		expiresAt = *value.ExpiresAt
	}
	return account.CredentialMaterial{
		AccountID: value.AccountID, Provider: provider, AuthType: account.AuthType(value.AuthType), OIDCClientID: value.ClientID,
		EncryptedAccessToken: value.EncryptedPrimary, EncryptedRefreshToken: value.EncryptedRefresh,
		ExpiresAt: expiresAt,
		RefreshDueAt: value.RefreshDueAt, LastRefreshAt: value.LastRefreshAt,
		RefreshFailureCount: value.RefreshFailures, RefreshUnclassifiedAuthCount: value.RefreshUnclassifiedAuthFailures, LastRefreshErrorStatus: value.LastRefreshErrorStatus, LastRefreshErrorCode: value.LastRefreshError, LastRefreshErrorMessage: value.LastRefreshErrorMessage, LastRefreshErrorResponse: value.LastRefreshErrorResponse,
		RefreshPermanent: value.RefreshPermanent, UpdatedAt: value.UpdatedAt,
	}
}

func fromAccountDomain(value account.Credential) accountModel {
	return accountModel{
		ID: value.ID, IdentityKey: accountIdentity(value), Provider: string(value.Provider), Name: value.Name, Email: value.Email,
		UserID: value.UserID, TeamID: value.TeamID, SourceKey: value.SourceKey,
		Enabled: value.Enabled, AuthStatus: string(value.AuthStatus), ReauthMarkedAt: value.ReauthMarkedAt, Priority: value.Priority,
		MaxConcurrent: value.MaxConcurrent, MinimumRemaining: value.MinimumRemaining, FailureCount: value.FailureCount,
		CooldownUntil: value.CooldownUntil, LastError: value.LastError, LastUsedAt: value.LastUsedAt,
		ObservedModel: value.ObservedModel, ObservedModelAt: value.ObservedModelAt,
		EgressNodeID: egressNodeID(value.EgressNodeID), EgressAssignmentMode: string(value.EgressAssignmentMode), EgressAssignedAt: value.EgressAssignedAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func valueEgressNodeID(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}

func egressNodeID(value uint64) *uint64 {
	if value == 0 {
		return nil
	}
	result := value
	return &result
}

func fromAccountCredentialDomain(value account.Credential) accountCredentialModel {
	var expiresAt *time.Time
	if !value.ExpiresAt.IsZero() {
		copy := value.ExpiresAt
		expiresAt = &copy
	}
	refreshDueAt := value.RefreshDueAt
	if refreshDueAt == nil && value.EncryptedRefreshToken != "" && !value.ExpiresAt.IsZero() {
		due := account.CredentialRefreshDueAt(value.ID, value.ExpiresAt)
		refreshDueAt = &due
	}
	authType := value.AuthType
	if authType == "" {
		authType = account.AuthTypeOAuth
	}
	return accountCredentialModel{
		AccountID: value.ID, AuthType: string(authType), ClientID: value.OIDCClientID,
		EncryptedPrimary: value.EncryptedAccessToken, EncryptedRefresh: value.EncryptedRefreshToken,
		ExpiresAt:        expiresAt, RefreshDueAt: refreshDueAt, LastRefreshAt: value.LastRefreshAt,
		RefreshFailures: value.RefreshFailureCount, RefreshUnclassifiedAuthFailures: value.RefreshUnclassifiedAuthCount, LastRefreshErrorStatus: value.LastRefreshErrorStatus, LastRefreshError: value.LastRefreshErrorCode, LastRefreshErrorMessage: value.LastRefreshErrorMessage, LastRefreshErrorResponse: value.LastRefreshErrorResponse, RefreshPermanent: value.RefreshPermanent,
		UpdatedAt: time.Now().UTC(),
	}
}

func fromWebProfileDomain(value account.Credential) *webAccountProfileModel {
	return nil
}

func accountIdentity(value account.Credential) string {
	provider := string(value.Provider)
	var identity string
	switch {
	case strings.TrimSpace(value.UserID) != "":
		identity = strings.Join([]string{provider, "user", strings.TrimSpace(value.UserID), strings.TrimSpace(value.TeamID)}, "|")
	case strings.TrimSpace(value.Email) != "":
		identity = strings.Join([]string{provider, "email", strings.ToLower(strings.TrimSpace(value.Email)), strings.TrimSpace(value.TeamID)}, "|")
	default:
		identity = strings.Join([]string{provider, "source", strings.TrimSpace(value.SourceKey)}, "|")
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func toBillingDomain(value billingModel) account.Billing {
	var history []account.BillingHistoryEntry
	_ = json.Unmarshal([]byte(value.HistoryJSON), &history)
	result := toRoutingBillingDomain(value)
	result.History = history
	return result
}

func toRoutingBillingDomain(value billingModel) account.Billing {
	return account.Billing{AccountID: value.AccountID, PlanCode: value.PlanCode, PlanName: value.PlanName, MonthlyLimit: value.MonthlyLimit, Used: value.Used, OnDemandCap: value.OnDemandCap, OnDemandUsed: value.OnDemandUsed, PrepaidBalance: value.PrepaidBalance, CreditUsagePercent: value.CreditUsagePercent, IsUnifiedBillingUser: value.IsUnifiedBillingUser, OnDemandEnabled: value.OnDemandEnabled, TopUpMethod: value.TopUpMethod, UsagePeriodType: value.UsagePeriodType, UsagePeriodStart: value.UsagePeriodStart, UsagePeriodEnd: value.UsagePeriodEnd, BillingPeriodStart: value.BillingPeriodStart, BillingPeriodEnd: value.BillingPeriodEnd, SyncedAt: value.SyncedAt}
}

func toModelDomain(value modelRouteModel) model.Route {
	return model.Route{ID: value.ID, PublicID: value.PublicID, Provider: account.Provider(value.Provider), UpstreamModel: value.UpstreamModel, Capability: model.Capability(value.Capability), Origin: model.Origin(value.Origin), Enabled: value.Enabled, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func toClientKeyDomain(value clientKeyModel, allowedModels []uint64) clientkey.Key {
	providerScope, _ := clientkey.NormalizeProviderScope(clientkey.ProviderScope(value.ProviderScopeMask))
	tierScope, _ := clientkey.NormalizeTierScope(clientkey.TierScope(value.TierScopeMask))
	internalKind := ""
	if value.InternalKind != nil {
		internalKind = *value.InternalKind
	}
	return clientkey.Key{
		ID: value.ID, Name: value.Name, Prefix: value.Prefix, SecretHash: value.SecretHash, EncryptedSecret: value.EncryptedSecret,
		InternalKind: internalKind,
		Enabled:      value.Enabled, ExpiresAt: value.ExpiresAt, RPMLimit: value.RPMLimit, MaxConcurrent: value.MaxConcurrent,
		BillingLimitUSDTicks: value.BillingLimitUSDTicks, BilledUsageUSDTicks: value.BilledUsageUSDTicks, ReservedUsageUSDTicks: value.ReservedUsageUSDTicks,
		AllowModelAliases: value.AllowModelAliases, AllowedModels: allowedModels,
		ProviderScope: providerScope, TierScope: tierScope,
		LastUsedAt: value.LastUsedAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func toAuditDomain(value requestAuditModel) audit.Record {
	var requestHeaders map[string][]string
	if strings.TrimSpace(value.RequestHeadersJSON) != "" && value.RequestHeadersJSON != "{}" {
		_ = json.Unmarshal([]byte(value.RequestHeadersJSON), &requestHeaders)
	}
	return audit.Record{
		ID: value.ID, EventID: value.EventID, RequestID: value.RequestID, ClientKeyID: value.ClientKeyID, ClientKeyName: value.ClientKeyName, ClientIP: value.ClientIP,
		ModelRouteID: value.ModelRouteID, ModelPublicID: value.ModelPublicID, ModelUpstreamModel: value.ModelUpstreamModel,
		Provider: value.Provider, Operation: audit.Operation(value.Operation), UsageSource: audit.UsageSource(value.UsageSource),
		ReasoningEffort: value.ReasoningEffort,
		AccountID:       value.AccountID, AccountName: value.AccountName,
		EgressNodeID: value.EgressNodeID, EgressNodeName: value.EgressNodeName, EgressScope: value.EgressScope, EgressMode: audit.EgressMode(value.EgressMode),
		StatusCode: value.StatusCode, Streaming: value.Streaming,
		MediaInputImages: value.MediaInputImages, MediaOutputImages: value.MediaOutputImages, MediaOutputSeconds: value.MediaOutputSeconds,
		InputTokens: value.InputTokens, CachedInputTokens: value.CachedInputTokens, OutputTokens: value.OutputTokens,
		ReasoningTokens: value.ReasoningTokens, TotalTokens: value.TotalTokens, CostInUSDTicks: value.CostInUSDTicks,
		EstimatedCostInUSDTicks: value.EstimatedCostInUSDTicks, PricingModel: value.PricingModel, PricingVersion: value.PricingVersion,
		NumSourcesUsed: value.NumSourcesUsed, NumServerSideToolsUsed: value.NumServerSideToolsUsed,
		ContextInputTokens: value.ContextInputTokens, ContextOutputTokens: value.ContextOutputTokens, FirstTokenMS: value.FirstTokenMS, DurationMS: value.DurationMS,
		ErrorCode: value.ErrorCode, RequestMethod: value.RequestMethod, RequestPath: value.RequestPath, RequestHeaders: requestHeaders, AttemptCount: value.AttemptCount, CreatedAt: value.CreatedAt,
	}
}

func toAuditAttemptDomain(value requestAuditAttemptModel) (audit.Attempt, error) {
	var responseHeaders map[string][]string
	if err := json.Unmarshal([]byte(value.ResponseHeadersJSON), &responseHeaders); err != nil {
		return audit.Attempt{}, err
	}
	var errorChain []audit.ErrorFrame
	if err := json.Unmarshal([]byte(value.ErrorChainJSON), &errorChain); err != nil {
		return audit.Attempt{}, err
	}
	return audit.Attempt{
		ID:                    value.ID,
		AuditID:               value.AuditID,
		Number:                value.Number,
		Source:                audit.AttemptSource(value.Source),
		Stage:                 value.Stage,
		AccountID:             value.AccountID,
		AccountName:           value.AccountName,
		Method:                value.Method,
		RequestPath:           value.RequestPath,
		UpstreamURL:           value.UpstreamURL,
		StartedAt:             value.StartedAt,
		DurationMS:            value.DurationMS,
		UpstreamStatusCode:    value.UpstreamStatusCode,
		UpstreamStatus:        value.UpstreamStatus,
		ResponseHeaders:       responseHeaders,
		ResponseBody:          value.ResponseBody,
		ResponseBodyTruncated: value.ResponseBodyTruncated,
		TransportError:        value.TransportError,
		ErrorChain:            errorChain,
	}, nil
}
