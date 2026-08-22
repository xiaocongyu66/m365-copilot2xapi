package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"

	"m365-copilot2xapi/backend/internal/domain/account"
	clientkeydomain "m365-copilot2xapi/backend/internal/domain/clientkey"
	"m365-copilot2xapi/backend/internal/domain/media"
	egressapp "m365-copilot2xapi/backend/internal/application/egress"
	"m365-copilot2xapi/backend/internal/infra/provider"

	"github.com/gorilla/websocket"
)

// ErrMediaNotSupported indicates that M365 Copilot does not support media
// generation. The gateway exposes the OpenAI-compatible media endpoints for
// API compatibility, but M365 accounts cannot fulfill these requests.
var ErrMediaNotSupported = errors.New("M365 Copilot does not support media generation")

// ImageGenerationInput mirrors the historical image generation request shape.
type ImageGenerationInput struct {
	RequestID      string
	ClientKey      clientkeydomain.Key
	PublicModel    string
	Prompt         string
	Count          int
	Size           string
	AspectRatio    string
	Resolution     string
	Quality        string
	ResponseFormat string
	Streaming      bool
	PartialImages  int
	Method         string
	Path           string
	Headers        http.Header
}

// ImageEditInput mirrors the historical image edit request shape.
type ImageEditInput struct {
	RequestID      string
	ClientKey      clientkeydomain.Key
	PublicModel    string
	Prompt         string
	ImageURLs      []string
	Count          int
	Size           string
	AspectRatio    string
	Resolution     string
	Quality        string
	ResponseFormat string
	Streaming      bool
	PartialImages  int
	Method         string
	Path           string
	Headers        http.Header
}

// VideoInput mirrors the historical video generation request shape.
type VideoInput struct {
	RequestID       string
	ClientKey       clientkeydomain.Key
	PublicModel     string
	Operation       provider.VideoOperation
	Prompt          string
	Duration        int
	AspectRatio     string
	Resolution      string
	ImageURL        string
	ReferenceURLs   []string
	ReferenceAudios []string
	VideoURL        string
	Method          string
	Path            string
	Headers         http.Header
	Credential      account.Credential
}

// VideoInputFileReference is a placeholder for video file references.
type VideoInputFileReference string

// VideoResult is the no-op result returned for video requests.
type VideoResult = media.Job

// TTSInput mirrors the historical text-to-speech request shape.
type TTSInput struct {
	RequestID                string
	ClientKey                clientkeydomain.Key
	PublicModel              string
	Text                     string
	Input                    string
	Voice                    string
	VoiceID                  string
	Language                 string
	OutputFormat             provider.TTSOutputFormat
	Format                   provider.TTSOutputFormat
	Speed                    float64
	OptimizeStreamingLatency int
	TextNormalization        bool
	WithTimestamps           bool
	Method                   string
	Path                     string
	Headers                  http.Header
}

// TTSResult is the no-op result returned for TTS requests.
type TTSResult = Result

// GenerateImage always returns ErrMediaNotSupported for M365.
func (s *Service) GenerateImage(ctx context.Context, input ImageGenerationInput) (*Result, error) {
	return nil, ErrMediaNotSupported
}

// EditImage always returns ErrMediaNotSupported for M365.
func (s *Service) EditImage(ctx context.Context, input ImageEditInput) (*Result, error) {
	return nil, ErrMediaNotSupported
}

// CreateVideo always returns ErrMediaNotSupported for M365.
func (s *Service) CreateVideo(ctx context.Context, input VideoInput) (media.Job, error) {
	return media.Job{}, ErrMediaNotSupported
}

// GetVideo always returns ErrMediaNotSupported for M365.
func (s *Service) GetVideo(ctx context.Context, requestID string, clientKey clientkeydomain.Key) (media.Job, error) {
	return media.Job{}, ErrMediaNotSupported
}

// OpenVideoContent always returns ErrMediaNotSupported for M365.
func (s *Service) OpenVideoContent(ctx context.Context, requestID string, clientKey clientkeydomain.Key) (io.ReadCloser, string, int64, error) {
	return nil, "", 0, ErrMediaNotSupported
}

// SynthesizeSpeech always returns ErrMediaNotSupported for M365.
func (s *Service) SynthesizeSpeech(ctx context.Context, input TTSInput) (*Result, error) {
	return nil, ErrMediaNotSupported
}

// ListTTSVoices always returns ErrMediaNotSupported for M365.
func (s *Service) ListTTSVoices(ctx context.Context, input VoiceListInput) (*Result, error) {
	return nil, ErrMediaNotSupported
}

// GetTTSVoice always returns ErrMediaNotSupported for M365.
func (s *Service) GetTTSVoice(ctx context.Context, input VoiceIDInput) (*Result, error) {
	return nil, ErrMediaNotSupported
}

// TranscribeSpeech always returns ErrMediaNotSupported for M365.
func (s *Service) TranscribeSpeech(ctx context.Context, input STTInput) (*Result, error) {
	return nil, ErrMediaNotSupported
}

// OpenVoiceWebSocket always returns ErrMediaNotSupported for M365.
func (s *Service) OpenVoiceWebSocket(ctx context.Context, input VoiceWebSocketInput) (*VoiceSession, error) {
	return nil, ErrMediaNotSupported
}

// VoiceSession is a placeholder for realtime voice WebSocket sessions.
type VoiceSession struct {
	Conn *websocket.Conn
}

// Finalize is a no-op for M365 (voice WebSocket not supported).
func (s *VoiceSession) Finalize(outcome VoiceWebSocketOutcome) {}

// VoiceInfo is a placeholder for TTS voice metadata.
type VoiceInfo struct{}

// VoiceListInput mirrors the historical voice list request shape.
type VoiceListInput struct {
	RequestID   string
	ClientKey   clientkeydomain.Key
	PublicModel string
	Method      string
	Path        string
	Headers     http.Header
}

// VoiceIDInput mirrors the historical single-voice request shape.
type VoiceIDInput struct {
	RequestID   string
	ClientKey   clientkeydomain.Key
	PublicModel string
	VoiceID     string
	Method      string
	Path        string
	Headers     http.Header
}

// STTInput mirrors the historical speech-to-text request shape.
type STTInput struct {
	RequestID       string
	ClientKey       clientkeydomain.Key
	PublicModel     string
	URL             string
	AudioFormat     string
	SampleRate      string
	Channels        int
	Multichannel    bool
	Language        string
	Format          bool
	ResponseFormat  string
	Diarize         bool
	FillerWords     bool
	KeyTerms        []string
	VADThreshold    *float64
	FileData        []byte
	FileName        string
	FileMIME        string
	Method          string
	Path            string
	Headers         http.Header
}

// VoiceWebSocketInput mirrors the historical realtime voice WebSocket request shape.
type VoiceWebSocketInput struct {
	RequestID   string
	ClientKey   clientkeydomain.Key
	PublicModel string
	Voice       string
	Method      string
	Path        string
	Headers     http.Header
}

// ProbeEgressQuality always returns ErrMediaNotSupported for M365.
func (s *Service) ProbeEgressQuality(ctx context.Context, nodeID uint64, input egressapp.QualityProbeInput) (egressapp.QualityProbeResult, error) {
	return egressapp.QualityProbeResult{}, ErrMediaNotSupported
}

// VoiceWebSocketOutcome is the no-op result for voice WebSocket sessions.
type VoiceWebSocketOutcome struct {
	ErrorCode            string
	UpstreamFailed       bool
	AudioDurationSeconds float64
}
