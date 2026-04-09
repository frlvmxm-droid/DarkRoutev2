// Package watchdog implements the main event loop of the vpn-watchdog daemon.
//
// It ticks at configurable intervals, delegates probing to the probe engine,
// maintains state via the state machine, and triggers the switch engine when
// connectivity degrades. DPI detection runs before each PROBING phase to
// guide the selection of obfuscation variants.
package watchdog

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/aiadvisor"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/dpi"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/probe"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/scoring"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/state"
	switcher "github.com/frlvmxm-droid/darkroute/daemon/internal/switch"
)

// Watchdog is the main daemon struct.
type Watchdog struct {
	cfg      config.DaemonConfig
	store    *config.Store
	probeEng *probe.Engine
	db       *scoring.DB
	machine  *state.Machine
	eng      *switcher.Engine
	// DPI subsystem.
	learnedDB *dpi.LearnedDB

	// mu protects fields accessed from both the tick loop and the HTTP API goroutine.
	mu                  sync.Mutex
	lastDPIResult       dpi.DetectionResult
	lastStateWasProbing bool
}

// New creates a Watchdog with all subsystems initialized.
func New(cfg config.DaemonConfig) *Watchdog {
	if err := os.MkdirAll(cfg.StateDir, 0755); err != nil {
		slog.Warn("watchdog: could not create state dir", "err", err)
	}

	store := config.NewStore(cfg)
	db := scoring.NewDB(cfg.StateDir)
	machine := state.New(cfg.StateDir)
	probeEng := probe.New(cfg)
	eng := switcher.New(cfg, db, machine)
	learnedDB := dpi.NewLearnedDB(cfg.StateDir)

	// Restore last known DPI detection result so the status API returns
	// meaningful data immediately after restart.
	var lastDPI dpi.DetectionResult
	if r, ok := dpi.LoadDetection(cfg.StateDir); ok {
		slog.Info("watchdog: restored last DPI detection", "block_type", r.BlockType.String())
		lastDPI = r
	}

	return &Watchdog{
		cfg:           cfg,
		store:         store,
		probeEng:      probeEng,
		db:            db,
		machine:       machine,
		eng:           eng,
		learnedDB:     learnedDB,
		lastDPIResult: lastDPI,
	}
}

// Run starts the watchdog loop, blocking until ctx is cancelled or signal received.
func (w *Watchdog) Run(ctx context.Context) error {
	slog.Info("watchdog: starting",
		"state", w.machine.Current(),
		"active", w.machine.ActiveConfigID(),
		"dpi_auto_tune", w.cfg.DPIAutoTune,
	)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	ticker := time.NewTicker(w.probeInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("watchdog: context cancelled, shutting down")
			return ctx.Err()

		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				slog.Info("watchdog: SIGHUP received, reloading config")
				w.reload()
			case syscall.SIGTERM, syscall.SIGINT:
				slog.Info("watchdog: signal received, shutting down", "signal", sig)
				return nil
			}

		case <-ticker.C:
			w.tick(ctx)
			ticker.Reset(w.probeInterval())
		}
	}
}

func (w *Watchdog) tick(ctx context.Context) {
	st := w.machine.Current()
	slog.Debug("watchdog: tick", "state", st)

	switch st {
	case state.StateHealthy, state.StateDegraded:
		w.doProbe(ctx)

	case state.StateProbing:
		w.doSwitch(ctx)

	case state.StateSwitching:
		slog.Warn("watchdog: unexpected tick in SWITCHING state")
	}
}

// doProbe runs probes against all targets and updates the state machine.
func (w *Watchdog) doProbe(ctx context.Context) {
	if w.machine.Current() == state.StateHealthy && w.machine.InCooldown(w.cfg.PostSwitchCooldown) {
		slog.Debug("watchdog: in cooldown, skipping probe")
		return
	}

	res := w.probeEng.ProbeAll(ctx, 0)
	slog.Debug("watchdog: probe result",
		"success", res.Success,
		"rtt", res.AvgRTT,
		"loss", res.PacketLoss,
		"err_type", res.DominantErrType.String())

	if res.Success {
		w.machine.RecordSuccess(w.cfg.DegradedThreshold)
		w.mu.Lock()
		w.lastStateWasProbing = false
		w.mu.Unlock()
	} else {
		newState := w.machine.RecordFailure(w.cfg.DegradedThreshold, w.cfg.ProbingThreshold)
		slog.Info("watchdog: probe failed",
			"new_state", newState,
			"consecutive_fails", w.machine.ConsecutiveFails(),
			"err_type", res.DominantErrType.String())
	}
}

// doSwitch runs DPI detection, generates DPI-aware candidate pool, and
// applies the best available config.
func (w *Watchdog) doSwitch(ctx context.Context) {
	candidates, err := w.store.LoadAll()
	if err != nil {
		slog.Error("watchdog: failed to load configs", "err", err)
		return
	}
	if len(candidates) == 0 {
		slog.Warn("watchdog: no enabled configs to try")
		return
	}

	// ── DPI Detection ────────────────────────────────────────────────────
	// Run only once per PROBING entry (not on every tick).
	w.mu.Lock()
	needsDPI := !w.lastStateWasProbing
	w.mu.Unlock()

	if needsDPI {
		slog.Info("watchdog: running DPI detection...")
		detCtx, detCancel := context.WithTimeout(ctx, 20*time.Second)
		result := dpi.Detect(detCtx, w.cfg.ProbeTargets, 0)
		detCancel()

		w.mu.Lock()
		w.lastDPIResult = result
		w.lastStateWasProbing = true
		w.mu.Unlock()

		dpi.SaveDetection(w.cfg.StateDir, result)
		slog.Info("watchdog: DPI detection complete",
			"block_type", result.BlockType.String(),
			"evidence", result.Evidence)
	}

	w.mu.Lock()
	dpiResult := w.lastDPIResult
	w.mu.Unlock()

	slog.Info("watchdog: entering PROBING",
		"candidates", len(candidates),
		"dpi_hint", dpiResult.BlockType.String(),
		"dpi_reason", dpiResult.ReasonCode,
		"dpi_confidence", dpiResult.Confidence,
		"learned_variants", w.learnedDB.Count())

	dpiHint := dpiResult.BlockType
	// Low-confidence classifications should not aggressively bias variant generation.
	if dpiResult.Confidence < 0.6 {
		dpiHint = dpi.BlockNone
	}

	activeID := w.machine.ActiveConfigID()
	appliedID, err := w.eng.ProbeAndSwitch(
		ctx, candidates, activeID,
		dpiHint, dpiResult.ReasonCode, dpiResult.Confidence, w.learnedDB,
	)
	if err != nil {
		slog.Error("watchdog: ProbeAndSwitch error", "err", err)
		return
	}
	if appliedID == "" {
		slog.Warn("watchdog: no config applied; will retry on next tick")
		// Don't block the event loop — the ticker will retry after probeInterval().
	} else {
		// Reset for next PROBING entry.
		w.mu.Lock()
		w.lastStateWasProbing = false
		w.mu.Unlock()
	}
}

// reload re-reads the daemon config from UCI.
func (w *Watchdog) reload() {
	cfg := config.LoadDaemonConfigFromUCI()
	w.cfg = cfg
	w.store = config.NewStore(cfg)
	w.probeEng = probe.New(cfg)
	w.eng = switcher.New(cfg, w.db, w.machine)
	slog.Info("watchdog: config reloaded", "dpi_auto_tune", cfg.DPIAutoTune)
}

// probeInterval returns the ticker interval for the current state.
func (w *Watchdog) probeInterval() time.Duration {
	switch w.machine.Current() {
	case state.StateDegraded:
		return w.cfg.ProbeIntervalDegraded
	case state.StateProbing, state.StateSwitching:
		return 5 * time.Second
	default:
		return w.cfg.ProbeIntervalHealthy
	}
}

// Status returns a snapshot for the LuCI API endpoint.
func (w *Watchdog) Status() Status {
	w.mu.Lock()
	dpiResult := w.lastDPIResult
	w.mu.Unlock()

	return Status{
		State:          string(w.machine.Current()),
		ActiveConfigID: w.machine.ActiveConfigID(),
		LastSwitch:     w.machine.LastSwitch(),
		ConsecFails:    w.machine.ConsecutiveFails(),
		ScoreEntries:   w.db.AllEntries(),
		DPIBlockType:   dpiResult.BlockType.String(),
		DPIReasonCode:  dpiResult.ReasonCode,
		DPIConfidence:  dpiResult.Confidence,
		DPIEvidence:    dpiResult.Evidence,
		DPIStages:      dpiResult.StageResults,
		DPITestedAt:    dpiResult.TestedAt,
		LearnedCount:   w.learnedDB.Count(),
		VPNDomains:     w.cfg.VPNDomains,
		VPNIPs:         w.cfg.VPNIPs,
		VPNDomainFiles: w.cfg.VPNDomainFiles,
		AIStatus:       w.eng.AIStatus(),
	}
}

// Status is a JSON-serialisable snapshot of the watchdog status.
type Status struct {
	State          string          `json:"state"`
	ActiveConfigID string          `json:"active_config_id"`
	LastSwitch     time.Time       `json:"last_switch"`
	ConsecFails    int             `json:"consecutive_failures"`
	ScoreEntries   []scoring.Entry `json:"scores"`
	// DPI fields.
	DPIBlockType   string            `json:"dpi_block_type"`
	DPIReasonCode  string            `json:"dpi_reason_code"`
	DPIConfidence  float64           `json:"dpi_confidence"`
	DPIEvidence    []string          `json:"dpi_evidence"`
	DPIStages      []dpi.StageResult `json:"dpi_stages,omitempty"`
	DPITestedAt    time.Time         `json:"dpi_tested_at"`
	LearnedCount   int               `json:"learned_variants_count"`
	VPNDomains     []string          `json:"vpn_domains,omitempty"`
	VPNIPs         []string          `json:"vpn_ips,omitempty"`
	VPNDomainFiles []string          `json:"vpn_domain_files,omitempty"`
	AIStatus       aiadvisor.Status  `json:"ai_status"`
}
