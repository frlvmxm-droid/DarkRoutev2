package aiadvisor

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/dpi"
)

type Status struct {
	Enabled            bool      `json:"enabled"`
	Provider           string    `json:"provider"`
	LastCallAt         time.Time `json:"last_call_at,omitempty"`
	LastStatus         string    `json:"last_status,omitempty"`
	LastRejectReason   string    `json:"last_reject_reason,omitempty"`
	CallsRemainingHour int       `json:"calls_remaining_hour"`
}

type Manager struct {
	cfg      config.AIAdvisorConfig
	provider Provider
	attempts *AttemptLog
	presets  *PresetStore

	mu            sync.Mutex
	windowStart   time.Time
	callsInWindow int
	status        Status
}

func NewManager(cfg config.DaemonConfig) *Manager {
	m := &Manager{
		cfg:         cfg.AIAdvisor,
		provider:    NewProvider(cfg.AIAdvisor),
		attempts:    NewAttemptLog(cfg.StateDir, 300),
		presets:     NewPresetStore(filepath.Join("/etc/vpn-watchdog", "dpi_ai_presets.json")),
		windowStart: time.Now(),
	}
	m.status = Status{Enabled: cfg.AIAdvisor.Enabled, Provider: cfg.AIAdvisor.Provider, CallsRemainingHour: cfg.AIAdvisor.MaxCallsPerHour}
	return m
}

func (m *Manager) Enabled() bool           { return m.cfg.Enabled && m.cfg.Provider != "disabled" }
func (m *Manager) AttemptLog() *AttemptLog { return m.attempts }
func (m *Manager) Presets() *PresetStore   { return m.presets }

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status
	if m.cfg.MaxCallsPerHour > 0 {
		st.CallsRemainingHour = max(0, m.cfg.MaxCallsPerHour-m.callsInWindow)
	}
	return st
}

func (m *Manager) Recommend(ctx context.Context, detected dpi.DetectionResult, base []*config.TunnelConfig) (*config.TunnelConfig, Recommendation, error) {
	if !m.Enabled() {
		return nil, Recommendation{}, fmt.Errorf("disabled")
	}
	if !m.acquireToken() {
		m.setStatus("rate_limited", "hourly_call_limit")
		return nil, Recommendation{}, fmt.Errorf("rate limited")
	}

	input := AnalyzeInput{Detected: detected, RecentAttempts: m.attempts.Recent(100), BaseConfigs: base}
	reqCtx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()
	rec, err := m.provider.Analyze(reqCtx, input)
	m.mu.Lock()
	m.status.LastCallAt = time.Now()
	m.mu.Unlock()
	if err != nil {
		m.setStatus("provider_error", err.Error())
		return nil, Recommendation{}, err
	}
	if err := ValidateRecommendation(rec, m.cfg.MinConfidence); err != nil {
		m.setStatus("rejected", err.Error())
		return nil, rec, err
	}
	baseCfg := findBaseConfig(base, rec.BaseConfigID)
	if baseCfg == nil {
		err := fmt.Errorf("unknown base config id %q", rec.BaseConfigID)
		m.setStatus("rejected", err.Error())
		return nil, rec, err
	}
	variant, err := BuildVariantFromRecommendation(baseCfg, rec)
	if err != nil {
		m.setStatus("rejected", err.Error())
		return nil, rec, err
	}
	m.setStatus("ok", "")
	slog.Info("aiadvisor: recommendation accepted", "base", rec.BaseConfigID, "confidence", rec.Confidence)
	return variant, rec, nil
}

func (m *Manager) acquireToken() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.MaxCallsPerHour <= 0 {
		return true
	}
	now := time.Now()
	if now.Sub(m.windowStart) >= time.Hour {
		m.windowStart = now
		m.callsInWindow = 0
	}
	if m.callsInWindow >= m.cfg.MaxCallsPerHour {
		return false
	}
	m.callsInWindow++
	return true
}

func (m *Manager) setStatus(status, reject string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.LastStatus = status
	m.status.LastRejectReason = reject
}

func findBaseConfig(c []*config.TunnelConfig, id string) *config.TunnelConfig {
	for _, x := range c {
		if x.ID == id {
			return x
		}
	}
	return nil
}
