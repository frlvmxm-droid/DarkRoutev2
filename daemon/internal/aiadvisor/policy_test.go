package aiadvisor

import (
	"testing"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

func TestValidateRecommendationRejectsLowConfidence(t *testing.T) {
	rec := Recommendation{BaseConfigID: "x", Confidence: 0.3}
	if err := ValidateRecommendation(rec, 0.6); err == nil {
		t.Fatal("expected low-confidence recommendation to be rejected")
	}
}

func TestBuildVariantFromRecommendationVLESS(t *testing.T) {
	port := 443
	base := &config.TunnelConfig{ID: "v", Protocol: config.ProtocolVLESS, VLESS: &config.VLESSConfig{Port: 8443, Transport: "ws", TransportPath: "/old"}}
	rec := Recommendation{BaseConfigID: "v", Confidence: 0.9, EndpointPort: &port, VLESSTransport: "grpc", VLESSPath: "/svc"}
	v, err := BuildVariantFromRecommendation(base, rec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.VLESS.Port != 443 || v.VLESS.Transport != "grpc" || v.VLESS.TransportPath != "svc" {
		t.Fatalf("unexpected variant: %+v", v.VLESS)
	}
}
