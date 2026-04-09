// Package state implements the vpn-watchdog state machine.
//
// States:
//
//	HEALTHY   – probes pass; checked every ProbeIntervalHealthy
//	DEGRADED  – consecutive failures ≥ DegradedThreshold; checked faster
//	PROBING   – parallel test of all configs; pick best scorer
//	SWITCHING – applying new config; verify within SwitchVerifyTimeout
package state

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State represents a watchdog state.
type State string

const (
	StateHealthy   State = "HEALTHY"
	StateDegraded  State = "DEGRADED"
	StateProbing   State = "PROBING"
	StateSwitching State = "SWITCHING"
)

// Machine tracks state and failure counters.
type Machine struct {
	mu sync.Mutex

	current          State
	consecutiveFails int
	activeConfigID   string
	lastSwitch       time.Time
	switchAttempts   int

	// Path to persist state across daemon restarts (tmpfs).
	persistPath string
}

type persistedState struct {
	Current        State     `json:"current"`
	ActiveConfigID string    `json:"active_config_id"`
	LastSwitch     time.Time `json:"last_switch"`
}

// New creates a Machine, restoring from persistPath if present.
func New(stateDir string) *Machine {
	m := &Machine{
		current:     StateHealthy,
		persistPath: filepath.Join(stateDir, "state.json"),
	}
	m.load()
	return m
}

func (m *Machine) load() {
	data, err := os.ReadFile(m.persistPath)
	if err != nil {
		return
	}
	var ps persistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		return
	}
	m.current = ps.Current
	m.activeConfigID = ps.ActiveConfigID
	m.lastSwitch = ps.LastSwitch
	slog.Info("state: restored", "state", m.current, "active", m.activeConfigID)
}

func (m *Machine) save() {
	ps := persistedState{
		Current:        m.current,
		ActiveConfigID: m.activeConfigID,
		LastSwitch:     m.lastSwitch,
	}
	data, _ := json.MarshalIndent(ps, "", "  ")
	if err := os.MkdirAll(filepath.Dir(m.persistPath), 0755); err != nil {
		return
	}
	_ = os.WriteFile(m.persistPath, data, 0600)
}

// Current returns the current state (thread-safe).
func (m *Machine) Current() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// ActiveConfigID returns the currently active tunnel config ID.
func (m *Machine) ActiveConfigID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeConfigID
}

// LastSwitch returns the time of the last config switch.
func (m *Machine) LastSwitch() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastSwitch
}

// SwitchAttempts returns the number of consecutive switch attempts.
func (m *Machine) SwitchAttempts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.switchAttempts
}

// ConsecutiveFails returns the consecutive failure count.
func (m *Machine) ConsecutiveFails() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.consecutiveFails
}

// RecordSuccess records a successful probe; may transition back to HEALTHY.
func (m *Machine) RecordSuccess(degradedThreshold int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.consecutiveFails = 0
	if m.current == StateDegraded {
		slog.Info("state: DEGRADED → HEALTHY")
		m.current = StateHealthy
		m.save()
	}
}

// RecordFailure records a probe failure; may advance the state.
// Returns the new state.
func (m *Machine) RecordFailure(degradedThreshold, probingThreshold int) State {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.consecutiveFails++
	slog.Debug("state: probe failure", "consecutive", m.consecutiveFails, "state", m.current)

	switch m.current {
	case StateHealthy:
		if m.consecutiveFails >= degradedThreshold {
			slog.Warn("state: HEALTHY → DEGRADED", "failures", m.consecutiveFails)
			m.current = StateDegraded
			m.save()
		}

	case StateDegraded:
		if m.consecutiveFails >= degradedThreshold+probingThreshold {
			slog.Warn("state: DEGRADED → PROBING", "failures", m.consecutiveFails)
			m.current = StateProbing
			m.consecutiveFails = 0
			m.switchAttempts = 0
			m.save()
		}
	}

	return m.current
}

// EnterSwitching transitions from PROBING to SWITCHING.
func (m *Machine) EnterSwitching(newConfigID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	slog.Info("state: PROBING → SWITCHING", "config", newConfigID)
	m.current = StateSwitching
	m.switchAttempts++
	m.save()
}

// SwitchSucceeded records a successful switch.
func (m *Machine) SwitchSucceeded(configID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	slog.Info("state: SWITCHING → HEALTHY", "config", configID)
	m.current = StateHealthy
	m.activeConfigID = configID
	m.lastSwitch = time.Now()
	m.consecutiveFails = 0
	m.switchAttempts = 0
	m.save()
}

// SwitchFailed records a failed switch; either retries PROBING or gives up.
// Returns true if we should try the next config, false if max attempts reached.
func (m *Machine) SwitchFailed(maxAttempts int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	slog.Warn("state: switch failed", "attempts", m.switchAttempts, "max", maxAttempts)
	if m.switchAttempts < maxAttempts {
		slog.Info("state: SWITCHING → PROBING (retry)")
		m.current = StateProbing
		m.save()
		return true
	}
	slog.Error("state: max switch attempts reached, staying PROBING for backoff")
	m.current = StateProbing
	m.switchAttempts = 0
	m.save()
	return false
}

// SetActiveConfig directly updates the active config (e.g., on startup).
func (m *Machine) SetActiveConfig(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeConfigID = id
	m.save()
}

// InCooldown returns true if we are within the post-switch cooldown period.
func (m *Machine) InCooldown(cooldown time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.lastSwitch.IsZero() && time.Since(m.lastSwitch) < cooldown
}
