package m365

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"M365Copilot2ApiX/backend/internal/domain/account"
	"M365Copilot2ApiX/backend/internal/infra/provider"
)

// importedAccount is the JSON shape accepted by ParseImportedCredentials.
// It supports both the M365-Copilot2API AccountToken cache format and a
// simplified {refresh_token, email} object.
type importedAccount struct {
	RefreshToken      string `json:"refresh_token"`
	RefreshTokenCamel string `json:"refreshToken"`
	Email             string `json:"email"`
	DisplayName       string `json:"displayName,omitempty"`
	OID               string `json:"oid,omitempty"`
	TID               string `json:"tid,omitempty"`
}

// ParseImportedCredentials accepts a JSON byte slice and returns a slice of
// CredentialSeed values. Supported formats:
//  1. JSON array of objects with refresh_token/email fields (or camelCase
//     refreshToken, mirroring the M365-Copilot2API AccountToken cache).
//  2. JSON object with the same shape (single account).
//  3. A bare refresh token string.
//
// Each seed is populated with the refresh token, email, and OID/TID if
// available. The caller (PrepareImportedCredential or the application layer)
// is responsible for exchanging the refresh token for an access token before
// persistence.
func (a *Adapter) ParseImportedCredentials(data []byte) ([]provider.CredentialSeed, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("import data is empty")
	}

	// Try JSON array first.
	var accounts []importedAccount
	if err := json.Unmarshal(data, &accounts); err == nil && len(accounts) > 0 {
		return buildSeeds(accounts), nil
	}

	// Try single JSON object.
	var single importedAccount
	if err := json.Unmarshal(data, &single); err == nil && (single.RefreshToken != "" || single.RefreshTokenCamel != "") {
		return buildSeeds([]importedAccount{single}), nil
	}

	// Fall back to bare refresh token string. Strip surrounding quotes if the
	// input is a JSON string literal.
	bare := trimmed
	if strings.HasPrefix(bare, "\"") && strings.HasSuffix(bare, "\"") {
		_ = json.Unmarshal(data, &bare)
	}
	bare = strings.TrimSpace(bare)
	if bare == "" {
		return nil, fmt.Errorf("no refresh token found in import data")
	}
	return []provider.CredentialSeed{
		{
			Provider:     account.ProviderM365,
			AuthType:     account.AuthTypeOAuth,
			RefreshToken: bare,
		},
	}, nil
}

// buildSeeds converts importedAccount entries into CredentialSeed values,
// preferring the snake_case refresh_token field and falling back to camelCase.
func buildSeeds(accounts []importedAccount) []provider.CredentialSeed {
	seeds := make([]provider.CredentialSeed, 0, len(accounts))
	for _, acc := range accounts {
		rt := acc.RefreshToken
		if rt == "" {
			rt = acc.RefreshTokenCamel
		}
		rt = strings.TrimSpace(rt)
		if rt == "" {
			continue
		}
		seeds = append(seeds, provider.CredentialSeed{
			Provider:     account.ProviderM365,
			AuthType:     account.AuthTypeOAuth,
			Name:         acc.DisplayName,
			Email:        acc.Email,
			UserID:       acc.OID,
			TeamID:       acc.TID,
			RefreshToken: rt,
		})
	}
	return seeds
}

// MarshalCredentials serializes a slice of CredentialSeed values into a JSON
// array using the M365-Copilot2API AccountToken cache format. Encrypted tokens
// are not included; only non-sensitive metadata and the refresh token are
// persisted so the export can be re-imported.
func (a *Adapter) MarshalCredentials(values []provider.CredentialSeed) ([]byte, error) {
	type exportAccount struct {
		Email        string `json:"email,omitempty"`
		DisplayName  string `json:"displayName,omitempty"`
		RefreshToken string `json:"refreshToken,omitempty"`
		OID          string `json:"oid,omitempty"`
		TID          string `json:"tid,omitempty"`
	}
	out := make([]exportAccount, 0, len(values))
	for _, v := range values {
		out = append(out, exportAccount{
			Email:        v.Email,
			DisplayName:  v.Name,
			RefreshToken: v.RefreshToken,
			OID:          v.UserID,
			TID:          v.TeamID,
		})
	}
	return json.MarshalIndent(out, "", "  ")
}

// PrepareImportedCredential exchanges an imported refresh token for a full
// TokenSet, then populates AccessToken, Email, OID, and TID on the seed.
// This implements the CredentialImportPreparer interface so the application
// layer can persist a complete credential after import.
func (a *Adapter) PrepareImportedCredential(ctx context.Context, seed provider.CredentialSeed) (provider.CredentialSeed, error) {
	if strings.TrimSpace(seed.RefreshToken) == "" {
		return seed, fmt.Errorf("imported credential has no refresh token")
	}
	select {
	case <-ctx.Done():
		return seed, ctx.Err()
	default:
	}
	tok, err := Refresh(seed.RefreshToken)
	if err != nil {
		return seed, fmt.Errorf("exchange imported refresh token: %w", err)
	}
	result := seed
	result.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		result.RefreshToken = tok.RefreshToken
	}
	if result.Email == "" {
		result.Email = tok.Email
	}
	if result.Name == "" {
		result.Name = tok.DisplayName
	}
	if result.UserID == "" {
		result.UserID = tok.HomeOID
	}
	if result.TeamID == "" {
		result.TeamID = tok.TenantID
	}
	result.ExpiresAt = tok.ExpiresAt
	return result, nil
}
