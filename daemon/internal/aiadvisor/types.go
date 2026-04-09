package aiadvisor

import (
	"context"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/dpi"
)

// AttemptEntry captures one probe/apply attempt for AI context.
type AttemptEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	ConfigID     string    `json:"config_id"`
	BaseConfigID string    `json:"base_config_id,omitempty"`
	VariantID    string    `json:"variant_id,omitempty"`
	ReasonCode   string    `json:"reason_code,omitempty"`
	BlockType    string    `json:"block_type,omitempty"`
	Profile      string    `json:"profile,omitempty"`
	Stage        string    `json:"stage"` // probe | apply | verify
	Success      bool      `json:"success"`
	ErrorSummary string    `json:"error_summary,omitempty"`
}

// AnalyzeInput is passed to the provider.
type AnalyzeInput struct {
	Detected       dpi.DetectionResult    `json:"detected"`
	RecentAttempts []AttemptEntry         `json:"recent_attempts"`
	BaseConfigs    []*config.TunnelConfig `json:"base_configs"`
}

// Recommendation is provider output.
type Recommendation struct {
	BaseConfigID   string  `json:"base_config_id"`
	Reasoning      string  `json:"reasoning,omitempty"`
	Confidence     float64 `json:"confidence"`
	RetryBudget    int     `json:"retry_budget,omitempty"`
	ExpiresSeconds int     `json:"expires_seconds,omitempty"`

	MTU              *int   `json:"mtu,omitempty"`
	EndpointPort     *int   `json:"endpoint_port,omitempty"`
	VLESSTransport   string `json:"vless_transport,omitempty"`
	VLESSFingerprint string `json:"vless_fingerprint,omitempty"`
	VLESSPath        string `json:"vless_path,omitempty"`
	AWGProfile       string `json:"awg_profile,omitempty"`
}

// Provider returns one recommendation.
type Provider interface {
	Analyze(ctx context.Context, in AnalyzeInput) (Recommendation, error)
}
