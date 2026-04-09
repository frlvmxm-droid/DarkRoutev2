package scoring

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/probe"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	return NewDB(t.TempDir())
}

// TestNewEntryScore: a brand-new entry should have a positive finite score.
func TestNewEntryScore(t *testing.T) {
	e := &Entry{ConfigID: "x", SessionSuccess: 0.5}
	s := e.Score()
	if s <= 0 || math.IsInf(s, 0) || math.IsNaN(s) {
		t.Fatalf("want finite positive score, got %f", s)
	}
}

// TestHighRTTPenalty: higher RTT should give a lower score.
func TestHighRTTPenalty(t *testing.T) {
	fast := &Entry{ConfigID: "fast", EWMARTZ: 50, EWMALoss: 0, SessionSuccess: 1}
	slow := &Entry{ConfigID: "slow", EWMARTZ: 500, EWMALoss: 0, SessionSuccess: 1}
	if fast.Score() <= slow.Score() {
		t.Fatalf("fast(%f) should score higher than slow(%f)", fast.Score(), slow.Score())
	}
}

// TestHighLossPenalty: higher loss should give a lower score.
func TestHighLossPenalty(t *testing.T) {
	clean := &Entry{ConfigID: "clean", EWMARTZ: 100, EWMALoss: 0, SessionSuccess: 1}
	lossy := &Entry{ConfigID: "lossy", EWMARTZ: 100, EWMALoss: 0.5, SessionSuccess: 1}
	if clean.Score() <= lossy.Score() {
		t.Fatalf("clean(%f) should score higher than lossy(%f)", clean.Score(), lossy.Score())
	}
}

// TestDPIBonus: configs with higher DPIBypassSuccess get higher scores.
func TestDPIBonus(t *testing.T) {
	base := &Entry{ConfigID: "base", EWMARTZ: 100, EWMALoss: 0, SessionSuccess: 1, DPIBypassSuccess: 0}
	dpi := &Entry{ConfigID: "dpi", EWMARTZ: 100, EWMALoss: 0, SessionSuccess: 1, DPIBypassSuccess: 1}
	if dpi.Score() <= base.Score() {
		t.Fatalf("dpi(%f) should score higher than base(%f)", dpi.Score(), base.Score())
	}
}

func TestCompositeScoreSecurityPreference(t *testing.T) {
	base := &Entry{
		ConfigID:         "base",
		EWMARTZ:          100,
		EWMALoss:         0.02,
		SessionSuccess:   0.9,
		DPIBypassSuccess: 0.8,
		LastBlockType:    "tls_dpi",
		Sessions:         10,
	}
	wg := &config.TunnelConfig{ID: "wg", Protocol: config.ProtocolWireGuard, WireGuard: &config.WireGuardConfig{}}
	vlessReality := &config.TunnelConfig{
		ID:       "vr",
		Protocol: config.ProtocolVLESS,
		VLESS:    &config.VLESSConfig{Security: "reality"},
	}
	if base.CompositeScore(vlessReality) <= base.CompositeScore(wg) {
		t.Fatalf("expected reality profile to have higher security-weighted score")
	}
}

func TestCompositeScoreDPIAvailabilityPenalty(t *testing.T) {
	tc := &config.TunnelConfig{ID: "x", Protocol: config.ProtocolWireGuard, WireGuard: &config.WireGuardConfig{}}
	good := &Entry{
		ConfigID:         "good",
		EWMARTZ:          80,
		EWMALoss:         0.01,
		SessionSuccess:   0.8,
		DPIBypassSuccess: 0.9,
		LastBlockType:    "protocol_dpi",
		Sessions:         10,
	}
	bad := &Entry{
		ConfigID:         "bad",
		EWMARTZ:          80,
		EWMALoss:         0.01,
		SessionSuccess:   0.8,
		DPIBypassSuccess: 0.1,
		LastBlockType:    "protocol_dpi",
		Sessions:         10,
	}
	if good.CompositeScore(tc) <= bad.CompositeScore(tc) {
		t.Fatalf("expected high-DPI-availability entry to score higher")
	}
}

// TestEWMAUpdate: RecordProbeResult applies EWMA smoothing.
func TestEWMAUpdate(t *testing.T) {
	db := newTestDB(t)
	r := probe.AggResult{
		Success:    true,
		AvgRTT:     100 * time.Millisecond,
		PacketLoss: 0.1,
	}
	db.RecordProbeResult("cfg-a", r)
	e := db.GetEntry("cfg-a")
	// First update: EWMA is set directly.
	if e.EWMARTZ != 100 {
		t.Fatalf("first RTT EWMA want 100, got %f", e.EWMARTZ)
	}

	// Second update with 200ms: should be 0.3*200 + 0.7*100 = 130.
	r2 := probe.AggResult{Success: true, AvgRTT: 200 * time.Millisecond, PacketLoss: 0}
	db.RecordProbeResult("cfg-a", r2)
	e2 := db.GetEntry("cfg-a")
	want := ewmaAlpha*200 + (1-ewmaAlpha)*100
	if math.Abs(e2.EWMARTZ-want) > 0.001 {
		t.Fatalf("second RTT EWMA want %.3f, got %.3f", want, e2.EWMARTZ)
	}
}

// TestRankOrdering: Rank returns higher-scoring configs first.
func TestRankOrdering(t *testing.T) {
	db := newTestDB(t)
	// seed: cfg-good has fast RTT, cfg-bad has slow RTT + loss.
	db.RecordProbeResult("cfg-good", probe.AggResult{
		Success: true, AvgRTT: 50 * time.Millisecond, PacketLoss: 0,
	})
	db.RecordProbeResult("cfg-bad", probe.AggResult{
		Success: true, AvgRTT: 500 * time.Millisecond, PacketLoss: 0.5,
	})

	good := &config.TunnelConfig{ID: "cfg-good", Protocol: config.ProtocolWireGuard, WireGuard: &config.WireGuardConfig{}}
	bad := &config.TunnelConfig{ID: "cfg-bad", Protocol: config.ProtocolWireGuard, WireGuard: &config.WireGuardConfig{}}
	ranked := db.Rank([]*config.TunnelConfig{bad, good})
	if ranked[0].ID != "cfg-good" {
		t.Fatalf("want cfg-good first, got %s", ranked[0].ID)
	}
}

// TestRecordSession: session success updates SessionSuccess EWMA.
func TestRecordSession(t *testing.T) {
	db := newTestDB(t)
	db.RecordSession(Session{ConfigID: "cfg-x", Success: true, StartedAt: time.Now()})
	e := db.GetEntry("cfg-x")
	// Starting at 0.5, one success: 0.3*1 + 0.7*0.5 = 0.65
	want := ewmaAlpha*1.0 + (1-ewmaAlpha)*0.5
	if math.Abs(e.SessionSuccess-want) > 0.001 {
		t.Fatalf("session success want %.3f, got %.3f", want, e.SessionSuccess)
	}
}

// TestDPIBypassEWMA: RecordDPIResult updates DPIBypassSuccess EWMA.
func TestDPIBypassEWMA(t *testing.T) {
	db := newTestDB(t)
	db.RecordDPIResult("cfg-y", "tls", true)
	e := db.GetEntry("cfg-y")
	// Starting at 0 (new entry), success: 0.3*1 + 0.7*0 = 0.3
	want := ewmaAlpha * 1.0
	if math.Abs(e.DPIBypassSuccess-want) > 0.001 {
		t.Fatalf("DPIBypassSuccess want %.3f, got %.3f", want, e.DPIBypassSuccess)
	}
	if e.LastBlockType != "tls" {
		t.Fatalf("want LastBlockType=tls, got %s", e.LastBlockType)
	}
}

// TestScoresFileMode: scores.json has mode 0600.
func TestScoresFileMode(t *testing.T) {
	dir := t.TempDir()
	db := NewDB(dir)
	db.RecordSession(Session{ConfigID: "cfg-z", Success: true, StartedAt: time.Now()})

	path := filepath.Join(dir, "scores.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("scores.json not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("want mode 0600, got %04o", perm)
	}
}
