package m365

import (
	"m365-copilot2xapi/backend/internal/domain/account"
	modeldomain "m365-copilot2xapi/backend/internal/domain/model"
	"m365-copilot2xapi/backend/internal/infra/provider"
)

// Definition declares the static capability boundary of the M365 Copilot
// Provider. M365 is an OAuth-only, text-and-reasoning channel backed by the
// ChatHub WebSocket protocol. It does not support media generation, realtime
// voice, or stored responses.
func (a *Adapter) Definition() provider.Definition {
	return provider.Definition{
		Provider:       account.ProviderM365,
		ModelNamespace: account.ProviderM365.ModelNamespace(),
		ModelCatalog:   provider.ModelCatalogStatic,
		ModelCapabilities: []modeldomain.Capability{
			modeldomain.CapabilityResponses,
			modeldomain.CapabilityChat,
		},
		Quota: provider.QuotaLocalWindow,
		Credential: provider.CredentialSurface{
			AuthType:    account.AuthTypeOAuth,
			Import:      true,
			Refresh:     true,
			DeviceOAuth: true,
		},
		Conversation: provider.ConversationSurface{
			Responses:       true,
			ChatCompletions: true,
			Messages:        true,
			Compact:         false,
			StoredResponses: false,
		},
		Media: provider.MediaSurface{
			ImageGeneration: false,
			ImageEdit:       false,
			VideoGeneration: false,
			TTS:             false,
			STT:             false,
			Realtime:        false,
		},
		Inference: provider.InferencePolicy{
			Usage: provider.UsageEstimated,
		},
	}
}
