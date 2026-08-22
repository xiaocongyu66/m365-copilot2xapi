package m365

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"M365Copilot2ApiX/backend/internal/infra/provider"
)

// ---------------------------------------------------------------------------
// Request parsing
// ---------------------------------------------------------------------------

// oaiMessage is the OpenAI chat-completions message shape used for parsing.
type oaiMessage struct {
	Role             string           `json:"role"`
	Content          json.RawMessage  `json:"content"`
	Name             string           `json:"name,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []map[string]any `json:"tool_calls,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
}

// oaiChatRequest is the OpenAI /v1/chat/completions request body.
type oaiChatRequest struct {
	Model           string            `json:"model"`
	Messages        []oaiMessage      `json:"messages"`
	Stream          bool              `json:"stream"`
	Tools           []Tool            `json:"tools,omitempty"`
	Functions       []json.RawMessage `json:"functions,omitempty"`
	ToolChoice      any               `json:"tool_choice,omitempty"`
	FunctionCall    any               `json:"function_call,omitempty"`
	Reasoning       *reasoningCfg     `json:"reasoning,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
}

type reasoningCfg struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// anthropicMessage is the Anthropic /v1/messages content block shape.
type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// anthropicRequest is the Anthropic /v1/messages request body.
type anthropicRequest struct {
	Model    string             `json:"model"`
	Messages []anthropicMessage `json:"messages"`
	System   json.RawMessage    `json:"system,omitempty"`
	Stream   bool               `json:"stream"`
	Tools    []json.RawMessage  `json:"tools,omitempty"`
}

// parsedRequest is the protocol-neutral internal representation after parsing
// the incoming OpenAI or Anthropic request body.
type parsedRequest struct {
	prompt          string
	attachments     []Attachment
	tools           []Tool
	toolChoice      any
	stream          bool
	model           string
	reasoningEffort string
}

// normalizeLegacyTools converts OpenAI legacy functions/function_call into
// tools/tool_choice, mirroring M365-Copilot2API/internal/web/server.go.
func normalizeLegacyTools(body *oaiChatRequest) {
	if len(body.Tools) == 0 && len(body.Functions) > 0 {
		body.Tools = make([]Tool, 0, len(body.Functions))
		for _, f := range body.Functions {
			body.Tools = append(body.Tools, Tool{Type: "function", Function: f})
		}
	}
	if body.ToolChoice == nil && body.FunctionCall != nil {
		body.ToolChoice = body.FunctionCall
	}
}

// parseOpenAIChat parses an OpenAI /v1/chat/completions request body into the
// protocol-neutral parsedRequest.
func parseOpenAIChat(body []byte) (parsedRequest, error) {
	var req oaiChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return parsedRequest{}, fmt.Errorf("parse openai chat body: %w", err)
	}
	normalizeLegacyTools(&req)
	effort := req.ReasoningEffort
	if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
		effort = req.Reasoning.Effort
	}
	prompt, attachments := flattenPromptMessages(req.Messages, nil)
	return parsedRequest{
		prompt:          strings.TrimSpace(prompt),
		attachments:     attachments,
		tools:           req.Tools,
		toolChoice:      req.ToolChoice,
		stream:          req.Stream,
		model:           req.Model,
		reasoningEffort: effort,
	}, nil
}

// parseAnthropicMessages parses an Anthropic /v1/messages request body into the
// protocol-neutral parsedRequest. Anthropic system is prepended as a system
// message; Anthropic content blocks are mapped to the OpenAI message shape so
// flattenPromptMessages can handle them uniformly.
func parseAnthropicMessages(body []byte) (parsedRequest, error) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return parsedRequest{}, fmt.Errorf("parse anthropic body: %w", err)
	}
	var msgs []oaiMessage
	// Anthropic system can be a string or an array of content blocks.
	if len(req.System) > 0 {
		systemText := extractAnthropicText(req.System)
		if strings.TrimSpace(systemText) != "" {
			msgs = append(msgs, oaiMessage{Role: "system", Content: json.RawMessage(`"` + jsonEscape(systemText) + `"`)})
		}
	}
	for _, m := range req.Messages {
		msgs = append(msgs, oaiMessage{Role: m.Role, Content: m.Content})
	}
	// Convert Anthropic tools to OpenAI tool shape.
	var tools []Tool
	for _, raw := range req.Tools {
		tools = append(tools, Tool{Type: "function", Function: raw})
	}
	prompt, attachments := flattenPromptMessages(msgs, nil)
	return parsedRequest{
		prompt:      strings.TrimSpace(prompt),
		attachments: attachments,
		tools:       tools,
		stream:      req.Stream,
		model:       req.Model,
	}, nil
}

// parseResponsesAPI parses an OpenAI /v1/responses request body. The Responses
// API uses "input" instead of "messages" and "instructions" instead of system.
func parseResponsesAPI(body []byte) (parsedRequest, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return parsedRequest{}, fmt.Errorf("parse responses body: %w", err)
	}
	model, _ := raw["model"].(string)
	stream, _ := raw["stream"].(bool)
	effort, _ := raw["reasoning"].(map[string]any)
	effortStr, _ := effort["effort"].(string)

	var msgs []oaiMessage
	// instructions → system message
	if instr, ok := raw["instructions"].(string); ok && strings.TrimSpace(instr) != "" {
		msgs = append(msgs, oaiMessage{Role: "system", Content: json.RawMessage(`"` + jsonEscape(instr) + `"`)})
	}
	// input can be a string or an array of input items
	switch input := raw["input"].(type) {
	case string:
		msgs = append(msgs, oaiMessage{Role: "user", Content: json.RawMessage(`"` + jsonEscape(input) + `"`)})
	case []any:
		for _, item := range input {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			if role == "" {
				role = "user"
			}
			content, _ := m["content"]
			contentBytes, _ := json.Marshal(content)
			msgs = append(msgs, oaiMessage{Role: role, Content: contentBytes})
		}
	}
	prompt, attachments := flattenPromptMessages(msgs, nil)
	return parsedRequest{
		prompt:          strings.TrimSpace(prompt),
		attachments:     attachments,
		stream:          stream,
		model:           model,
		reasoningEffort: effortStr,
	}, nil
}

// parseRequest dispatches parsing based on the Operation field.
func parseRequest(operation string, body []byte) (parsedRequest, error) {
	switch operation {
	case "messages":
		return parseAnthropicMessages(body)
	case "responses":
		return parseResponsesAPI(body)
	default:
		return parseOpenAIChat(body)
	}
}

// ---------------------------------------------------------------------------
// Prompt flattening (ported from M365-Copilot2API/internal/web/prompt.go)
// ---------------------------------------------------------------------------

// flattenPromptMessages preserves role boundaries when adapting OpenAI messages
// to ChatHub's single message.text field. It also extracts image attachments
// from multimodal content blocks.
func flattenPromptMessages(messages []oaiMessage, attachments []Attachment) (string, []Attachment) {
	var b strings.Builder
	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "" {
			role = "user"
		}
		txt, files := parseContent(m.Content)
		attachments = append(attachments, files...)
		txt = strings.TrimSpace(txt)
		if len(m.ToolCalls) > 0 {
			if txt != "" {
				b.WriteString(fmt.Sprintf("\n[%s]\n%s\n", role, txt))
			}
			calls, _ := json.Marshal(m.ToolCalls)
			b.WriteString(fmt.Sprintf("\n[%s tool_calls]\n%s\n", role, string(calls)))
			continue
		}
		if role == "tool" {
			txt = compactToolResult(txt, 4000)
			b.WriteString(fmt.Sprintf("\n[tool result id=%s]\n%s\n", m.ToolCallID, txt))
			continue
		}
		if txt == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("\n[%s]\n%s\n", role, txt))
	}
	return strings.TrimSpace(b.String()), attachments
}

// parseContent extracts text and image attachments from an OpenAI/Anthropic
// content field. Ported from M365-Copilot2API/internal/web/multimodal.go.
func parseContent(c json.RawMessage) (string, []Attachment) {
	if len(c) == 0 {
		return "", nil
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(c, &s); err == nil {
		return s, nil
	}
	// Try array of content parts.
	var parts []any
	if err := json.Unmarshal(c, &parts); err != nil {
		// Fall back to raw representation.
		return string(c), nil
	}
	var text strings.Builder
	var files []Attachment
	for _, raw := range parts {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		if v, ok := m["text"].(string); ok && (typ == "text" || typ == "input_text" || typ == "output_text" || typ == "") {
			text.WriteString(v)
		}
		if direct, ok := m["image_url"].(string); ok && direct != "" {
			files = append(files, Attachment{Type: "image", URL: direct, MimeType: "image/*"})
		}
		switch typ {
		case "text", "input_text", "output_text":
			// handled above
		case "image_url":
			if u, ok := m["image_url"].(map[string]any); ok {
				if v, ok := u["url"].(string); ok {
					a := Attachment{Type: "image", URL: v, MimeType: "image/*"}
					if d, ok := u["detail"].(string); ok {
						a.Detail = d
					}
					files = append(files, a)
				}
			}
		case "input_image", "image":
			if v, ok := m["image_url"].(string); ok && v != "" {
				files = append(files, Attachment{Type: "image", URL: v, MimeType: "image/*"})
			} else if u, ok := m["image_url"].(map[string]any); ok {
				if v, ok := u["url"].(string); ok {
					files = append(files, Attachment{Type: "image", URL: v, MimeType: "image/*"})
				}
			}
			if src, ok := m["source"].(map[string]any); ok {
				if u, ok := src["url"].(string); ok {
					files = append(files, Attachment{Type: "image", URL: u, MimeType: "image/*"})
				}
			}
		}
	}
	return text.String(), files
}

// extractAnthropicText extracts text from an Anthropic system field, which can
// be a plain string or an array of content blocks.
func extractAnthropicText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []any
	if err := json.Unmarshal(raw, &parts); err != nil {
		return strings.TrimSpace(string(raw))
	}
	var b strings.Builder
	for _, part := range parts {
		m, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "text" {
			if s, ok := m["text"].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

// compactToolResult truncates tool result text to a bounded length.
func compactToolResult(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…[truncated]"
}

// jsonEscape returns a JSON-escaped string literal content for s.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// ---------------------------------------------------------------------------
// ForwardResponse
// ---------------------------------------------------------------------------

// ForwardResponse implements provider.ResponseAdapter. It parses the incoming
// OpenAI/Anthropic request, calls ChatHub via WebSocket, and returns the
// response as an OpenAI-compatible SSE stream (streaming) or JSON body
// (non-streaming).
func (a *Adapter) ForwardResponse(ctx context.Context, request provider.ResponseResourceRequest) (*provider.Response, error) {
	// Record reasoning effort in NormalizedMetadata so the gateway audit trail
	// reflects the real upstream intent.
	if request.NormalizedMetadata == nil {
		request.NormalizedMetadata = &provider.NormalizedRequestMetadata{}
	}

	parsed, err := parseRequest(request.Operation, request.Body)
	if err != nil {
		return errorResponse(http.StatusBadRequest, "invalid_request_error", err.Error()), nil
	}

	// Update NormalizedMetadata with the reasoning effort extracted from the body.
	request.NormalizedMetadata.ReasoningEffort = parsed.reasoningEffort

	if strings.TrimSpace(parsed.prompt) == "" && len(parsed.attachments) == 0 {
		return errorResponse(http.StatusBadRequest, "invalid_request_error", "messages required"), nil
	}

	// Map model to ChatHub tone.
	modelName := stripModelPrefix(request.Model)
	if modelName == "" {
		modelName = stripModelPrefix(parsed.model)
	}
	if modelName == "" {
		modelName = "gpt-5.2"
	}
	tone, toneErr := reasoningTone(modelName, parsed.reasoningEffort)
	if toneErr != nil {
		return errorResponse(http.StatusBadRequest, "invalid_request_error", toneErr.Error()), nil
	}

	// Resolve account from credential.
	acc, err := a.resolveAccount(request.Credential)
	if err != nil {
		return errorResponse(http.StatusUnauthorized, "account_error", err.Error()), nil
	}

	// Normalize tool choice.
	toolChoice := parsed.toolChoice
	if toolChoice == nil && len(parsed.tools) > 0 {
		toolChoice = "auto"
	}

	// Build ChatHub request.
	hubReq := Request{
		Text:        parsed.prompt,
		Tone:        tone,
		Attachments: parsed.attachments,
		Tools:       parsed.tools,
		ToolChoice:  toolChoice,
	}

	// Set a default tool choice for the ChatHub payload.
	model := parsed.model
	if model == "" {
		model = request.Model
	}
	if model == "" {
		model = "m365-copilot"
	}

	chatCtx, cancel := context.WithTimeout(ctx, a.chatTimeout())
	defer cancel()

	if parsed.stream || request.Streaming {
		return a.forwardStreaming(chatCtx, acc, hubReq, model)
	}
	return a.forwardNonStreaming(chatCtx, acc, hubReq, model, parsed.prompt)
}

// forwardStreaming opens an io.Pipe and writes OpenAI SSE chunks in a goroutine.
func (a *Adapter) forwardStreaming(ctx context.Context, acc Account, req Request, model string) (*provider.Response, error) {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		a.streamChatHubToPipe(ctx, acc, req, model, pw)
	}()
	header := make(http.Header)
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	return &provider.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     header,
		Body:       pr,
	}, nil
}

// streamChatHubToPipe calls ChatHub with streaming callbacks and writes OpenAI
// SSE chunks to the pipe writer.
func (a *Adapter) streamChatHubToPipe(ctx context.Context, acc Account, req Request, model string, w io.Writer) {
	id := "chatcmpl-" + uuid.NewString()
	created := time.Now().Unix()
	first := true

	writeSSE := func(chunk map[string]any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		return nil
	}

	writeDelta := func(content string, reasoning bool) error {
		if content == "" {
			return nil
		}
		delta := map[string]any{}
		if first {
			delta["role"] = "assistant"
			first = false
		}
		if reasoning {
			delta["reasoning_content"] = content
		} else {
			delta["content"] = content
		}
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			}},
		}
		return writeSSE(chunk)
	}

	// Send the connected preamble.
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}

	onDelta := func(content string) error {
		return writeDelta(content, false)
	}
	onReasoning := func(content string) error {
		return writeDelta(content, true)
	}

	res, err := a.client.ChatWithReasoning(ctx, acc, req, onDelta, onReasoning)
	if err != nil {
		// Emit an error SSE event then [DONE].
		msg := upstreamErrorMessage(err)
		_ = writeSSE(map[string]any{
			"error": map[string]any{
				"message": msg,
				"code":    upstreamErrorCode(err),
			},
		})
		_ = writeSSE(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     estimateTokens(req.Text),
				"completion_tokens": 0,
				"total_tokens":      estimateTokens(req.Text),
			},
		})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}

	// If no text was streamed via deltas but the result has text, emit it now.
	if res.Text != "" && first {
		_ = writeDelta(res.Text, false)
	}

	// Emit the final chunk with finish_reason and usage.
	pt := estimateTokens(req.Text)
	ct := estimateTokens(res.Text)
	finishChunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     pt,
			"completion_tokens": ct,
			"total_tokens":      pt + ct,
		},
	}
	_ = writeSSE(finishChunk)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

// forwardNonStreaming calls ChatHub synchronously and returns the result as a
// JSON body.
func (a *Adapter) forwardNonStreaming(ctx context.Context, acc Account, req Request, model, prompt string) (*provider.Response, error) {
	res, err := a.client.Chat(ctx, acc, req)
	if err != nil {
		if errors.Is(err, ErrEmptyCompletion) && req.Tone != defaultTone {
			// Retry with the default tone if the requested tone is unavailable.
			magicReq := req
			magicReq.Tone = defaultTone
			if res2, err2 := a.client.Chat(ctx, acc, magicReq); err2 == nil && res2.Text != "" {
				res = res2
				err = nil
			}
		}
	}
	if err != nil {
		status := http.StatusBadGateway
		code := upstreamErrorCode(err)
		if isAuthError(err) {
			status = http.StatusUnauthorized
		}
		return errorResponse(status, code, upstreamErrorMessage(err)), nil
	}

	id := "chatcmpl-" + uuid.NewString()
	created := time.Now().Unix()
	pt := estimateTokens(prompt)
	ct := estimateTokens(res.Text)

	assistant := map[string]any{
		"role":    "assistant",
		"content": res.Text,
	}
	if res.Reasoning != "" {
		assistant["reasoning_content"] = res.Reasoning
	}

	body := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       assistant,
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     pt,
			"completion_tokens": ct,
			"total_tokens":      pt + ct,
		},
	}
	if res.ConversationID != "" {
		body["m365"] = map[string]any{
			"conversationId": res.ConversationID,
			"sessionId":      res.SessionID,
			"requestId":      res.RequestID,
		}
	}

	data, _ := json.Marshal(body)
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &provider.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(data)),
	}, nil
}

// ---------------------------------------------------------------------------
// Error helpers
// ---------------------------------------------------------------------------

// errorResponse builds a provider.Response carrying an OpenAI-style error JSON.
func errorResponse(status int, code, message string) *provider.Response {
	body := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    code,
			"code":    code,
		},
	}
	data, _ := json.Marshal(body)
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &provider.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

// upstreamErrorMessage converts a ChatHub error to a safe client message.
func upstreamErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrRateLimitNotice) {
		return "upstream is rate limiting; try again shortly"
	}
	if errors.Is(err, ErrEmptyCompletion) {
		return "upstream returned empty completion; the requested model may be unavailable for this tenant"
	}
	var dialErr *DialError
	if errors.As(err, &dialErr) {
		if dialErr.Status == 429 {
			return "upstream is rate limiting; try again shortly"
		}
		if dialErr.Status == 401 || dialErr.Status == 403 {
			return "upstream credential rejected"
		}
		return fmt.Sprintf("upstream dial failed: HTTP %d", dialErr.Status)
	}
	return "upstream error"
}

// upstreamErrorCode classifies a ChatHub error into a short error code.
func upstreamErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrRateLimitNotice) {
		return "rate_limit"
	}
	if errors.Is(err, ErrEmptyCompletion) {
		return "upstream_error"
	}
	var dialErr *DialError
	if errors.As(err, &dialErr) {
		if dialErr.Status == 429 {
			return "rate_limit"
		}
		if dialErr.Status == 401 || dialErr.Status == 403 {
			return "account_error"
		}
		return "upstream_error"
	}
	return "upstream_error"
}

// isAuthError reports whether the error is an authentication failure.
func isAuthError(err error) bool {
	var dialErr *DialError
	if errors.As(err, &dialErr) {
		return dialErr.Status == 401 || dialErr.Status == 403
	}
	return false
}
