package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Store manages tunnel configuration files and daemon settings.
type Store struct {
	cfg DaemonConfig
}

// NewStore creates a Store backed by cfg.
func NewStore(cfg DaemonConfig) *Store {
	return &Store{cfg: cfg}
}

// LoadAll reads all enabled TunnelConfig entries from ConfigDir.
func (s *Store) LoadAll() ([]*TunnelConfig, error) {
	entries, err := filepath.Glob(filepath.Join(s.cfg.ConfigDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("glob configs: %w", err)
	}
	var tunnels []*TunnelConfig
	for _, path := range entries {
		tc, err := s.loadFile(path)
		if err != nil {
			slog.Warn("skipping invalid config", "path", path, "err", err)
			continue
		}
		if tc.Enabled {
			tunnels = append(tunnels, tc)
		}
	}
	return tunnels, nil
}

func (s *Store) loadFile(path string) (*TunnelConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var tc TunnelConfig
	if err := json.NewDecoder(f).Decode(&tc); err != nil {
		return nil, err
	}
	if tc.ID == "" {
		tc.ID = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	if err := validate(&tc); err != nil {
		return nil, err
	}
	return &tc, nil
}

// Save writes a TunnelConfig to ConfigDir/<id>.json with mode 0600.
func (s *Store) Save(tc *TunnelConfig) error {
	if err := os.MkdirAll(s.cfg.ConfigDir, 0750); err != nil {
		return err
	}
	path := filepath.Join(s.cfg.ConfigDir, tc.ID+".json")
	data, err := json.MarshalIndent(tc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// Delete removes a config by ID.
func (s *Store) Delete(id string) error {
	return os.Remove(filepath.Join(s.cfg.ConfigDir, id+".json"))
}

func validate(tc *TunnelConfig) error {
	if tc.ID == "" {
		return fmt.Errorf("missing id")
	}
	switch tc.Protocol {
	case ProtocolWireGuard:
		if tc.WireGuard == nil {
			return fmt.Errorf("wg config missing")
		}
	case ProtocolAmneziaWG:
		if tc.AmneziaWG == nil {
			return fmt.Errorf("awg config missing")
		}
	case ProtocolVLESS:
		if tc.VLESS == nil {
			return fmt.Errorf("vless config missing")
		}
	default:
		return fmt.Errorf("unknown protocol: %q", tc.Protocol)
	}
	return nil
}

// LoadDaemonConfigFromUCI reads settings from OpenWrt UCI (uci get vpn-watchdog.global.*).
// Falls back to defaults on any error.
func LoadDaemonConfigFromUCI() DaemonConfig {
	cfg := DefaultDaemonConfig()

	get := func(key string) string {
		out, err := exec.Command("uci", "get", key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}

	getDur := func(key string, def time.Duration) time.Duration {
		v := get(key)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return time.Duration(n) * time.Second
	}

	getInt := func(key string, def int) int {
		v := get(key)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return n
	}

	cfg.ProbeIntervalHealthy = getDur("vpn-watchdog.global.probe_interval_healthy", cfg.ProbeIntervalHealthy)
	cfg.ProbeIntervalDegraded = getDur("vpn-watchdog.global.probe_interval_degraded", cfg.ProbeIntervalDegraded)
	cfg.DegradedThreshold = getInt("vpn-watchdog.global.degraded_threshold", cfg.DegradedThreshold)
	cfg.ProbingThreshold = getInt("vpn-watchdog.global.probing_threshold", cfg.ProbingThreshold)
	cfg.MaxSwitchAttempts = getInt("vpn-watchdog.global.max_switch_attempts", cfg.MaxSwitchAttempts)
	cfg.PostSwitchCooldown = getDur("vpn-watchdog.global.post_switch_cooldown", cfg.PostSwitchCooldown)
	cfg.SwitchVerifyTimeout = getDur("vpn-watchdog.global.switch_verify_timeout", cfg.SwitchVerifyTimeout)
	if v := get("vpn-watchdog.global.probe_rotate_targets"); v == "0" {
		cfg.ProbeRotateTargets = false
	}
	cfg.ProbeTargetPool = getInt("vpn-watchdog.global.probe_target_pool", cfg.ProbeTargetPool)
	if v := get("vpn-watchdog.global.probe_use_doh"); v == "0" {
		cfg.ProbeUseDoH = false
	}
	if v := get("vpn-watchdog.global.probe_doh_endpoint"); v != "" {
		cfg.ProbeDoHEndpoint = v
	}

	if v := get("vpn-watchdog.global.config_dir"); v != "" {
		cfg.ConfigDir = v
	}
	if v := get("vpn-watchdog.global.sing_box_bin"); v != "" {
		cfg.SingBoxBin = v
	}

	getFloat := func(key string, def float64) float64 {
		v := get(key)
		if v == "" {
			return def
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return def
		}
		return f
	}

	// DPI settings.
	if v := get("vpn-watchdog.global.dpi_auto_tune"); v == "0" {
		cfg.DPIAutoTune = false
	}
	cfg.DPIMaxVariants = getInt("vpn-watchdog.global.dpi_max_variants", cfg.DPIMaxVariants)
	if v := get("vpn-watchdog.global.dpi_profile"); v != "" {
		switch v {
		case "compat", "balanced", "aggressive":
			cfg.DPIProfile = v
		default:
			slog.Warn("invalid dpi_profile, using default", "value", v, "default", cfg.DPIProfile)
		}
	}

	// Optional AI advisor settings.
	if v := get("vpn-watchdog.global.ai_enabled"); v == "1" {
		cfg.AIAdvisor.Enabled = true
	}
	if v := get("vpn-watchdog.global.ai_provider"); v != "" {
		cfg.AIAdvisor.Provider = v
	}
	if v := get("vpn-watchdog.global.ai_endpoint"); v != "" {
		cfg.AIAdvisor.Endpoint = v
	}
	if v := get("vpn-watchdog.global.ai_api_key_file"); v != "" {
		cfg.AIAdvisor.APIKeyFile = v
	}
	cfg.AIAdvisor.Timeout = getDur("vpn-watchdog.global.ai_timeout", cfg.AIAdvisor.Timeout)
	cfg.AIAdvisor.PresetTTL = getDur("vpn-watchdog.global.ai_preset_ttl", cfg.AIAdvisor.PresetTTL)
	cfg.AIAdvisor.MaxCallsPerHour = getInt("vpn-watchdog.global.ai_max_calls_per_hour", cfg.AIAdvisor.MaxCallsPerHour)
	cfg.AIAdvisor.MinConfidence = getFloat("vpn-watchdog.global.ai_min_confidence", cfg.AIAdvisor.MinConfidence)

	// Load probe targets from UCI list.
	targets := loadUCIProbeTargets()
	if len(targets) > 0 {
		cfg.ProbeTargets = targets
	}

	// Load user-defined routing selectors for VPN bypass mode.
	cfg.VPNDomains = loadUCIStringList("vpn-watchdog.global.vpn_domain")
	cfg.VPNIPs = loadUCIStringList("vpn-watchdog.global.vpn_ip")
	cfg.VPNDomainFiles = loadUCIStringList("vpn-watchdog.global.vpn_domain_file")

	// Make user VPN domains observable by probe subsystem as low-priority probes.
	// This helps detect if configured blocked domains are still unreachable.
	cfg.ProbeTargets = appendDomainProbeTargets(cfg.ProbeTargets, cfg.VPNDomains, 3)

	return cfg
}

func loadUCIProbeTargets() []ProbeTarget {
	out, err := exec.Command("uci", "show", "vpn-watchdog").Output()
	if err != nil {
		return nil
	}
	// Use a map for field accumulation but track insertion order to keep the
	// output slice in the same order as the UCI section indices.
	seen := map[string]ProbeTarget{}
	var order []string // section indices in first-seen order
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		// vpn-watchdog.@probe_target[0].host=1.1.1.1
		if !strings.Contains(line, "probe_target") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], strings.Trim(parts[1], "'")
		// Extract index like @probe_target[0]
		idxStart := strings.Index(key, "[")
		idxEnd := strings.Index(key, "]")
		if idxStart < 0 || idxEnd < 0 {
			continue
		}
		idx := key[idxStart+1 : idxEnd]
		field := key[idxEnd+2:]
		if _, exists := seen[idx]; !exists {
			order = append(order, idx)
		}
		pt := seen[idx]
		switch field {
		case "host":
			pt.Host = val
		case "port":
			pt.Port, _ = strconv.Atoi(val)
		case "type":
			pt.Type = ProbeType(val)
		}
		seen[idx] = pt
	}
	var targets []ProbeTarget
	for _, idx := range order {
		if pt := seen[idx]; pt.Host != "" {
			targets = append(targets, pt)
		}
	}
	return targets
}

func loadUCIStringList(prefix string) []string {
	// Example line:
	// vpn-watchdog.global.vpn_domain='example.com'
	out, err := exec.Command("uci", "show", "vpn-watchdog.global").Output()
	if err != nil {
		return nil
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	var vals []string
	seen := map[string]struct{}{}
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, prefix+"=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		v := strings.Trim(parts[1], "' \t\r\n")
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		vals = append(vals, v)
	}
	return vals
}

func appendDomainProbeTargets(base []ProbeTarget, domains []string, max int) []ProbeTarget {
	if len(domains) == 0 || max <= 0 {
		return base
	}
	seen := map[string]struct{}{}
	for _, t := range base {
		seen[t.Host] = struct{}{}
	}
	count := 0
	for _, d := range domains {
		if count >= max {
			break
		}
		if _, ok := seen[d]; ok {
			continue
		}
		base = append(base, ProbeTarget{
			Host: d,
			Port: 443,
			Type: ProbeHTTPS,
		})
		seen[d] = struct{}{}
		count++
	}
	return base
}
