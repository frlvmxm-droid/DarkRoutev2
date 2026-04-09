package dpi

import (
	"testing"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

func TestGenerateVariantsWGAddsEndpointPortsOnProtocolHint(t *testing.T) {
	base := &config.TunnelConfig{
		ID:       "wg-main",
		Protocol: config.ProtocolWireGuard,
		MTU:      1420,
		WireGuard: &config.WireGuardConfig{
			Endpoint: "vpn.example.com:51820",
		},
	}

	variants := GenerateVariants(base, BlockProtocol, 32)

	if !containsVariantID(variants, "wg-main:dpi:ep443") {
		t.Fatalf("expected endpoint-port variant ep443, got %#v", collectIDs(variants))
	}
	if !containsVariantID(variants, "wg-main:dpi:ep80") {
		t.Fatalf("expected endpoint-port variant ep80, got %#v", collectIDs(variants))
	}
}

func TestGenerateVariantsVLESSAddsPortVariantsOnTCPHint(t *testing.T) {
	base := &config.TunnelConfig{
		ID:       "vless-main",
		Protocol: config.ProtocolVLESS,
		VLESS: &config.VLESSConfig{
			Port:        8443,
			Transport:   "tcp",
			Fingerprint: "chrome",
		},
	}

	variants := GenerateVariants(base, BlockTCP, 32)

	if !containsVariantID(variants, "vless-main:dpi:port443") {
		t.Fatalf("expected vless port variant 443, got %#v", collectIDs(variants))
	}
	if !containsVariantID(variants, "vless-main:dpi:port53") {
		t.Fatalf("expected vless port variant 53, got %#v", collectIDs(variants))
	}
}

func TestSplitJoinEndpointHostPortIPv6(t *testing.T) {
	host, port, ok := splitEndpointHostPort("[2001:db8::1]:51820")
	if !ok {
		t.Fatal("expected parser to support bracketed IPv6 endpoint")
	}
	if host != "2001:db8::1" {
		t.Fatalf("unexpected host: %q", host)
	}
	if port != 51820 {
		t.Fatalf("unexpected port: %d", port)
	}

	joined := joinEndpointHostPort(host, 443)
	if joined != "[2001:db8::1]:443" {
		t.Fatalf("unexpected joined endpoint: %q", joined)
	}
}

func containsVariantID(variants []*config.TunnelConfig, id string) bool {
	for _, v := range variants {
		if v.ID == id {
			return true
		}
	}
	return false
}

func collectIDs(variants []*config.TunnelConfig) []string {
	out := make([]string, 0, len(variants))
	for _, v := range variants {
		out = append(out, v.ID)
	}
	return out
}

func TestGenerateVariantsVLESSAddsPathVariantsForHTTP(t *testing.T) {
	base := &config.TunnelConfig{
		ID:       "vless-http",
		Protocol: config.ProtocolVLESS,
		VLESS: &config.VLESSConfig{
			Port:          443,
			Transport:     "ws",
			TransportPath: "/",
		},
	}

	variants := GenerateVariants(base, BlockHTTP, 64)
	if !containsVariantID(variants, "vless-http:dpi:path:cdn-cgi-trace") {
		t.Fatalf("expected path variant for cdn-cgi/trace, got %#v", collectIDs(variants))
	}
	if !containsVariantID(variants, "vless-http:dpi:path:api") {
		t.Fatalf("expected path variant for /api, got %#v", collectIDs(variants))
	}
}

func TestNormalizeTransportPathForGRPC(t *testing.T) {
	if got := normalizeTransportPath("grpc", "/grpc"); got != "grpc" {
		t.Fatalf("expected grpc service name without slash, got %q", got)
	}
	if got := normalizeTransportPath("ws", "api"); got != "/api" {
		t.Fatalf("expected ws path with slash, got %q", got)
	}
}

func TestGenerateVariantsVLESSIncludesQUICAndH3(t *testing.T) {
	base := &config.TunnelConfig{
		ID:       "vless-tr",
		Protocol: config.ProtocolVLESS,
		VLESS: &config.VLESSConfig{
			Transport: "tcp",
		},
	}
	variants := GenerateVariants(base, BlockHTTP, 128)
	if !containsVariantID(variants, "vless-tr:dpi:tr:quic") {
		t.Fatalf("expected quic transport variant, got %#v", collectIDs(variants))
	}
	if !containsVariantID(variants, "vless-tr:dpi:tr:h3") {
		t.Fatalf("expected h3 transport variant, got %#v", collectIDs(variants))
	}
}

func TestGenerateVariantsVLESSECHOnTLSHint(t *testing.T) {
	base := &config.TunnelConfig{
		ID:       "vless-ech",
		Protocol: config.ProtocolVLESS,
		VLESS: &config.VLESSConfig{
			Security: "tls",
		},
	}
	variants := GenerateVariants(base, BlockTLS, 32)
	if !containsVariantID(variants, "vless-ech:dpi:ech:on") {
		t.Fatalf("expected ech variant, got %#v", collectIDs(variants))
	}
}
