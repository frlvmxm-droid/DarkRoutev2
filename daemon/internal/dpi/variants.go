package dpi

import (
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

// awgProfile describes an AmneziaWG obfuscation intensity profile.
// H1-H4 magic headers are critical for evading DPI signature detection of
// the WireGuard handshake — TSPU systems match on these fixed protocol bytes.
type awgProfile struct {
	name string
	jc   int // JunkPacketCount
	jmin int // JunkPacketMinSize
	jmax int // JunkPacketMaxSize
	s1   int // InitPacketJunkSize
	s2   int // ResponsePacketJunkSize
	h1   int // InitPacketMagicHeader
	h2   int // ResponsePacketMagicHeader
	h3   int // UnderLoadPacketMagicHeader
	h4   int // TransportPacketMagicHeader
}

var awgProfiles = []awgProfile{
	{name: "mild", jc: 2, jmin: 20, jmax: 50, s1: 0, s2: 0, h1: 0, h2: 0, h3: 0, h4: 0},
	{name: "moderate", jc: 4, jmin: 40, jmax: 70, s1: 0, s2: 0, h1: 1262700631, h2: 1752251508, h3: 1346454851, h4: 1532746439},
	{name: "aggressive", jc: 7, jmin: 50, jmax: 100, s1: 10, s2: 10, h1: 904958478, h2: 1153565019, h3: 1750837079, h4: 1009857588},
}

// MTU values to try when packet-level issues are suspected.
var mtuVariants = []int{1200, 1280, 1360}

// Common camouflage ports used by anti-DPI tools. Rotating endpoint ports can
// help against simplistic L4/L7 policies that key on default VPN ports.
var endpointPortVariants = []int{443, 80, 53}

// vlessFingerprints to cycle through when TLS DPI is detected.
var vlessFingerprints = []string{"chrome", "firefox", "safari", "ios", "android", "360"}

// vlessTransports to try when HTTP-layer DPI is detected.
var vlessTransports = []string{"ws", "grpc", "httpupgrade", "tcp", "quic", "h3"}

// Common-looking HTTP/gRPC paths to blend into normal CDN/API traffic patterns.
var vlessPathVariants = []string{"/", "/cdn-cgi/trace", "/api", "/v1", "grpc"}

var rnd = rand.New(rand.NewSource(time.Now().UnixNano()))

// GenerateVariants returns a set of mutated TunnelConfig copies for DPI
// evasion, guided by the detected BlockType hint.
//
// The returned configs have IsVariant=true and BaseConfigID set to tc.ID.
// Their IDs follow the pattern "<base-id>:dpi:<variant-suffix>".
// The maxVariants parameter caps the total number returned per base config.
func GenerateVariants(tc *config.TunnelConfig, hint BlockType, maxVariants int) []*config.TunnelConfig {
	if maxVariants <= 0 {
		maxVariants = 8
	}

	var variants []*config.TunnelConfig

	switch tc.Protocol {
	case config.ProtocolWireGuard:
		variants = wgVariants(tc, hint)
	case config.ProtocolAmneziaWG:
		variants = awgVariants(tc, hint)
	case config.ProtocolVLESS:
		variants = vlessVariants(tc, hint)
	}

	if len(variants) > maxVariants {
		variants = variants[:maxVariants]
	}
	return variants
}

// ── WireGuard variants ──────────────────────────────────────────────────────

func wgVariants(tc *config.TunnelConfig, hint BlockType) []*config.TunnelConfig {
	// MTU variants are useful for all block types.
	var out []*config.TunnelConfig
	for _, mtu := range mtuVariants {
		if mtu == tc.MTU {
			continue // skip identical
		}
		v := cloneWG(tc)
		v.ID = fmt.Sprintf("%s:dpi:mtu%d", tc.ID, mtu)
		v.MTU = mtu
		v.IsVariant = true
		v.BaseConfigID = tc.ID
		out = append(out, v)
	}

	// Protocol/TCP-level issues: also rotate endpoint ports.
	if hint == BlockProtocol || hint == BlockTCP || hint == BlockNone {
		out = append(out, wgEndpointPortVariants(tc)...)
	}
	return out
}

// ── AmneziaWG variants ──────────────────────────────────────────────────────

func awgVariants(tc *config.TunnelConfig, hint BlockType) []*config.TunnelConfig {
	if tc.AmneziaWG == nil {
		return nil
	}
	var out []*config.TunnelConfig

	// Protocol-level DPI → prioritise obfuscation profiles.
	profiles := awgProfiles
	if hint == BlockProtocol {
		// Aggressive first when protocol signature detected.
		profiles = []awgProfile{awgProfiles[2], awgProfiles[1], awgProfiles[0]}
	}

	for _, prof := range profiles {
		// Skip if identical to current settings.
		cur := tc.AmneziaWG
		if cur.JunkPacketCount == prof.jc &&
			cur.JunkPacketMinSize == prof.jmin &&
			cur.JunkPacketMaxSize == prof.jmax &&
			cur.InitPacketMagicHeader == prof.h1 &&
			cur.ResponsePacketMagicHeader == prof.h2 {
			continue
		}
		v := cloneAWG(tc)
		v.ID = fmt.Sprintf("%s:dpi:awg:%s", tc.ID, prof.name)
		v.AmneziaWG.JunkPacketCount = prof.jc
		v.AmneziaWG.JunkPacketMinSize = prof.jmin
		v.AmneziaWG.JunkPacketMaxSize = prof.jmax
		v.AmneziaWG.InitPacketJunkSize = prof.s1
		v.AmneziaWG.ResponsePacketJunkSize = prof.s2
		v.AmneziaWG.InitPacketMagicHeader = prof.h1
		v.AmneziaWG.ResponsePacketMagicHeader = prof.h2
		v.AmneziaWG.UnderLoadPacketMagicHeader = prof.h3
		v.AmneziaWG.TransportPacketMagicHeader = prof.h4
		randomizeAWGHeaders(v.AmneziaWG)
		v.IsVariant = true
		v.BaseConfigID = tc.ID
		out = append(out, v)
	}

	// Also try MTU variants.
	for _, mtu := range mtuVariants {
		if mtu == tc.MTU {
			continue
		}
		v := cloneAWG(tc)
		v.ID = fmt.Sprintf("%s:dpi:mtu%d", tc.ID, mtu)
		v.MTU = mtu
		v.IsVariant = true
		v.BaseConfigID = tc.ID
		out = append(out, v)
	}

	// Protocol/TCP-level issues: also rotate endpoint ports.
	if hint == BlockProtocol || hint == BlockTCP || hint == BlockNone {
		out = append(out, awgEndpointPortVariants(tc)...)
	}

	return out
}

// ── VLESS variants ──────────────────────────────────────────────────────────

func vlessVariants(tc *config.TunnelConfig, hint BlockType) []*config.TunnelConfig {
	if tc.VLESS == nil {
		return nil
	}
	var out []*config.TunnelConfig

	switch hint {
	case BlockTLS:
		// TLS fingerprint block → rotate fingerprints first, then transports.
		out = append(out, vlessFingerprintVariants(tc)...)
		out = append(out, vlessTransportVariants(tc)...)
		out = append(out, vlessECHVariants(tc)...)

	case BlockHTTP:
		// HTTP-layer block → change transport first.
		out = append(out, vlessTransportVariants(tc)...)
		out = append(out, vlessFingerprintVariants(tc)...)

	default:
		// Unknown or protocol block → try both.
		out = append(out, vlessFingerprintVariants(tc)...)
		out = append(out, vlessTransportVariants(tc)...)
		out = append(out, vlessECHVariants(tc)...)
	}

	if hint == BlockHTTP || hint == BlockTLS || hint == BlockNone {
		out = append(out, vlessPathVariantsForTransport(tc)...)
	}

	if hint == BlockProtocol || hint == BlockTCP || hint == BlockNone {
		out = append(out, vlessPortVariants(tc)...)
	}

	return out
}

func vlessFingerprintVariants(tc *config.TunnelConfig) []*config.TunnelConfig {
	var out []*config.TunnelConfig
	for _, fp := range vlessFingerprints {
		if fp == tc.VLESS.Fingerprint {
			continue
		}
		v := cloneVLESS(tc)
		v.ID = fmt.Sprintf("%s:dpi:fp:%s", tc.ID, fp)
		v.VLESS.Fingerprint = fp
		v.IsVariant = true
		v.BaseConfigID = tc.ID
		out = append(out, v)
	}
	return out
}

func vlessTransportVariants(tc *config.TunnelConfig) []*config.TunnelConfig {
	var out []*config.TunnelConfig
	for _, tr := range vlessTransports {
		if tr == tc.VLESS.Transport {
			continue
		}
		v := cloneVLESS(tc)
		v.ID = fmt.Sprintf("%s:dpi:tr:%s", tc.ID, tr)
		v.VLESS.Transport = tr
		// gRPC and ws need a path; use "/" as fallback.
		if v.VLESS.TransportPath == "" {
			v.VLESS.TransportPath = "/"
		}
		v.IsVariant = true
		v.BaseConfigID = tc.ID
		out = append(out, v)
	}
	return out
}

func vlessPathVariantsForTransport(tc *config.TunnelConfig) []*config.TunnelConfig {
	if tc.VLESS == nil {
		return nil
	}
	if tc.VLESS.Transport != "ws" && tc.VLESS.Transport != "httpupgrade" && tc.VLESS.Transport != "grpc" {
		return nil
	}

	var out []*config.TunnelConfig
	for _, path := range vlessPathVariants {
		normalized := normalizeTransportPath(tc.VLESS.Transport, path)
		if normalized == tc.VLESS.TransportPath {
			continue
		}

		v := cloneVLESS(tc)
		v.ID = fmt.Sprintf("%s:dpi:path:%s", tc.ID, slugPathVariant(normalized))
		v.VLESS.TransportPath = normalized
		v.IsVariant = true
		v.BaseConfigID = tc.ID
		out = append(out, v)
	}
	return out
}

func vlessECHVariants(tc *config.TunnelConfig) []*config.TunnelConfig {
	if tc.VLESS == nil || tc.VLESS.Security == "none" || tc.VLESS.ECH {
		return nil
	}
	v := cloneVLESS(tc)
	v.ID = fmt.Sprintf("%s:dpi:ech:on", tc.ID)
	v.VLESS.ECH = true
	v.IsVariant = true
	v.BaseConfigID = tc.ID
	return []*config.TunnelConfig{v}
}

func randomizeAWGHeaders(awg *config.AmneziaWGConfig) {
	if awg == nil {
		return
	}
	awg.InitPacketMagicHeader = int(rnd.Int31())
	awg.ResponsePacketMagicHeader = int(rnd.Int31())
	awg.UnderLoadPacketMagicHeader = int(rnd.Int31())
	awg.TransportPacketMagicHeader = int(rnd.Int31())
}

func normalizeTransportPath(transport, path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		p = "/"
	}
	if transport == "grpc" {
		return strings.TrimPrefix(p, "/")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func slugPathVariant(path string) string {
	slug := strings.Trim(path, "/")
	if slug == "" {
		return "root"
	}
	slug = strings.ReplaceAll(slug, "/", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	return slug
}

func wgEndpointPortVariants(tc *config.TunnelConfig) []*config.TunnelConfig {
	if tc.WireGuard == nil || tc.WireGuard.Endpoint == "" {
		return nil
	}
	host, currentPort, ok := splitEndpointHostPort(tc.WireGuard.Endpoint)
	if !ok {
		return nil
	}
	var out []*config.TunnelConfig
	for _, p := range endpointPortVariants {
		if p == currentPort {
			continue
		}
		v := cloneWG(tc)
		v.ID = fmt.Sprintf("%s:dpi:ep%d", tc.ID, p)
		v.WireGuard.Endpoint = joinEndpointHostPort(host, p)
		v.IsVariant = true
		v.BaseConfigID = tc.ID
		out = append(out, v)
	}
	return out
}

func awgEndpointPortVariants(tc *config.TunnelConfig) []*config.TunnelConfig {
	if tc.AmneziaWG == nil || tc.AmneziaWG.Endpoint == "" {
		return nil
	}
	host, currentPort, ok := splitEndpointHostPort(tc.AmneziaWG.Endpoint)
	if !ok {
		return nil
	}
	var out []*config.TunnelConfig
	for _, p := range endpointPortVariants {
		if p == currentPort {
			continue
		}
		v := cloneAWG(tc)
		v.ID = fmt.Sprintf("%s:dpi:ep%d", tc.ID, p)
		v.AmneziaWG.Endpoint = joinEndpointHostPort(host, p)
		v.IsVariant = true
		v.BaseConfigID = tc.ID
		out = append(out, v)
	}
	return out
}

func vlessPortVariants(tc *config.TunnelConfig) []*config.TunnelConfig {
	if tc.VLESS == nil {
		return nil
	}
	currentPort := tc.VLESS.Port
	if currentPort <= 0 {
		return nil
	}
	var out []*config.TunnelConfig
	for _, p := range endpointPortVariants {
		if p == currentPort {
			continue
		}
		v := cloneVLESS(tc)
		v.ID = fmt.Sprintf("%s:dpi:port%d", tc.ID, p)
		v.VLESS.Port = p
		v.IsVariant = true
		v.BaseConfigID = tc.ID
		out = append(out, v)
	}
	return out
}

func splitEndpointHostPort(endpoint string) (host string, port int, ok bool) {
	h, p, err := net.SplitHostPort(endpoint)
	if err != nil {
		// If IPv6 wasn't bracketed, split manually by last ':'.
		idx := strings.LastIndex(endpoint, ":")
		if idx <= 0 || idx+1 >= len(endpoint) {
			return "", 0, false
		}
		h = endpoint[:idx]
		p = endpoint[idx+1:]
	}

	portNum, err := strconv.Atoi(strings.TrimSpace(p))
	if err != nil || portNum <= 0 || portNum > 65535 {
		return "", 0, false
	}
	return h, portNum, true
}

func joinEndpointHostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// ── Deep-copy helpers ───────────────────────────────────────────────────────

func cloneWG(tc *config.TunnelConfig) *config.TunnelConfig {
	v := *tc
	if tc.WireGuard != nil {
		wg := *tc.WireGuard
		ips := make([]string, len(wg.AllowedIPs))
		copy(ips, wg.AllowedIPs)
		wg.AllowedIPs = ips
		v.WireGuard = &wg
	}
	return &v
}

func cloneAWG(tc *config.TunnelConfig) *config.TunnelConfig {
	v := *tc
	if tc.AmneziaWG != nil {
		awg := *tc.AmneziaWG
		ips := make([]string, len(awg.AllowedIPs))
		copy(ips, awg.AllowedIPs)
		awg.AllowedIPs = ips
		v.AmneziaWG = &awg
	}
	return &v
}

func cloneVLESS(tc *config.TunnelConfig) *config.TunnelConfig {
	v := *tc
	if tc.VLESS != nil {
		vl := *tc.VLESS
		v.VLESS = &vl
	}
	return &v
}
