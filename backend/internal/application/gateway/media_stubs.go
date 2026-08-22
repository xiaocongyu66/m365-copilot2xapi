package gateway

import (
	"context"
	"errors"
	"net/http"

	"m365-copilot2xapi/backend/internal/domain/account"
)

// ErrMediaNotSupported indicates that M365 Copilot does not support media
// generation. The gateway exposes the OpenAI-compatible media endpoints for
// API compatibility, but M365 accounts cannot fulfill these requests.
var ErrMediaNotSupported = errors.New("M365 Copilot does not support media generation")

// ImageGenerationInput mirrors the historical image generation request shape.
type ImageGenerationInput struct {
	RequestID      string
	ClientKey      uint64
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
	ClientKey      uint64
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

// ImageResult is the no-op result returned for image requests.
type ImageResult struct{}

// VideoInput mirrors the historical video generation request shape.
type VideoInput struct {
	RequestID   string
	ClientKey   uint64
	PublicModel string
	Prompt      string
	Duration    int
	AspectRatio string
	Resolution  string
	Method      string
	Path        string
	Headers     http.Header
	Credential  account.Credential
}

// VideoInputFileReference is a placeholder for video file references.
type VideoInputFileReference string

// VideoResult is the no-op result returned for video requests.
type VideoResult struct{}

// TTSInput mirrors the historical text-to-speech request shape.
type TTSInput struct {
	RequestID   string
	ClientKey   uint64
	PublicModel string
	Input       string
	Voice       string
	Format      string
	Method      string
	Path        string
	Headers     http.Header
}

// TTSResult is the no-op result returned for TTS requests.
type TTSResult struct{}

// GenerateImage always returns ErrMediaNotSupported for M365.
func (s *Service) GenerateImage(ctx context.Context, input ImageGenerationInput) (ImageResult, error) {
	return ImageResult{}, ErrMediaNotSupported
}

// EditImage always returns ErrMediaNotSupported for M365.
func (s *Service) EditImage(ctx context.Context, input ImageEditInput) (ImageResult, error) {
	return ImageResult{}, ErrMediaNotSupported
}

// CreateVideo always returns ErrMediaNotSupported for M365.
func (s *Service) CreateVideo(ctx context.Context, input VideoInput) (VideoResult, error) {
	return VideoResult{}, ErrMediaNotSupported
}

// GetVideo always returns ErrMediaNotSupported for M365.
func (s *Service) GetVideo(ctx context.Context, requestID string, clientKey uint64) (VideoResult, error) {
	return VideoResult{}, ErrMediaNotSupported
}

// OpenVideoContent always returns ErrMediaNotSupported for M365.
func (s *Service) OpenVideoContent(ctx context.Context, requestID string, clientKey uint64) ([]byte, string, int64, error) {
	return nil, "", 0, ErrMediaNotSupported
}
