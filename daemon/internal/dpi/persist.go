package dpi

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

// LearnedEntry records a DPI-variant configuration that has successfully
// established a working connection in a past session.
type LearnedEntry struct {
	BaseConfigID   string    `json:"base_config_id"`
	VariantSuffix  string    `json:"variant_suffix"`   // e.g. "dpi:fp:chrome"
	EffectiveMTU   int       `json:"effective_mtu,omitempty"`
	EffectiveFP    string    `json:"effective_fingerprint,omitempty"`
	EffectiveTrans string    `json:"effective_transport,omitempty"`
	AWGProfile     string    `json:"awg_profile,omitempty"`
	SuccessCount   int       `json:"success_count"`
	LastSuccess    time.Time `json:"last_success"`
}

// LearnedDB persists DPI-bypass knowledge across daemon restarts.
// Runtime state is in {stateDir}/dpi_learned.json (tmpfs).
// Persistent copy is saved to /etc/vpn-watchdog/dpi_learned.json.
type LearnedDB struct {
	mu       sync.Mutex
	entries  map[string]*LearnedEntry // key: variant config ID
	stateDir string
	persDir  string // /etc/vpn-watchdog (persistent across reboots)
}

const persistentLearnedPath = "/etc/vpn-watchdog/dpi_learned.json"

// NewLearnedDB creates a LearnedDB and loads existing entries from disk.
func NewLearnedDB(stateDir string) *LearnedDB {
	db := &LearnedDB{
		entries:  make(map[string]*LearnedEntry),
		stateDir: stateDir,
		persDir:  "/etc/vpn-watchdog",
	}
	db.load()
	return db
}

// RecordSuccess records that a DPI variant successfully established connectivity.
// It increments the success counter and saves both the runtime and persistent copies.
func (db *LearnedDB) RecordSuccess(variant *config.TunnelConfig) {
	if !variant.IsVariant {
		return
	}
	db.mu.Lock()
	e, ok := db.entries[variant.ID]
	if !ok {
		e = &LearnedEntry{
			BaseConfigID:  variant.BaseConfigID,
			VariantSuffix: extractVariantSuffix(variant.ID, variant.BaseConfigID),
		}
		db.entries[variant.ID] = e
	}
	e.SuccessCount++
	e.LastSuccess = time.Now()
	// Capture effective params.
	if variant.MTU > 0 {
		e.EffectiveMTU = variant.MTU
	}
	if variant.VLESS != nil {
		e.EffectiveFP = variant.VLESS.Fingerprint
		e.EffectiveTrans = variant.VLESS.Transport
	}
	if variant.AmneziaWG != nil {
		e.AWGProfile = extractAWGProfile(variant.ID)
	}
	db.mu.Unlock()

	db.save()
	slog.Info("dpi: learned successful variant", "variant", variant.ID, "successes", e.SuccessCount)
}

// GetCandidates returns TunnelConfig objects reconstructed from learned entries
// for the given base config ID, sorted by success count descending.
// These are injected into the PROBING candidate pool with high priority.
func (db *LearnedDB) GetCandidates(baseID string, baseConfig *config.TunnelConfig) []*config.TunnelConfig {
	db.mu.Lock()
	defer db.mu.Unlock()

	var matched []*LearnedEntry
	for _, e := range db.entries {
		if e.BaseConfigID == baseID && e.SuccessCount > 0 {
			matched = append(matched, e)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].SuccessCount > matched[j].SuccessCount
	})

	var out []*config.TunnelConfig
	for _, e := range matched {
		tc := rebuildVariant(baseConfig, e)
		if tc != nil {
			out = append(out, tc)
		}
	}
	return out
}

// AllEntries returns all learned entries for display in LuCI/API.
func (db *LearnedDB) AllEntries() []LearnedEntry {
	db.mu.Lock()
	defer db.mu.Unlock()
	out := make([]LearnedEntry, 0, len(db.entries))
	for _, e := range db.entries {
		out = append(out, *e)
	}
	return out
}

// Count returns total number of learned entries.
func (db *LearnedDB) Count() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return len(db.entries)
}

// ── Persistence ─────────────────────────────────────────────────────────────

func (db *LearnedDB) load() {
	// Try persistent copy first (survives reboots).
	if db.loadFrom(persistentLearnedPath) {
		return
	}
	// Fall back to tmpfs runtime copy.
	db.loadFrom(filepath.Join(db.stateDir, "dpi_learned.json"))
}

func (db *LearnedDB) loadFrom(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var entries []*LearnedEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("dpi: failed to parse learned DB", "path", path, "err", err)
		return false
	}
	for _, e := range entries {
		db.entries[e.BaseConfigID+":"+e.VariantSuffix] = e
	}
	slog.Info("dpi: loaded learned entries", "path", path, "count", len(entries))
	return true
}

func (db *LearnedDB) save() {
	db.mu.Lock()
	list := make([]*LearnedEntry, 0, len(db.entries))
	for _, e := range db.entries {
		list = append(list, e)
	}
	db.mu.Unlock()

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}

	// Write to tmpfs runtime copy.
	runtimePath := filepath.Join(db.stateDir, "dpi_learned.json")
	_ = os.MkdirAll(db.stateDir, 0755)
	_ = os.WriteFile(runtimePath, data, 0600)

	// Write to persistent copy (survives reboots).
	_ = os.MkdirAll(db.persDir, 0750)
	if err := os.WriteFile(persistentLearnedPath, data, 0640); err != nil {
		slog.Warn("dpi: could not write persistent learned DB", "err", err)
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// extractVariantSuffix strips the base ID prefix from a variant ID.
// e.g. "myconf:dpi:fp:chrome" with base "myconf" → "dpi:fp:chrome"
func extractVariantSuffix(variantID, baseID string) string {
	prefix := baseID + ":"
	if len(variantID) > len(prefix) && variantID[:len(prefix)] == prefix {
		return variantID[len(prefix):]
	}
	return variantID
}

func extractAWGProfile(variantID string) string {
	for _, p := range []string{"mild", "moderate", "aggressive"} {
		if len(variantID) >= len(p) && variantID[len(variantID)-len(p):] == p {
			return p
		}
	}
	return ""
}

// rebuildVariant reconstructs a TunnelConfig from a base config + learned entry.
func rebuildVariant(base *config.TunnelConfig, e *LearnedEntry) *config.TunnelConfig {
	if base == nil {
		return nil
	}

	var v *config.TunnelConfig
	switch base.Protocol {
	case config.ProtocolWireGuard:
		c := *base
		if wg := base.WireGuard; wg != nil {
			wgc := *wg
			c.WireGuard = &wgc
		}
		v = &c
	case config.ProtocolAmneziaWG:
		c := *base
		if awg := base.AmneziaWG; awg != nil {
			awgc := *awg
			c.AmneziaWG = &awgc
		}
		v = &c
	case config.ProtocolVLESS:
		c := *base
		if vl := base.VLESS; vl != nil {
			vlc := *vl
			c.VLESS = &vlc
		}
		v = &c
	default:
		return nil
	}

	v.ID = e.BaseConfigID + ":" + e.VariantSuffix
	v.IsVariant = true
	v.BaseConfigID = e.BaseConfigID

	// Apply learned effective params.
	if e.EffectiveMTU > 0 {
		v.MTU = e.EffectiveMTU
	}
	if v.VLESS != nil {
		if e.EffectiveFP != "" {
			v.VLESS.Fingerprint = e.EffectiveFP
		}
		if e.EffectiveTrans != "" {
			v.VLESS.Transport = e.EffectiveTrans
		}
	}
	if v.AmneziaWG != nil && e.AWGProfile != "" {
		for _, p := range awgProfiles {
			if p.name == e.AWGProfile {
				v.AmneziaWG.JunkPacketCount = p.jc
				v.AmneziaWG.JunkPacketMinSize = p.jmin
				v.AmneziaWG.JunkPacketMaxSize = p.jmax
				v.AmneziaWG.InitPacketJunkSize = p.s1
				v.AmneziaWG.ResponsePacketJunkSize = p.s2
				break
			}
		}
	}

	return v
}
