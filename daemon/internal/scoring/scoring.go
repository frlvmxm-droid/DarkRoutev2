// Package scoring ranks tunnel configurations by historical performance.
package scoring

import (
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/probe"
)

const ewmaAlpha = 0.3 // exponential moving average smoothing factor

// Session records one tunnel activation attempt.
type Session struct {
	ConfigID  string        `json:"config_id"`
	StartedAt time.Time     `json:"started_at"`
	Success   bool          `json:"success"`
	RTT       time.Duration `json:"rtt_ns"`
	Loss      float64       `json:"loss"`
}

// Entry holds scoring data for one configuration ID.
type Entry struct {
	ConfigID       string    `json:"config_id"`
	EWMARTZ        float64   `json:"ewma_rtt_ms"`            // Exponential moving avg RTT in ms
	EWMALoss       float64   `json:"ewma_loss"`              // Exponential moving avg packet loss
	SessionSuccess float64   `json:"session_success_weight"` // EWMA of bool success
	LastUsed       time.Time `json:"last_used"`
	Sessions       int       `json:"sessions_total"`
	// DPI-specific tracking.
	DPIBypassSuccess float64 `json:"dpi_bypass_success"` // EWMA of DPI-bypass successes (0–1)
	LastBlockType    string  `json:"last_block_type"`    // last observed block type
}

// Score computes a comparable score: higher is better.
//
//	score = (1 - loss) × 1000 / rttMs × sessionSuccessWeight × dpiBonus
//
// dpiBonus gives a 50% uplift to configs that historically bypass DPI well.
func (e *Entry) Score() float64 {
	rttMs := e.EWMARTZ
	if rttMs <= 0 {
		rttMs = 1000 // unknown → pessimistic
	}
	loss := math.Min(e.EWMALoss, 1.0)
	sw := e.SessionSuccess
	if sw == 0 {
		sw = 0.5 // neutral for new configs
	}
	// dpiBonus: range 1.0 (no DPI history) to 1.5 (always bypasses DPI).
	dpiBonus := 1.0 + e.DPIBypassSuccess*0.5
	return (1 - loss) * 1000 / rttMs * sw * dpiBonus
}

// CompositeScore computes a multi-criteria score for tunnel selection:
//  1. availability under DPI/TSPU pressure,
//  2. performance (RTT/loss),
//  3. security profile of the chosen protocol,
//  4. reliability from historical session outcomes.
//
// Returned value is 0..100 (higher is better).
func (e *Entry) CompositeScore(tc *config.TunnelConfig) float64 {
	rttMs := e.EWMARTZ
	if rttMs <= 0 {
		rttMs = 1000
	}
	loss := clamp01(e.EWMALoss)
	session := e.SessionSuccess
	if session <= 0 {
		session = 0.5
	}

	// Performance: fast RTT and low loss.
	// RTT is normalized with soft saturation so very high RTT is penalized.
	rttNorm := 1.0 / (1.0 + rttMs/120.0)
	performance := clamp01((1.0 - loss) * rttNorm)

	// Availability for censorship conditions: prioritize proven DPI bypass.
	dpi := e.DPIBypassSuccess
	if e.LastBlockType == "" && dpi == 0 {
		dpi = 0.5 // neutral when no DPI history yet
	}
	availability := clamp01(0.6*dpi + 0.4*session)

	// Reliability from session history.
	sampleConf := 1.0 - math.Exp(-float64(e.Sessions)/6.0) // -> 1 as samples grow
	reliability := clamp01(session*sampleConf + 0.5*(1.0-sampleConf))

	// Security profile depends on protocol/settings.
	security := protocolSecurity(tc)

	// Weighted aggregate.
	final := 100.0 * (0.40*availability + 0.35*performance + 0.15*security + 0.10*reliability)
	return final
}

// RecordDPIResult updates the DPI bypass EWMA. succeeded=true means this
// config successfully passed traffic when DPI was detected.
func (db *DB) RecordDPIResult(configID string, blockType string, succeeded bool) {
	db.mu.Lock()
	e := db.entry(configID)
	e.LastBlockType = blockType
	val := 0.0
	if succeeded {
		val = 1.0
	}
	e.DPIBypassSuccess = ewmaAlpha*val + (1-ewmaAlpha)*e.DPIBypassSuccess
	db.mu.Unlock()
	db.save()
}

// DB is the in-memory and on-disk scoring database.
type DB struct {
	mu      sync.Mutex
	entries map[string]*Entry
	path    string
}

// NewDB creates a DB backed by the JSON file at path.
func NewDB(stateDir string) *DB {
	db := &DB{
		entries: make(map[string]*Entry),
		path:    filepath.Join(stateDir, "scores.json"),
	}
	db.load()
	return db
}

func (db *DB) load() {
	data, err := os.ReadFile(db.path)
	if err != nil {
		return // first run
	}
	var entries []*Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("scoring: failed to parse scores.json", "err", err)
		return
	}
	for _, e := range entries {
		db.entries[e.ConfigID] = e
	}
}

func (db *DB) save() {
	db.mu.Lock()
	list := make([]*Entry, 0, len(db.entries))
	for _, e := range db.entries {
		list = append(list, e)
	}
	db.mu.Unlock()

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(db.path), 0755); err != nil {
		return
	}
	_ = os.WriteFile(db.path, data, 0600)
}

// entry returns (or creates) the Entry for configID; caller holds mu.
func (db *DB) entry(configID string) *Entry {
	if e, ok := db.entries[configID]; ok {
		return e
	}
	e := &Entry{ConfigID: configID, SessionSuccess: 0.5}
	db.entries[configID] = e
	return e
}

// entryOrDefault returns the Entry for configID if it exists, or a neutral
// default without persisting it. Used by Rank() to avoid polluting the DB
// with transient DPI variant IDs.
func (db *DB) entryOrDefault(configID string) *Entry {
	if e, ok := db.entries[configID]; ok {
		return e
	}
	return &Entry{ConfigID: configID, SessionSuccess: 0.5}
}

// RecordProbeResult updates EWMA metrics with a probe result.
func (db *DB) RecordProbeResult(configID string, res probe.AggResult) {
	db.mu.Lock()
	e := db.entry(configID)
	rttMs := float64(res.AvgRTT.Milliseconds())
	if rttMs <= 0 {
		rttMs = 5000
	}
	if e.EWMARTZ == 0 {
		e.EWMARTZ = rttMs
		e.EWMALoss = res.PacketLoss
	} else {
		e.EWMARTZ = ewmaAlpha*rttMs + (1-ewmaAlpha)*e.EWMARTZ
		e.EWMALoss = ewmaAlpha*res.PacketLoss + (1-ewmaAlpha)*e.EWMALoss
	}
	db.mu.Unlock()
	db.save()
}

// RecordSession records a switch attempt outcome.
func (db *DB) RecordSession(sess Session) {
	db.mu.Lock()
	e := db.entry(sess.ConfigID)
	success := 0.0
	if sess.Success {
		success = 1.0
	}
	e.SessionSuccess = ewmaAlpha*success + (1-ewmaAlpha)*e.SessionSuccess
	e.Sessions++
	e.LastUsed = sess.StartedAt
	db.mu.Unlock()
	db.save()
}

// Rank returns configs sorted by descending Score, filtering to those in the provided set.
func (db *DB) Rank(configs []*config.TunnelConfig) []*config.TunnelConfig {
	db.mu.Lock()
	defer db.mu.Unlock()

	type scored struct {
		tc    *config.TunnelConfig
		score float64
	}
	items := make([]scored, 0, len(configs))
	for _, tc := range configs {
		// Use entryOrDefault to avoid creating phantom DB entries for
		// transient DPI variant configs that may never be used again.
		e := db.entryOrDefault(tc.ID)
		items = append(items, scored{tc: tc, score: e.CompositeScore(tc)})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].score > items[j].score
	})
	out := make([]*config.TunnelConfig, len(items))
	for i, it := range items {
		out[i] = it.tc
	}
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func protocolSecurity(tc *config.TunnelConfig) float64 {
	if tc == nil {
		return 0.6
	}
	switch tc.Protocol {
	case config.ProtocolAmneziaWG:
		return 0.92
	case config.ProtocolWireGuard:
		return 0.88
	case config.ProtocolVLESS:
		if tc.VLESS == nil {
			return 0.70
		}
		switch tc.VLESS.Security {
		case "reality":
			return 0.95
		case "tls":
			return 0.85
		default:
			return 0.65
		}
	default:
		return 0.6
	}
}

// GetEntry returns a copy of the scoring Entry for a config ID.
func (db *DB) GetEntry(configID string) Entry {
	db.mu.Lock()
	defer db.mu.Unlock()
	e := db.entry(configID)
	return *e
}

// AllEntries returns all known entries for dashboard display.
func (db *DB) AllEntries() []Entry {
	db.mu.Lock()
	defer db.mu.Unlock()
	out := make([]Entry, 0, len(db.entries))
	for _, e := range db.entries {
		out = append(out, *e)
	}
	return out
}
