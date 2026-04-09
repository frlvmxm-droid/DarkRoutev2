package aiadvisor

import (
	"fmt"
	"strings"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

func ValidateRecommendation(rec Recommendation, minConfidence float64) error {
	if rec.BaseConfigID == "" {
		return fmt.Errorf("missing base_config_id")
	}
	if rec.Confidence < minConfidence {
		return fmt.Errorf("confidence %.2f below minimum %.2f", rec.Confidence, minConfidence)
	}
	if rec.MTU != nil && (*rec.MTU < 1200 || *rec.MTU > 1420) {
		return fmt.Errorf("mtu out of safe range")
	}
	if rec.EndpointPort != nil && (*rec.EndpointPort < 1 || *rec.EndpointPort > 65535) {
		return fmt.Errorf("endpoint port out of range")
	}
	if rec.VLESSTransport != "" {
		switch rec.VLESSTransport {
		case "tcp", "ws", "grpc", "httpupgrade":
		default:
			return fmt.Errorf("unsupported vless transport")
		}
	}
	if rec.VLESSPath != "" && len(rec.VLESSPath) > 64 {
		return fmt.Errorf("vless path too long")
	}
	if rec.AWGProfile != "" {
		switch rec.AWGProfile {
		case "mild", "moderate", "aggressive":
		default:
			return fmt.Errorf("unsupported awg profile")
		}
	}
	return nil
}

// BuildVariantFromRecommendation creates one temporary variant from recommendation.
func BuildVariantFromRecommendation(base *config.TunnelConfig, rec Recommendation) (*config.TunnelConfig, error) {
	if base == nil {
		return nil, fmt.Errorf("base config is nil")
	}
	v := *base
	v.IsVariant = true
	v.BaseConfigID = base.ID
	v.ID = base.ID + ":dpi:ai"

	if rec.MTU != nil {
		v.MTU = *rec.MTU
	}

	switch base.Protocol {
	case config.ProtocolWireGuard:
		if base.WireGuard == nil {
			return nil, fmt.Errorf("wg config missing")
		}
		wg := *base.WireGuard
		if rec.EndpointPort != nil {
			h, _, ok := splitHostPortCompat(wg.Endpoint)
			if ok {
				wg.Endpoint = joinHostPortCompat(h, *rec.EndpointPort)
			}
		}
		v.WireGuard = &wg

	case config.ProtocolAmneziaWG:
		if base.AmneziaWG == nil {
			return nil, fmt.Errorf("awg config missing")
		}
		awg := *base.AmneziaWG
		if rec.EndpointPort != nil {
			h, _, ok := splitHostPortCompat(awg.Endpoint)
			if ok {
				awg.Endpoint = joinHostPortCompat(h, *rec.EndpointPort)
			}
		}
		applyAWGProfile(&awg, rec.AWGProfile)
		v.AmneziaWG = &awg

	case config.ProtocolVLESS:
		if base.VLESS == nil {
			return nil, fmt.Errorf("vless config missing")
		}
		vl := *base.VLESS
		if rec.EndpointPort != nil {
			vl.Port = *rec.EndpointPort
		}
		if rec.VLESSTransport != "" {
			vl.Transport = rec.VLESSTransport
		}
		if rec.VLESSFingerprint != "" {
			vl.Fingerprint = rec.VLESSFingerprint
		}
		if rec.VLESSPath != "" {
			if vl.Transport == "grpc" {
				vl.TransportPath = strings.TrimPrefix(rec.VLESSPath, "/")
			} else {
				if strings.HasPrefix(rec.VLESSPath, "/") {
					vl.TransportPath = rec.VLESSPath
				} else {
					vl.TransportPath = "/" + rec.VLESSPath
				}
			}
		}
		v.VLESS = &vl
	}
	return &v, nil
}

func applyAWGProfile(awg *config.AmneziaWGConfig, profile string) {
	switch profile {
	case "mild":
		awg.JunkPacketCount, awg.JunkPacketMinSize, awg.JunkPacketMaxSize = 2, 20, 50
	case "moderate":
		awg.JunkPacketCount, awg.JunkPacketMinSize, awg.JunkPacketMaxSize = 4, 40, 70
	case "aggressive":
		awg.JunkPacketCount, awg.JunkPacketMinSize, awg.JunkPacketMaxSize = 7, 50, 100
	}
}
