package switcher

import (
	"testing"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/dpi"
)

func TestFilterVariantsByProfileCompat(t *testing.T) {
	e := &Engine{cfg: config.DaemonConfig{DPIProfile: "compat"}}
	variants := []*config.TunnelConfig{
		{ID: "a:dpi:fp:chrome"},
		{ID: "a:dpi:mtu1280"},
		{ID: "a:dpi:awg:mild"},
	}
	out := e.filterVariantsByProfile(variants, dpi.BlockTLS, "tls_timeout", 0.8)
	if len(out) != 2 {
		t.Fatalf("compat should keep mtu+mild subset, got %d", len(out))
	}
}

func TestFilterVariantsByProfileBalancedSkipsAggressiveWhenWeakSignal(t *testing.T) {
	e := &Engine{cfg: config.DaemonConfig{DPIProfile: "balanced"}}
	variants := []*config.TunnelConfig{
		{ID: "a:dpi:awg:aggressive"},
		{ID: "a:dpi:awg:moderate"},
		{ID: "a:dpi:mtu1280"},
	}
	out := e.filterVariantsByProfile(variants, dpi.BlockTLS, "tls_timeout", 0.6)
	for _, v := range out {
		if v.ID == "a:dpi:awg:aggressive" {
			t.Fatal("balanced should skip aggressive awg variant on non-protocol hint")
		}
	}
}

func TestFilterVariantsByProfileLowConfidenceForcesCompat(t *testing.T) {
	e := &Engine{cfg: config.DaemonConfig{DPIProfile: "aggressive"}}
	variants := []*config.TunnelConfig{
		{ID: "a:dpi:fp:chrome"},
		{ID: "a:dpi:mtu1280"},
	}
	out := e.filterVariantsByProfile(variants, dpi.BlockTLS, "tls_timeout", 0.2)
	if len(out) != 1 || out[0].ID != "a:dpi:mtu1280" {
		t.Fatalf("low confidence should force compat-like output, got %+v", out)
	}
}

