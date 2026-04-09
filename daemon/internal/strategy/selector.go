// Package strategy implements reason/profile-aware selection of bypass variants.
package strategy

import (
	"strings"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/dpi"
)

// SelectVariants picks and orders generated variants according to profile,
// detector reason, block hint and confidence.
//
// This is a lightweight strategy-runtime layer inspired by profile-driven
// engines: the same generated pool can be transformed differently by policy.
func SelectVariants(
	profile string,
	reason string,
	hint dpi.BlockType,
	confidence float64,
	variants []*config.TunnelConfig,
) []*config.TunnelConfig {
	if len(variants) == 0 {
		return variants
	}
	if profile == "" {
		profile = "balanced"
	}

	// Confidence gates are global safety rails.
	if confidence < 0.4 {
		profile = "compat"
		reason = "low_confidence"
	}

	switch profile {
	case "compat":
		return compatSelect(reason, hint, variants)
	case "aggressive":
		return aggressiveSelect(reason, hint, variants)
	default:
		return balancedSelect(reason, hint, variants)
	}
}

func compatSelect(reason string, hint dpi.BlockType, variants []*config.TunnelConfig) []*config.TunnelConfig {
	out := make([]*config.TunnelConfig, 0, len(variants))
	patterns := patternsForReason(reason, hint, true)
	for _, p := range patterns {
		out = appendUnique(out, matchByPattern(variants, p)...)
	}
	if len(out) == 0 {
		// Safety fallback: keep only MTU and mild AWG if present.
		out = appendUnique(out, matchByPattern(variants, ":dpi:mtu")...)
		out = appendUnique(out, matchByPattern(variants, ":dpi:awg:mild")...)
	}
	if len(out) == 0 {
		out = append(out, variants[0])
	}
	return capVariants(out, 3)
}

func balancedSelect(reason string, hint dpi.BlockType, variants []*config.TunnelConfig) []*config.TunnelConfig {
	out := make([]*config.TunnelConfig, 0, len(variants))
	patterns := patternsForReason(reason, hint, false)
	for _, p := range patterns {
		out = appendUnique(out, matchByPattern(variants, p)...)
	}
	// Fill the rest from original order, but avoid very aggressive AWG unless protocol signal.
	for _, v := range variants {
		if contains(v.ID, ":dpi:awg:aggressive") && hint != dpi.BlockProtocol {
			continue
		}
		out = appendUnique(out, v)
	}
	return capVariants(out, 6)
}

func aggressiveSelect(reason string, hint dpi.BlockType, variants []*config.TunnelConfig) []*config.TunnelConfig {
	out := make([]*config.TunnelConfig, 0, len(variants))
	patterns := patternsForReason(reason, hint, false)
	for _, p := range patterns {
		out = appendUnique(out, matchByPattern(variants, p)...)
	}
	for _, v := range variants {
		out = appendUnique(out, v)
	}
	return out
}

func patternsForReason(reason string, hint dpi.BlockType, safeOnly bool) []string {
	switch {
	case strings.HasPrefix(reason, "tls_"):
		if safeOnly {
			return []string{":dpi:fp:", ":dpi:mtu"}
		}
		return []string{":dpi:fp:", ":dpi:path:", ":dpi:tr:tcp", ":dpi:tr:ws", ":dpi:mtu"}

	case strings.HasPrefix(reason, "http_"):
		if safeOnly {
			return []string{":dpi:path:", ":dpi:tr:ws", ":dpi:tr:httpupgrade", ":dpi:mtu"}
		}
		return []string{":dpi:path:", ":dpi:tr:ws", ":dpi:tr:httpupgrade", ":dpi:tr:grpc", ":dpi:mtu"}

	case strings.HasPrefix(reason, "tcp_"):
		return []string{":dpi:ep", ":dpi:port", ":dpi:mtu", ":dpi:awg:mild"}

	case strings.HasPrefix(reason, "protocol_") || hint == dpi.BlockProtocol:
		if safeOnly {
			return []string{":dpi:ep", ":dpi:port", ":dpi:awg:mild", ":dpi:mtu"}
		}
		return []string{":dpi:ep", ":dpi:port", ":dpi:awg:aggressive", ":dpi:awg:moderate", ":dpi:awg:mild", ":dpi:mtu"}

	case reason == "low_confidence":
		return []string{":dpi:mtu"}

	default:
		// Unknown reason: conservative generic ordering.
		if safeOnly {
			return []string{":dpi:mtu", ":dpi:fp:"}
		}
		return []string{":dpi:mtu", ":dpi:fp:", ":dpi:tr:"}
	}
}

func matchByPattern(variants []*config.TunnelConfig, pattern string) []*config.TunnelConfig {
	if pattern == "" {
		return nil
	}
	out := make([]*config.TunnelConfig, 0, len(variants))
	for _, v := range variants {
		if contains(v.ID, pattern) {
			out = append(out, v)
		}
	}
	return out
}

func appendUnique(dst []*config.TunnelConfig, src ...*config.TunnelConfig) []*config.TunnelConfig {
	seen := make(map[string]struct{}, len(dst))
	for _, d := range dst {
		seen[d.ID] = struct{}{}
	}
	for _, s := range src {
		if _, ok := seen[s.ID]; ok {
			continue
		}
		seen[s.ID] = struct{}{}
		dst = append(dst, s)
	}
	return dst
}

func capVariants(in []*config.TunnelConfig, n int) []*config.TunnelConfig {
	if n <= 0 || len(in) <= n {
		return in
	}
	return in[:n]
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
