// Package switcher implements the Switch Engine: parallel probing of all
// configs and application of the best one, with rollback support.
package switcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/aiadvisor"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/dpi"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/policy"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/probe"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/scoring"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/state"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/strategy"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/tunnel"
)

// Engine probes all candidate configs in parallel, ranks them, and applies the best.
type Engine struct {
	cfg      config.DaemonConfig
	probeEng *probe.Engine
	db       *scoring.DB
	machine  *state.Machine
	policy   *policy.Manager
	ai       *aiadvisor.Manager
}

// New creates a Switch Engine.
func New(cfg config.DaemonConfig, db *scoring.DB, machine *state.Machine) *Engine {
	return &Engine{
		cfg:      cfg,
		probeEng: probe.New(cfg),
		db:       db,
		machine:  machine,
		policy:   policy.New(cfg),
		ai:       aiadvisor.NewManager(cfg),
	}
}

// parallelProbeResult holds the probe result for one candidate config.
type parallelProbeResult struct {
	tc  *config.TunnelConfig
	res probe.AggResult
	err error // error bringing up the temp tunnel
}

// ProbeAndSwitch:
//  1. Injects high-priority learned DPI variants for each base config.
//  2. Generates fresh DPI variants guided by the detected BlockType.
//  3. Brings up each candidate on its own routing table in parallel.
//  4. Probes each through its dedicated table.
//  5. Tears down all temp tunnels.
//  6. Ranks candidates by score (includes DPI-bypass bonus).
//  7. Tries to apply the best one; rolls back and tries the next on failure.
//  8. On success with a DPI variant, persists the effective params.
func (e *Engine) ProbeAndSwitch(
	ctx context.Context,
	baseCandidates []*config.TunnelConfig,
	activeID string,
	dpiHint dpi.BlockType,
	dpiReason string,
	dpiConfidence float64,
	learnedDB *dpi.LearnedDB,
) (appliedID string, err error) {
	if len(baseCandidates) == 0 {
		return "", nil
	}

	// ── Build candidate pool ──────────────────────────────────────────────
	// Priority order: learned variants → base configs → fresh DPI variants.
	var candidates []*config.TunnelConfig

	// 1. Learned variants first (highest priority — they worked before).
	for _, bc := range baseCandidates {
		learned := learnedDB.GetCandidates(bc.ID, bc)
		candidates = append(candidates, learned...)
	}

	// 2. Base configs.
	candidates = append(candidates, baseCandidates...)

	// 3. Fresh DPI variants (only if auto-tune is enabled globally).
	if e.cfg.DPIAutoTune {
		for _, bc := range baseCandidates {
			autoTune := bc.DPI == nil || bc.DPI.AutoTune
			if autoTune {
				variants := dpi.GenerateVariants(bc, dpiHint, e.cfg.DPIMaxVariants)
				variants = e.filterVariantsByProfile(variants, dpiHint, dpiReason, dpiConfidence)
				candidates = append(candidates, variants...)
			}
		}
	}

	// Inject active AI presets (if any) as high-priority candidates.
	if e.ai != nil && e.ai.Enabled() {
		now := time.Now()
		for _, bc := range baseCandidates {
			if p, ok := e.ai.Presets().GetActive(bc.ID, now); ok {
				if v, err := aiadvisor.BuildVariantFromRecommendation(bc, p.Recommendation); err == nil {
					v.ID = bc.ID + ":dpi:ai:preset"
					candidates = append([]*config.TunnelConfig{v}, candidates...)
				}
			}
		}
	}

	slog.Info("switcher: candidate pool", "total", len(candidates),
		"base", len(baseCandidates), "dpi_hint", dpiHint.String())

	// ── Parallel probe ────────────────────────────────────────────────────
	results := e.parallelProbe(ctx, candidates, activeID)

	// Tear down all temporary tunnel instances (they were test-only).
	// Skip the currently active config — it may still be providing connectivity.
	for _, r := range results {
		if r.err == nil && r.tc.ID != activeID {
			mgr, mgrErr := tunnel.ForConfig(r.tc, e.cfg)
			if mgrErr == nil {
				_ = mgr.Down(ctx, r.tc)
			}
		}
	}

	// Record probe results in scoring DB.
	for _, r := range results {
		if r.err == nil {
			e.db.RecordProbeResult(r.tc.ID, r.res)
		}
	}

	// Filter to successful probes and rank.
	var viable []*config.TunnelConfig
	for _, r := range results {
		if r.err == nil && r.res.Success {
			viable = append(viable, r.tc)
		}
	}
	if len(viable) == 0 {
		slog.Warn("switcher: no viable configs found during parallel probe")
		if e.ai != nil && e.ai.Enabled() {
			if v, rec, aiErr := e.ai.Recommend(ctx, dpi.DetectionResult{BlockType: dpiHint, ReasonCode: dpiReason, Confidence: dpiConfidence}, baseCandidates); aiErr == nil && v != nil {
				slog.Info("switcher: retrying with AI recommendation", "variant", v.ID, "base", rec.BaseConfigID)
				aiResults := e.parallelProbe(ctx, []*config.TunnelConfig{v}, activeID)
				if len(aiResults) == 1 && aiResults[0].err == nil && aiResults[0].res.Success {
					viable = []*config.TunnelConfig{v}
					e.ai.Presets().SavePreset(rec.BaseConfigID, rec, e.cfg.AIAdvisor.PresetTTL)
				} else {
					e.ai.Presets().MarkFailure(rec.BaseConfigID, "ai_probe_failed", 30*time.Minute)
				}
			}
		}
		if len(viable) == 0 {
			return "", nil
		}
	}
	viable = e.db.Rank(viable)

	// ── Apply best viable config ──────────────────────────────────────────
	maxAttempts := e.cfg.MaxSwitchAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	for i, tc := range viable {
		if i >= maxAttempts {
			break
		}
		slog.Info("switcher: trying config", "rank", i+1, "config", tc.ID,
			"is_variant", tc.IsVariant)

		e.machine.EnterSwitching(tc.ID)

		start := time.Now()
		applyErr := e.apply(ctx, tc)
		if applyErr != nil {
			slog.Error("switcher: apply failed", "config", tc.ID, "err", applyErr)
			e.db.RecordSession(scoring.Session{
				ConfigID: tc.ID, StartedAt: start, Success: false,
			})
			if dpiHint != dpi.BlockNone {
				e.db.RecordDPIResult(tc.ID, dpiHint.String(), false)
			}
			_ = e.policy.Clear(ctx, fwmarkFor(tc))
			e.machine.SwitchFailed(maxAttempts)
			e.recordAttempt(tc, "apply", false, applyErr.Error(), dpiHint, dpiReason)
			continue
		}

		// Verify connectivity post-apply.
		if !e.verify(ctx) {
			slog.Warn("switcher: verify failed after applying", "config", tc.ID)
			mgr, mgrErr := tunnel.ForConfig(tc, e.cfg)
			if mgrErr == nil {
				_ = mgr.Down(ctx, tc)
			}
			_ = e.policy.Clear(ctx, fwmarkFor(tc))
			e.db.RecordSession(scoring.Session{
				ConfigID: tc.ID, StartedAt: start, Success: false,
			})
			if dpiHint != dpi.BlockNone {
				e.db.RecordDPIResult(tc.ID, dpiHint.String(), false)
			}
			e.machine.SwitchFailed(maxAttempts)
			e.recordAttempt(tc, "verify", false, "verify failed", dpiHint, dpiReason)
			continue
		}

		// ── Success ───────────────────────────────────────────────────────
		e.db.RecordSession(scoring.Session{
			ConfigID: tc.ID, StartedAt: start, Success: true,
		})
		if dpiHint != dpi.BlockNone {
			e.db.RecordDPIResult(tc.ID, dpiHint.String(), true)
		}

		// If this was a DPI variant, persist the effective params so future
		// PROBING phases can use it as a high-priority candidate.
		if tc.IsVariant && learnedDB != nil {
			learnedDB.RecordSuccess(tc)
		}

		e.machine.SwitchSucceeded(tc.ID)
		slog.Info("switcher: successfully switched", "config", tc.ID,
			"was_dpi_variant", tc.IsVariant)
		e.recordAttempt(tc, "verify", true, "", dpiHint, dpiReason)
		if e.ai != nil && e.ai.Enabled() {
			e.ai.Presets().MarkSuccess(tc.BaseConfigID)
		}
		return tc.ID, nil
	}

	return "", nil
}

// filterVariantsByProfile trims/weights generated variants according to
// global DPI strategy profile and confidence from detector.
func (e *Engine) filterVariantsByProfile(
	variants []*config.TunnelConfig,
	dpiHint dpi.BlockType,
	dpiReason string,
	dpiConfidence float64,
) []*config.TunnelConfig {
	if len(variants) == 0 {
		return variants
	}

	profile := e.cfg.DPIProfile
	if profile == "" {
		profile = "balanced"
	}

	// Very low confidence: keep only safest variants in any profile.
	if dpiConfidence < 0.4 {
		profile = "compat"
	}

	filtered := make([]*config.TunnelConfig, 0, len(variants))
	filtered = strategy.SelectVariants(profile, dpiReason, dpiHint, dpiConfidence, variants)

	slog.Debug("switcher: dpi profile filter",
		"profile", profile,
		"in", len(variants),
		"out", len(filtered),
		"dpi_hint", dpiHint.String(),
		"dpi_reason", dpiReason,
		"dpi_confidence", dpiConfidence)
	return filtered
}

// parallelProbe brings up each config on its routing table concurrently,
// runs probes through that table, and returns results.
// activeID is the currently running config; it is already up so we skip Up().
func (e *Engine) parallelProbe(ctx context.Context, configs []*config.TunnelConfig, activeID string) []parallelProbeResult {
	probCtx, cancel := context.WithTimeout(ctx, e.cfg.ParallelProbeTimeout)
	defer cancel()

	results := make([]parallelProbeResult, len(configs))
	var wg sync.WaitGroup
	wg.Add(len(configs))

	for i, tc := range configs {
		go func(idx int, cfg *config.TunnelConfig) {
			defer wg.Done()
			res := e.probeConfig(probCtx, cfg, activeID)
			results[idx] = res
		}(i, tc)
	}
	wg.Wait()
	return results
}

func (e *Engine) probeConfig(ctx context.Context, tc *config.TunnelConfig, activeID string) parallelProbeResult {
	// DPI variants share the base config's InterfaceName, which would conflict
	// during parallel probe. Generate a unique interface name for non-active
	// variant configs to avoid collisions.
	if tc.ID != activeID && tc.IsVariant && tc.InterfaceName != "" {
		h := 0
		for _, c := range tc.ID {
			h = h*31 + int(c)
		}
		tc.InterfaceName = fmt.Sprintf("vw%d", (h%9000+9000)%9000+1000)
	}

	mgr, err := tunnel.ForConfig(tc, e.cfg)
	if err != nil {
		return parallelProbeResult{tc: tc, err: err}
	}

	// If this is the currently active config, it is already running — skip Up()
	// to avoid disrupting production traffic during the probe phase.
	if tc.ID != activeID {
		if err := mgr.Up(ctx, tc); err != nil {
			return parallelProbeResult{tc: tc, err: err}
		}

		// Small settling time for newly-started tunnels.
		select {
		case <-time.After(1 * time.Second):
		case <-ctx.Done():
			_ = mgr.Down(ctx, tc)
			return parallelProbeResult{tc: tc, err: ctx.Err()}
		}
	}

	fwmark := mgr.FWMark(tc)
	res := e.probeEng.ProbeAll(ctx, fwmark)
	return parallelProbeResult{tc: tc, res: res}
}

// apply brings up a config as the production tunnel.
func (e *Engine) apply(ctx context.Context, tc *config.TunnelConfig) error {
	mgr, err := tunnel.ForConfig(tc, e.cfg)
	if err != nil {
		return err
	}
	if err := mgr.Up(ctx, tc); err != nil {
		return err
	}
	if e.policy != nil {
		if err := e.policy.Apply(ctx, mgr.FWMark(tc)); err != nil {
			// Policy is optional enhancement: tunnel should remain usable even if
			// selector enforcement could not be applied.
			slog.Warn("switcher: policy apply failed", "config", tc.ID, "err", err)
		}
	}
	return nil
}

func fwmarkFor(tc *config.TunnelConfig) uint32 {
	if tc.RoutingTableID > 0 {
		return uint32(tc.RoutingTableID)
	}
	// fallback mirrors tunnel routing table derivation logic range.
	h := 0
	for _, c := range tc.ID {
		h = h*31 + int(c)
	}
	return uint32(100 + (h%100+100)%100)
}

// verify probes all targets through the default route (fwmark 0) to confirm
// the new config provides working internet.
func (e *Engine) verify(ctx context.Context) bool {
	verCtx, cancel := context.WithTimeout(ctx, e.cfg.SwitchVerifyTimeout)
	defer cancel()

	select {
	case <-time.After(3 * time.Second):
	case <-verCtx.Done():
		return false
	}

	res := e.probeEng.ProbeAll(verCtx, 0)
	slog.Info("switcher: verify probe",
		"success", res.Success,
		"rtt", res.AvgRTT,
		"loss", res.PacketLoss,
		"err_type", res.DominantErrType.String())
	return res.Success
}

func (e *Engine) recordAttempt(tc *config.TunnelConfig, stage string, success bool, errSummary string, hint dpi.BlockType, reason string) {
	if e.ai == nil {
		return
	}
	base := tc.ID
	if tc.BaseConfigID != "" {
		base = tc.BaseConfigID
	}
	e.ai.AttemptLog().Add(aiadvisor.AttemptEntry{
		Timestamp:    time.Now(),
		ConfigID:     tc.ID,
		BaseConfigID: base,
		VariantID:    tc.ID,
		ReasonCode:   reason,
		BlockType:    hint.String(),
		Profile:      e.cfg.DPIProfile,
		Stage:        stage,
		Success:      success,
		ErrorSummary: errSummary,
	})
}

func (e *Engine) AIStatus() aiadvisor.Status {
	if e.ai == nil {
		return aiadvisor.Status{}
	}
	return e.ai.Status()
}
