package m365

import (
	"context"
	"strings"
	"time"

	"m365-copilot2xapi/backend/internal/domain/account"
	"m365-copilot2xapi/backend/internal/infra/provider"
)

// staticModels is the built-in model catalog, ported from
// M365-Copilot2API/internal/web/codex_catalog.go gatewayModels. Each model ID
// uses the M365/ prefix as the internal routing namespace.
var staticModels = []string{
	"M365/gpt-5.2",
	"M365/gpt-5.2-reasoning",
	"M365/gpt-5.3",
	"M365/gpt-5.4",
	"M365/gpt-5.4-reasoning",
	"M365/gpt-5.5",
	"M365/gpt-5.5-reasoning",
	"M365/gpt-5.6-reasoning",
	"M365/gpt-image-2",
	"M365/claude-sonnet",
	"M365/claude-sonnet-reasoning",
}

// ListModels returns the static model catalog. The credential parameter is
// accepted for interface conformance but M365 does not discover models from
// upstream; the catalog is built-in.
func (a *Adapter) ListModels(_ context.Context, _ account.Credential) ([]string, error) {
	out := make([]string, len(staticModels))
	copy(out, staticModels)
	return out, nil
}

// ModelAliases returns compatibility aliases that map common client-facing
// model names to canonical M365 routes.
func (a *Adapter) ModelAliases() []provider.ModelAlias {
	return []provider.ModelAlias{
		{Alias: "gpt-5.2", PublicModel: "M365/gpt-5.2", Provider: account.ProviderM365, UpstreamModel: "gpt-5.2"},
		{Alias: "gpt-5.2-reasoning", PublicModel: "M365/gpt-5.2-reasoning", Provider: account.ProviderM365, UpstreamModel: "gpt-5.2-reasoning"},
		{Alias: "gpt-5.3", PublicModel: "M365/gpt-5.3", Provider: account.ProviderM365, UpstreamModel: "gpt-5.3"},
		{Alias: "gpt-5.4", PublicModel: "M365/gpt-5.4", Provider: account.ProviderM365, UpstreamModel: "gpt-5.4"},
		{Alias: "gpt-5.4-reasoning", PublicModel: "M365/gpt-5.4-reasoning", Provider: account.ProviderM365, UpstreamModel: "gpt-5.4-reasoning"},
		{Alias: "gpt-5.5", PublicModel: "M365/gpt-5.5", Provider: account.ProviderM365, UpstreamModel: "gpt-5.5"},
		{Alias: "gpt-5.5-reasoning", PublicModel: "M365/gpt-5.5-reasoning", Provider: account.ProviderM365, UpstreamModel: "gpt-5.5-reasoning"},
		{Alias: "gpt-5.6-reasoning", PublicModel: "M365/gpt-5.6-reasoning", Provider: account.ProviderM365, UpstreamModel: "gpt-5.6-reasoning"},
		{Alias: "claude-sonnet", PublicModel: "M365/claude-sonnet", Provider: account.ProviderM365, UpstreamModel: "claude-sonnet"},
		{Alias: "claude-sonnet-reasoning", PublicModel: "M365/claude-sonnet-reasoning", Provider: account.ProviderM365, UpstreamModel: "claude-sonnet-reasoning"},
		{Alias: "claude", PublicModel: "M365/claude-sonnet", Provider: account.ProviderM365, UpstreamModel: "claude-sonnet"},
	}
}

// modelTone maps a model name (without the M365/ prefix) to the ChatHub tone
// identifier. Ported from M365-Copilot2API/internal/web/codex_catalog.go.
func modelTone(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-5.2":
		return "Gpt_5_2_Chat"
	case "gpt-5.2-reasoning":
		return "Gpt_5_2_Reasoning"
	case "gpt-5.3":
		return "Gpt_5_3_Chat"
	case "gpt-5.4":
		return "Gpt_5_4_Chat"
	case "gpt-5.4-reasoning":
		return "Gpt_5_4_Reasoning"
	case "gpt-5.5":
		return "Gpt_5_5_Chat"
	case "gpt-5.5-reasoning":
		return "Gpt_5_5_Reasoning"
	case "gpt-5.6-reasoning":
		return "Gpt_5_6_Reasoning"
	case "claude", "claude-sonnet":
		return "Claude_Sonnet"
	case "claude-sonnet-reasoning":
		return "Claude_Sonnet_Reasoning"
	case "gpt-5.4-quick":
		return "Gpt_5_4_Chat"
	case "gpt-5.3-think-deeper":
		return "Gpt_5_3_Chat"
	default:
		return "magic"
	}
}

// normalizeReasoningEffort validates and normalizes a reasoning effort string.
func normalizeReasoningEffort(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return "", nil
	}
	switch e {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return e, nil
	}
	return "", nil
}

// reasoningTone maps a model name and reasoning effort to the ChatHub tone.
// Ported from M365-Copilot2API/internal/web/codex_catalog.go reasoningTone.
func reasoningTone(model, effort string) (string, error) {
	e, err := normalizeReasoningEffort(effort)
	if err != nil {
		return "", err
	}
	base := modelTone(model)
	if strings.Contains(strings.ToLower(model), "reasoning") {
		return base, nil
	}
	if e == "" || e == "none" || e == "minimal" || e == "low" {
		return base, nil
	}
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "claude", "claude-sonnet":
		return "Claude_Sonnet_Reasoning", nil
	case "gpt-5.2":
		return "Gpt_5_2_Reasoning", nil
	case "gpt-5.3":
		return "Gpt_5_3_Reasoning", nil
	case "gpt-5.4":
		return "Gpt_5_4_Reasoning", nil
	case "gpt-5.5":
		return "Gpt_5_5_Reasoning", nil
	case "gpt-5.6":
		return "Gpt_5_5_Reasoning", nil
	default:
		return "Gpt_5_5_Reasoning", nil
	}
}

// stripModelPrefix removes the "M365/" namespace prefix from an internal
// routing ID, returning the bare model name used for tone mapping.
func stripModelPrefix(model string) string {
	prefix := account.ProviderM365.ModelNamespace() + "/"
	if len(model) >= len(prefix) && strings.EqualFold(model[:len(prefix)], prefix) {
		return strings.TrimSpace(model[len(prefix):])
	}
	return strings.TrimSpace(model)
}

// estimateTokens provides a rough token count for usage estimation since
// ChatHub does not return upstream token counts.
func estimateTokens(text string) int64 {
	return int64(len(text) / 4)
}

// now returns the current UTC time. Indirected for testability.
var now = func() time.Time { return time.Now().UTC() }
