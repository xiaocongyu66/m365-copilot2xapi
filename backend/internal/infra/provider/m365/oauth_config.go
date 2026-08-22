package m365

import (
	"strings"

	"M365Copilot2ApiX/backend/internal/infra/config"
)

// Office web Copilot first-party client (verified working with ChatHub via browser PKCE).
// The default authority is multi-tenant so any supported Microsoft account can sign in.
// Device-code/FOCI client can still be forced by overriding the OAuthConfig.
const (
	DefaultClientID    = config.M365DefaultClientID
	FOCIClientID       = config.M365FOCIClientID
	DefaultAuthority   = config.M365DefaultAuthority
	DefaultRedirectURI = config.M365DefaultRedirectURI
	DefaultScope       = config.M365DefaultScope
)

// OAuthConfig holds the OAuth client configuration used by the browser PKCE
// and device-code flows. It is seeded with the Microsoft 365 Copilot
// first-party defaults and can be overridden via SetOAuthConfig so the
// grok2api configuration layer (config.M365ProviderConfig) becomes the single
// source of truth instead of os.Getenv.
type OAuthConfig struct {
	ClientID        string
	Authority       string
	RedirectURI     string
	Scope           string
	DeviceClientID  string
	DeviceAuthority string
	DeviceScope     string

	// Optional endpoint overrides. When non-empty they take precedence over
	// the derived Authority()/DeviceAuthority() endpoints.
	AuthorizeEndpoint   string
	TokenEndpoint       string
	DeviceCodeEndpoint  string
	DeviceTokenEndpoint string
}

// defaultOAuthConfig returns the built-in defaults, mirroring the constants
// defined above and in the grok2api config package.
func defaultOAuthConfig() OAuthConfig {
	return OAuthConfig{
		ClientID:            DefaultClientID,
		Authority:           DefaultAuthority,
		RedirectURI:         DefaultRedirectURI,
		Scope:               DefaultScope,
		DeviceClientID:      FOCIClientID,
		DeviceAuthority:     DefaultAuthority,
		DeviceScope:         DefaultScope,
		AuthorizeEndpoint:   "",
		TokenEndpoint:       "",
		DeviceCodeEndpoint:  "",
		DeviceTokenEndpoint: "",
	}
}

// oauthConfig is the package-level configuration source. It is initialised
// with the defaults and updated by SetOAuthConfig. All ClientID/Authority/...
// helpers below read from this value so that the auth/token/device/cache
// code picks up overrides without their signatures changing.
var oauthConfig = defaultOAuthConfig()

// SetOAuthConfig applies non-empty overrides from the grok2api configuration
// layer onto the package-level OAuth config. Fields left empty in the input
// retain their previous value, so callers may safely pass a partially
// populated struct.
func SetOAuthConfig(c OAuthConfig) {
	oauthConfig = applyOverrides(oauthConfig, c)
}

// applyOverrides returns dst with every non-empty field from src replaced.
func applyOverrides(dst, src OAuthConfig) OAuthConfig {
	if src.ClientID != "" {
		dst.ClientID = src.ClientID
	}
	if src.Authority != "" {
		dst.Authority = src.Authority
	}
	if src.RedirectURI != "" {
		dst.RedirectURI = src.RedirectURI
	}
	if src.Scope != "" {
		dst.Scope = src.Scope
	}
	if src.DeviceClientID != "" {
		dst.DeviceClientID = src.DeviceClientID
	}
	if src.DeviceAuthority != "" {
		dst.DeviceAuthority = src.DeviceAuthority
	}
	if src.DeviceScope != "" {
		dst.DeviceScope = src.DeviceScope
	}
	if strings.TrimSpace(src.AuthorizeEndpoint) != "" {
		dst.AuthorizeEndpoint = src.AuthorizeEndpoint
	}
	if strings.TrimSpace(src.TokenEndpoint) != "" {
		dst.TokenEndpoint = src.TokenEndpoint
	}
	if strings.TrimSpace(src.DeviceCodeEndpoint) != "" {
		dst.DeviceCodeEndpoint = src.DeviceCodeEndpoint
	}
	if strings.TrimSpace(src.DeviceTokenEndpoint) != "" {
		dst.DeviceTokenEndpoint = src.DeviceTokenEndpoint
	}
	return dst
}

// FromM365ProviderConfig maps a grok2api config.M365ProviderConfig into the
// OAuthConfig shape used by this package. This keeps the wiring in one place
// so the adapter/application layer can call SetOAuthConfig(FromM365ProviderConfig(cfg))
// without the auth code depending on the config package's YAML tags.
func FromM365ProviderConfig(cfg config.M365ProviderConfig) OAuthConfig {
	return OAuthConfig{
		ClientID:        cfg.ClientID,
		Authority:       cfg.Authority,
		RedirectURI:     cfg.RedirectURI,
		Scope:           cfg.Scope,
		DeviceClientID:  cfg.DeviceClientID,
		DeviceAuthority: cfg.DeviceAuthority,
		DeviceScope:     cfg.DeviceScope,
	}
}

func ClientID() string {
	return oauthConfig.ClientID
}

func Authority() string {
	return oauthConfig.Authority
}

func RedirectURI() string {
	return oauthConfig.RedirectURI
}

func Scope() string {
	return oauthConfig.Scope
}

func DeviceClientID() string {
	return oauthConfig.DeviceClientID
}

func DeviceAuthority() string {
	return oauthConfig.DeviceAuthority
}

func DeviceScope() string {
	return oauthConfig.DeviceScope
}

func AuthorizeEndpoint() string {
	if oauthConfig.AuthorizeEndpoint != "" {
		return oauthConfig.AuthorizeEndpoint
	}
	return Authority() + "/oauth2/v2.0/authorize"
}

func TokenEndpoint() string {
	if oauthConfig.TokenEndpoint != "" {
		return oauthConfig.TokenEndpoint
	}
	return Authority() + "/oauth2/v2.0/token"
}

func DeviceCodeEndpoint() string {
	if oauthConfig.DeviceCodeEndpoint != "" {
		return oauthConfig.DeviceCodeEndpoint
	}
	return DeviceAuthority() + "/oauth2/v2.0/devicecode"
}

func DeviceTokenEndpoint() string {
	if oauthConfig.DeviceTokenEndpoint != "" {
		return oauthConfig.DeviceTokenEndpoint
	}
	return DeviceAuthority() + "/oauth2/v2.0/token"
}
