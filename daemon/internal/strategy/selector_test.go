package strategy

import (
	"testing"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/dpi"
)

func TestSelectVariantsTLSBalancedPrioritizesFingerprint(t *testing.T) {
	in := []*config.TunnelConfig{
		{ID: "x:dpi:mtu1280"},
		{ID: "x:dpi:tr:ws"},
		{ID: "x:dpi:fp:chrome"},
	}
	out := SelectVariants("balanced", "tls_timeout", dpi.BlockTLS, 0.8, in)
	if len(out) == 0 || out[0].ID != "x:dpi:fp:chrome" {
		t.Fatalf("expected TLS strategy to prioritize fingerprint variant, got %+v", out)
	}
}

func TestSelectVariantsLowConfidenceForcesCompat(t *testing.T) {
	in := []*config.TunnelConfig{
		{ID: "x:dpi:fp:chrome"},
		{ID: "x:dpi:mtu1280"},
		{ID: "x:dpi:tr:ws"},
	}
	out := SelectVariants("aggressive", "tls_timeout", dpi.BlockTLS, 0.2, in)
	if len(out) == 0 || out[0].ID != "x:dpi:mtu1280" {
		t.Fatalf("low confidence should force safe selection (mtu first), got %+v", out)
	}
}

func TestSelectVariantsProtocolAggressiveIncludesAWG(t *testing.T) {
	in := []*config.TunnelConfig{
		{ID: "x:dpi:awg:mild"},
		{ID: "x:dpi:awg:moderate"},
		{ID: "x:dpi:awg:aggressive"},
		{ID: "x:dpi:mtu1280"},
	}
	out := SelectVariants("aggressive", "protocol_signal", dpi.BlockProtocol, 0.9, in)
	if len(out) < 3 {
		t.Fatalf("expected awg protocol variants in aggressive mode, got %+v", out)
	}
}

func TestSelectVariantsTCPBalancedPrioritizesEndpointPort(t *testing.T) {
	in := []*config.TunnelConfig{
		{ID: "x:dpi:mtu1280"},
		{ID: "x:dpi:ep443"},
		{ID: "x:dpi:awg:mild"},
	}
	out := SelectVariants("balanced", "tcp_timeout", dpi.BlockTCP, 0.9, in)
	if len(out) == 0 || out[0].ID != "x:dpi:ep443" {
		t.Fatalf("expected TCP strategy to prioritize endpoint-port variant, got %+v", out)
	}
}

func TestSelectVariantsHTTPBalancedPrioritizesPathVariants(t *testing.T) {
	in := []*config.TunnelConfig{
		{ID: "x:dpi:tr:ws"},
		{ID: "x:dpi:path:api"},
		{ID: "x:dpi:mtu1280"},
	}
	out := SelectVariants("balanced", "http_reset", dpi.BlockHTTP, 0.9, in)
	if len(out) == 0 || out[0].ID != "x:dpi:path:api" {
		t.Fatalf("expected HTTP strategy to prioritize path variant, got %+v", out)
	}
}
