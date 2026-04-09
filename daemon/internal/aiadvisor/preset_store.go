package aiadvisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Preset struct {
	BaseConfigID   string         `json:"base_config_id"`
	Recommendation Recommendation `json:"recommendation"`
	CreatedAt      time.Time      `json:"created_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
	SuccessStreak  int            `json:"success_streak"`
	DisabledUntil  time.Time      `json:"disabled_until,omitempty"`
	LastFailure    string         `json:"last_failure,omitempty"`
}

type PresetStore struct {
	mu      sync.Mutex
	path    string
	presets map[string]Preset
}

func NewPresetStore(path string) *PresetStore {
	ps := &PresetStore{path: path, presets: map[string]Preset{}}
	ps.load()
	return ps
}

func (ps *PresetStore) GetActive(baseID string, now time.Time) (Preset, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.presets[baseID]
	if !ok {
		return Preset{}, false
	}
	if now.After(p.ExpiresAt) || (!p.DisabledUntil.IsZero() && now.Before(p.DisabledUntil)) {
		return Preset{}, false
	}
	return p, true
}

func (ps *PresetStore) SavePreset(baseID string, rec Recommendation, ttl time.Duration) {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.presets[baseID] = Preset{
		BaseConfigID:   baseID,
		Recommendation: rec,
		CreatedAt:      time.Now(),
		ExpiresAt:      time.Now().Add(ttl),
	}
	ps.saveLocked()
}

func (ps *PresetStore) MarkSuccess(baseID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.presets[baseID]
	if !ok {
		return
	}
	p.SuccessStreak++
	p.LastFailure = ""
	ps.presets[baseID] = p
	ps.saveLocked()
}

func (ps *PresetStore) MarkFailure(baseID, reason string, cooldown time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.presets[baseID]
	if !ok {
		return
	}
	p.LastFailure = reason
	if cooldown > 0 {
		p.DisabledUntil = time.Now().Add(cooldown)
	}
	ps.presets[baseID] = p
	ps.saveLocked()
}

func (ps *PresetStore) load() {
	b, err := os.ReadFile(ps.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &ps.presets)
}

func (ps *PresetStore) saveLocked() {
	_ = os.MkdirAll(filepath.Dir(ps.path), 0750)
	b, err := json.MarshalIndent(ps.presets, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(ps.path, b, 0640)
}
