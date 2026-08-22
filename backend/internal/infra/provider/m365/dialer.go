package m365

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// DefaultHTTPClient returns a simple direct-connect HTTP client.
//
// The original M365-Copilot2API chathub/auth packages depended on an
// outbound proxy pool (m365-copilot2api/internal/outbound). During the
// initial port into grok2api we intentionally bypass the grok2api egress
// system and use a plain *http.Client. A later phase will wire this up to
// the real egress layer.
func DefaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 120 * time.Second,
	}
}

// DefaultWebSocketDialer returns a simple direct-connect WebSocket dialer.
//
// As with DefaultHTTPClient, this is a stand-in for the outbound proxy
// dialer and does not route through grok2api egress yet.
func DefaultWebSocketDialer() *websocket.Dialer {
	return &websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
	}
}
