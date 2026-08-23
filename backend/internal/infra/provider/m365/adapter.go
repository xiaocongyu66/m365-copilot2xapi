package m365

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"M365Copilot2ApiX/backend/internal/domain/account"
	"M365Copilot2ApiX/backend/internal/infra/config"
	"M365Copilot2ApiX/backend/internal/infra/provider"
	"M365Copilot2ApiX/backend/internal/infra/security"
)

// Adapter is the M365 Copilot Provider adapter. It implements all required
// provider interfaces: ResponseAdapter, ModelCatalogAdapter, DefinitionAdapter,
// CredentialRefreshAdapter, DeviceOAuthAdapter, CredentialCodecAdapter, and
// ModelAliasAdapter.
type Adapter struct {
	cfg    config.M365ProviderConfig
	cipher *security.Cipher
	client *Client
	mu     sync.RWMutex
}

// NewAdapter creates an M365 Adapter from the provider configuration and
// credential cipher. It seeds the package-level OAuthConfig from the grok2api
// configuration layer so that all token/device/cache helpers use the correct
// client ID, authority, and scope.
func NewAdapter(cfg config.M365ProviderConfig, cipher *security.Cipher) *Adapter {
	SetOAuthConfig(FromM365ProviderConfig(cfg))
	return &Adapter{
		cfg:    cfg,
		cipher: cipher,
		client: NewClient(),
	}
}

// Provider returns the M365 Copilot Provider identity.
func (a *Adapter) Provider() account.Provider { return account.ProviderM365 }

// chatTimeout returns the configured ChatHub conversation timeout, defaulting
// to 5 minutes when unset.
func (a *Adapter) chatTimeout() time.Duration {
	if a.cfg.ChatTimeout > 0 {
		return time.Duration(a.cfg.ChatTimeout)
	}
	return 5 * time.Minute
}

// decryptAccessToken extracts the plaintext access token from a Credential.
// If the credential has no encrypted token, it tries the refresh token flow.
func (a *Adapter) decryptAccessToken(cred account.Credential) (string, string, error) {
	if a.cipher == nil {
		return "", "", fmt.Errorf("credential cipher is not configured")
	}
	accessToken, err := a.cipher.Decrypt(cred.EncryptedAccessToken)
	if err != nil {
		return "", "", fmt.Errorf("decrypt access token: %w", err)
	}
	// EncryptedRefreshToken 可能含 "\x00" 分隔的加密密码(注册器场景)
	// 只取 refresh token 部分,密码由 ROPC fallback 时单独提取
	encRefresh := cred.EncryptedRefreshToken
	if idx := strings.IndexByte(encRefresh, '\x00'); idx >= 0 {
		encRefresh = encRefresh[:idx]
	}
	refreshToken, err := a.cipher.Decrypt(encRefresh)
	if err != nil {
		return "", "", fmt.Errorf("decrypt refresh token: %w", err)
	}
	return accessToken, refreshToken, nil
}

// decryptPassword 从 EncryptedRefreshToken 的 "\x00" 分隔符后提取加密密码并解密。
// 注册器场景:credentialFromSeed 把加密密码追加到 EncryptedRefreshToken。
func (a *Adapter) decryptPassword(cred account.Credential) string {
	if a.cipher == nil {
		return ""
	}
	idx := strings.IndexByte(cred.EncryptedRefreshToken, '\x00')
	if idx < 0 {
		return ""
	}
	encPassword := cred.EncryptedRefreshToken[idx+1:]
	if encPassword == "" {
		return ""
	}
	password, err := a.cipher.Decrypt(encPassword)
	if err != nil {
		return ""
	}
	return password
}

// resolveAccount extracts the access token, OID, and TID from a Credential.
// If OID/TID are missing from the credential metadata, it decodes them from
// the JWT access token claims.
func (a *Adapter) resolveAccount(cred account.Credential) (Account, error) {
	accessToken, refreshToken, err := a.decryptAccessToken(cred)
	if err != nil {
		return Account{}, err
	}
	if accessToken == "" {
		if refreshToken == "" {
			return Account{}, fmt.Errorf("credential has no access or refresh token")
		}
		tok, err := Refresh(refreshToken)
		if err != nil {
			return Account{}, fmt.Errorf("refresh expired access token: %w", err)
		}
		accessToken = tok.AccessToken
	}
	oid := cred.UserID
	tid := cred.TeamID
	if oid == "" || tid == "" {
		if claims, err := decodeJWTClaims(accessToken); err == nil {
			if oid == "" {
				oid = firstNonEmpty(claims["oid"], claims["sub"])
			}
			if tid == "" {
				tid = firstNonEmpty(claims["tid"], claims["tenant_id"])
			}
		}
	}
	if oid == "" || tid == "" {
		return Account{}, fmt.Errorf("account missing oid/tid")
	}
	return Account{AccessToken: accessToken, OID: oid, TID: tid}, nil
}

// RefreshCredential exchanges the stored refresh token for a new TokenSet and
// returns the encrypted rotated credentials. If refresh fails, falls back to
// ROPC (username + password) to get a fresh token.
func (a *Adapter) RefreshCredential(ctx context.Context, cred account.Credential) (provider.RefreshedCredential, error) {
	if a.cipher == nil {
		return provider.RefreshedCredential{}, fmt.Errorf("credential cipher is not configured")
	}
	_, refreshToken, err := a.decryptAccessToken(cred)
	if err != nil {
		return provider.RefreshedCredential{}, err
	}
	select {
	case <-ctx.Done():
		return provider.RefreshedCredential{}, ctx.Err()
	default:
	}

	// 尝试 1:用 refresh token 刷新
	var tok TokenSet
	refreshErr := error(nil)
	if strings.TrimSpace(refreshToken) != "" {
		tok, refreshErr = Refresh(refreshToken)
	}
	if refreshErr != nil || strings.TrimSpace(refreshToken) == "" {
		// 尝试 2:用账号密码(ROPC)登录获取新 token
		// 从 credential 的 email 和 userID 提取 UPN
		upn := cred.Email
		if upn == "" {
			upn = cred.UserID
		}
		if upn == "" {
			if refreshErr != nil {
				return provider.RefreshedCredential{}, mapOAuthError(refreshErr)
			}
			return provider.RefreshedCredential{}, fmt.Errorf("credential has no refresh token and no UPN for ROPC")
		}
		// 从 EncryptedRefreshToken 的 \x00 分隔符后解密出密码(注册器存入)
		password := a.decryptPassword(cred)
		if password == "" {
			if refreshErr != nil {
				return provider.RefreshedCredential{}, mapOAuthError(refreshErr)
			}
			return provider.RefreshedCredential{}, fmt.Errorf("refresh token failed and no password for ROPC")
		}
		tok, err = ROPC(upn, password)
		if err != nil {
			return provider.RefreshedCredential{}, mapOAuthError(err)
		}
	}

	encAccess, err := a.cipher.Encrypt(tok.AccessToken)
	if err != nil {
		return provider.RefreshedCredential{}, fmt.Errorf("encrypt access token: %w", err)
	}
	encRefresh := refreshToken
	rotated := false
	if tok.RefreshToken != "" && tok.RefreshToken != refreshToken {
		encRefresh, err = a.cipher.Encrypt(tok.RefreshToken)
		if err != nil {
			return provider.RefreshedCredential{}, fmt.Errorf("encrypt refresh token: %w", err)
		}
		rotated = true
	} else {
		encRefresh, err = a.cipher.Encrypt(refreshToken)
		if err != nil {
			return provider.RefreshedCredential{}, fmt.Errorf("encrypt refresh token: %w", err)
		}
	}
	// 保留加密密码(注册器场景):追加到 encRefresh 后,\x00 分隔
	if password != "" {
		encPassword, err := a.cipher.Encrypt(password)
		if err == nil {
			encRefresh = encRefresh + "\x00" + encPassword
		}
	}
	return provider.RefreshedCredential{
		EncryptedAccessToken:  encAccess,
		EncryptedRefreshToken: encRefresh,
		ExpiresAt:             tok.ExpiresAt,
		RefreshTokenRotated:   rotated,
	}, nil
}

// StartDeviceAuthorization initiates the OAuth 2.0 Device Authorization Grant
// flow using the M365 FOCI device-code client.
func (a *Adapter) StartDeviceAuthorization(ctx context.Context) (provider.DeviceAuthorization, error) {
	select {
	case <-ctx.Done():
		return provider.DeviceAuthorization{}, ctx.Err()
	default:
	}
	dc, err := StartDeviceCode()
	if err != nil {
		return provider.DeviceAuthorization{}, err
	}
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	expires := time.Duration(dc.ExpiresIn) * time.Second
	return provider.DeviceAuthorization{
		DeviceCode:              dc.DeviceCode,
		UserCode:                dc.UserCode,
		VerificationURI:         dc.VerificationURI,
		VerificationURIComplete: dc.VerificationURI + "?user_code=" + dc.UserCode,
		Interval:                interval,
		ExpiresIn:               expires,
	}, nil
}

// PollDeviceAuthorization polls the device code endpoint and returns a
// CredentialSeed when the user completes authentication. If authorization is
// still pending, it returns provider.ErrAuthorizationPending.
func (a *Adapter) PollDeviceAuthorization(ctx context.Context, deviceCode string) (provider.CredentialSeed, error) {
	select {
	case <-ctx.Done():
		return provider.CredentialSeed{}, ctx.Err()
	default:
	}
	tok, done, err := PollDeviceCode(deviceCode)
	if err != nil {
		return provider.CredentialSeed{}, err
	}
	if !done {
		return provider.CredentialSeed{}, provider.ErrAuthorizationPending
	}
	seed := provider.CredentialSeed{
		Provider:     account.ProviderM365,
		AuthType:     account.AuthTypeOAuth,
		Email:        tok.Email,
		Name:         tok.DisplayName,
		UserID:       tok.HomeOID,
		TeamID:       tok.TenantID,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.ExpiresAt,
	}
	return seed, nil
}

// mapOAuthError converts an m365.OAuthError into a provider.CredentialRefreshError
// so the gateway can classify permanent vs temporary refresh failures.
func mapOAuthError(err error) error {
	if oe, ok := err.(*OAuthError); ok {
		permanent := provider.IsPermanentCredentialRefreshErrorCode(oe.Code)
		return &provider.CredentialRefreshError{
			Status:    oe.HTTPStatus,
			Code:      oe.Code,
			Message:   oe.AADSTS,
			Permanent: permanent,
			Cause:     err,
		}
	}
	return err
}

// GetBilling 实现 BillingAdapter 接口。
// M365 Copilot 没有 Billing 概念(免费商业订阅),返回空 Billing 满足接口要求。
func (a *Adapter) GetBilling(ctx context.Context, cred account.Credential) (account.Billing, error) {
	return account.Billing{}, nil
}
