package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"

	"m365-copilot2xapi/backend/internal/domain/account"
	clientkeydomain "m365-copilot2xapi/backend/internal/domain/clientkey"
	"m365-copilot2xapi/backend/internal/domain/media"
	"m365-copilot2xapi/backend/internal/infra/provider"
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
	OutputFormat             string
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
type TTSResult struct{}

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
func (s *Service) SynthesizeSpeech(ctx context.Context, input TTSInput) (TTSResult, error) {
	return TTSResult{}, ErrMediaNotSupported
}
