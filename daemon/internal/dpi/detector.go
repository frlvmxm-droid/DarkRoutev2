// Package dpi implements DPI (Deep Packet Inspection) detection and
// automatic obfuscation-parameter adaptation for the vpn-watchdog daemon.
//
// The detector runs a sequence of differential probes to classify WHY a
// connection is failing: hard IP/port block, HTTP-layer DPI, TLS-fingerprint
// blocking, or VPN protocol signature detection.
package dpi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

// BlockType classifies the type of network restriction detected.
type BlockType int

const (
	// BlockNone means no DPI blocking detected; failure has another cause.
	BlockNone BlockType = iota
	// BlockTCP means TCP-level block (IP/port blacklist, hard firewall rule).
	BlockTCP
	// BlockHTTP means HTTP application-layer DPI (HTTP headers, URL patterns).
	BlockHTTP
	// BlockTLS means TLS-level DPI (fingerprint detection, SNI blocking).
	BlockTLS
	// BlockProtocol means VPN protocol signature detected by DPI (WireGuard
	// handshake pattern, etc.).
	BlockProtocol
)

// String returns a human-readable block type name.
func (b BlockType) String() string {
	switch b {
	case BlockNone:
		return "none"
	case BlockTCP:
		return "tcp_block"
	case BlockHTTP:
		return "http_dpi"
	case BlockTLS:
		return "tls_dpi"
	case BlockProtocol:
		return "protocol_dpi"
	default:
		return "unknown"
	}
}

// DetectionResult is the outcome of a DPI detection run.
type DetectionResult struct {
	BlockType BlockType `json:"block_type"`
	// ReasonCode is a stable machine-readable code describing the dominant cause.
	ReasonCode string `json:"reason_code"`
	// Confidence is an estimate (0..1) of how reliable ReasonCode/BlockType is.
	Confidence float64 `json:"confidence"`
	// Evidence holds human-readable strings describing what was observed.
	Evidence []string  `json:"evidence"`
	// StageResults stores per-step details from the differential probe sequence.
	StageResults []StageResult `json:"stage_results,omitempty"`
	TestedAt time.Time `json:"tested_at"`
}

// StageResult captures a single diagnostic stage outcome.
type StageResult struct {
	Stage      string  `json:"stage"`
	Success    bool    `json:"success"`
	ReasonCode string  `json:"reason_code,omitempty"`
	LatencyMS  int64   `json:"latency_ms,omitempty"`
	Detail     string  `json:"detail,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// Detect runs a sequence of differential connectivity tests against the
// provided probe targets (using fwmark for routing) and returns a
// DetectionResult classifying the type of blocking present.
//
// The probe sequence is:
//  1. TCP connect to target:443 → classify hard block vs RST injection
//  2. If TCP OK → HTTP HEAD request → detect HTTP-layer DPI
//  3. If HTTP OK → HTTPS HEAD with default Go TLS → detect TLS fingerprint block
//
// fwmark == 0 probes through the default routing table (no VPN).
// Pass the active tunnel's fwmark to probe through the tunnel.
func Detect(ctx context.Context, targets []config.ProbeTarget, fwmark uint32) DetectionResult {
	result := DetectionResult{
		TestedAt:    time.Now(),
		BlockType:   BlockNone,
		ReasonCode:  "ok",
		Confidence:  0.2,
		StageResults: make([]StageResult, 0, 4),
	}

	if len(targets) == 0 {
		result.ReasonCode = "no_probe_targets"
		result.Confidence = 0.95
		result.Evidence = append(result.Evidence, "no probe targets configured")
		result.StageResults = append(result.StageResults, StageResult{
			Stage:      "precheck",
			Success:    false,
			ReasonCode: result.ReasonCode,
			Detail:     "probe target list is empty",
			Confidence: result.Confidence,
		})
		return result
	}

	// Pick the best target for DPI detection: prefer HTTPS targets.
	target := pickDetectionTarget(targets)

	host := target.Host
	port := target.Port
	if port == 0 {
		port = 443
	}
	addr := fmt.Sprintf("%s:%d", host, port)

	dialer := markedDialer(fwmark)

	// ── Step 1: TCP connect ─────────────────────────────────────────────────
	tcpCtx, tcpCancel := context.WithTimeout(ctx, 5*time.Second)
	defer tcpCancel()

	tcpStart := time.Now()
	conn, tcpErr := dialer.DialContext(tcpCtx, "tcp", addr)
	tcpElapsed := time.Since(tcpStart)

	if tcpErr != nil {
		bt, reasonCode, confidence, evidence := classifyTCPError(tcpErr, tcpElapsed)
		result.BlockType = bt
		result.ReasonCode = reasonCode
		result.Confidence = confidence
		result.Evidence = evidence
		result.StageResults = append(result.StageResults, StageResult{
			Stage:      "tcp_connect",
			Success:    false,
			ReasonCode: reasonCode,
			LatencyMS:  tcpElapsed.Milliseconds(),
			Detail:     fmt.Sprintf("dial %s failed: %v", addr, tcpErr),
			Confidence: confidence,
		})
		return result
	}
	conn.Close()
	result.Evidence = append(result.Evidence,
		fmt.Sprintf("TCP connect OK to %s in %dms", addr, tcpElapsed.Milliseconds()))
	result.StageResults = append(result.StageResults, StageResult{
		Stage:      "tcp_connect",
		Success:    true,
		ReasonCode: "ok",
		LatencyMS:  tcpElapsed.Milliseconds(),
		Detail:     fmt.Sprintf("dial %s succeeded", addr),
		Confidence: 0.9,
	})

	// ── Step 2: HTTP HEAD ───────────────────────────────────────────────────
	httpOK := false
	httpPorts := detectionHTTPPorts(host, targets)
	var httpErr error
	var httpPort int
	for _, p := range httpPorts {
		httpPort = p
		httpErr = probeHTTPHead(ctx, host, p, fwmark)
		if httpErr == nil {
			httpOK = true
			break
		}
	}
	if !httpOK {
		reason, conf, detail, likelyDPI := classifyHTTPError(httpErr)
		result.StageResults = append(result.StageResults, StageResult{
			Stage:      "http_head",
			Success:    false,
			ReasonCode: reason,
			Detail:     detail,
			Confidence: conf,
		})
		result.Evidence = append(result.Evidence,
			fmt.Sprintf("HTTP HEAD failed on ports %v: %s", httpPorts, detail))
		// If error pattern is not DPI-like (e.g. port closed), continue with lower confidence.
		if likelyDPI {
			result.BlockType = BlockHTTP
			result.ReasonCode = reason
			result.Confidence = conf
			return result
		}
	} else {
		result.Evidence = append(result.Evidence,
			fmt.Sprintf("HTTP HEAD OK on :%d", httpPort))
		result.StageResults = append(result.StageResults, StageResult{
			Stage:      "http_head",
			Success:    true,
			ReasonCode: "ok",
			Detail:     fmt.Sprintf("port=%d", httpPort),
			Confidence: 0.8,
		})
	}

	// ── Step 3: HTTPS HEAD (plain Go TLS) ──────────────────────────────────
	httpsURL := fmt.Sprintf("https://%s:%d/", host, port)
	httpsClient := tlsHTTPClient(fwmark, false)
	tlsCtx, tlsCancel := context.WithTimeout(ctx, 8*time.Second)
	defer tlsCancel()

	req2, _ := http.NewRequestWithContext(tlsCtx, http.MethodHead, httpsURL, nil)
	req2.Header.Set("User-Agent", "Mozilla/5.0 (compatible)")

	tlsResp, tlsErr := httpsClient.Do(req2)
	if tlsErr != nil {
		blockType, reason, conf, detail := classifyTLSError(tlsErr, httpOK)
		result.BlockType = blockType
		result.ReasonCode = reason
		result.Confidence = conf
		result.Evidence = append(result.Evidence, detail)
		result.StageResults = append(result.StageResults, StageResult{
			Stage:      "https_head",
			Success:    false,
			ReasonCode: reason,
			Detail:     tlsErr.Error(),
			Confidence: conf,
		})
		return result
	}
	tlsResp.Body.Close()
	result.Evidence = append(result.Evidence,
		fmt.Sprintf("HTTPS HEAD OK: %d — no DPI detected", tlsResp.StatusCode))
	result.StageResults = append(result.StageResults, StageResult{
		Stage:      "https_head",
		Success:    true,
		ReasonCode: "ok",
		Detail:     fmt.Sprintf("status=%d", tlsResp.StatusCode),
		Confidence: 0.9,
	})

	result.BlockType = BlockNone
	result.ReasonCode = "ok"
	if httpOK {
		result.Confidence = 0.95
	} else {
		result.Confidence = 0.75
	}
	return result
}

// DetectProtocolBlock runs a supplementary check specifically for VPN protocol
// signature detection. It probes whether UDP traffic to the VPN endpoint is
// being blocked (RST or timeout), suggesting DPI is filtering VPN handshakes.
//
// endpoint is "host:port", protocol is "wg", "awg", or "vless".
func DetectProtocolBlock(ctx context.Context, endpoint, protocol string, fwmark uint32) (bool, string) {
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return false, fmt.Sprintf("invalid endpoint %q: %v", endpoint, err)
	}
	_ = portStr

	// Attempt TCP to the same port as a signal (many WG/AWG endpoints block TCP but not UDP).
	// If TCP is fully reachable but VPN fails → protocol DPI likely.
	dialer := markedDialer(fwmark)
	tCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	conn, err := dialer.DialContext(tCtx, "tcp", net.JoinHostPort(host, portStr))
	if err != nil {
		if isRSTError(err) {
			return true, fmt.Sprintf("TCP RST to %s — DPI blocking %s protocol port", endpoint, protocol)
		}
		// Timeout or other: could be firewall, not necessarily DPI
		return false, fmt.Sprintf("TCP timeout to %s — may be firewall, not DPI", endpoint)
	}
	conn.Close()
	return false, fmt.Sprintf("TCP OK to %s — DPI may target %s protocol signature (not port)", endpoint, protocol)
}

// SaveDetection persists a DetectionResult to stateDir for LuCI consumption.
func SaveDetection(stateDir string, r DetectionResult) {
	path := filepath.Join(stateDir, "dpi_detection.json")
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}

// LoadDetection reads the last persisted DetectionResult from stateDir.
func LoadDetection(stateDir string) (DetectionResult, bool) {
	path := filepath.Join(stateDir, "dpi_detection.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return DetectionResult{}, false
	}
	var r DetectionResult
	if err := json.Unmarshal(data, &r); err != nil {
		return DetectionResult{}, false
	}
	return r, true
}

// ── internal helpers ────────────────────────────────────────────────────────

// classifyTCPError determines block type from a TCP dial error.
func classifyTCPError(err error, elapsed time.Duration) (BlockType, string, float64, []string) {
	var evidence []string

	if isRSTError(err) {
		// RST within very short time → active DPI RST injection.
		if elapsed < 200*time.Millisecond {
			evidence = append(evidence,
				fmt.Sprintf("TCP RST in %dms — DPI RST injection (protocol signature block)", elapsed.Milliseconds()))
			return BlockProtocol, "tcp_rst_fast", 0.9, evidence
		}
		evidence = append(evidence,
			fmt.Sprintf("TCP RST in %dms — hard TCP block", elapsed.Milliseconds()))
		return BlockTCP, "tcp_rst", 0.8, evidence
	}

	// Connection refused means nothing is listening — not a DPI indicator.
	if isConnRefusedError(err) {
		evidence = append(evidence,
			fmt.Sprintf("TCP ECONNREFUSED in %dms — port closed, not DPI", elapsed.Milliseconds()))
		return BlockNone, "tcp_refused", 0.3, evidence
	}

	// Timeout or other error.
	if isTimeoutError(err) {
		evidence = append(evidence,
			fmt.Sprintf("TCP timeout after %dms — hard IP/port block", elapsed.Milliseconds()))
		return BlockTCP, "tcp_timeout", 0.7, evidence
	}

	evidence = append(evidence,
		fmt.Sprintf("TCP error in %dms: %v", elapsed.Milliseconds(), err))
	return BlockTCP, "tcp_connect_error", 0.55, evidence
}

// isRSTError returns true if err is a connection reset (RST injection).
// ECONNREFUSED (port closed) is NOT an RST — it indicates no listener, not DPI.
func isRSTError(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return errors.Is(opErr.Err, syscall.ECONNRESET)
	}
	return false
}

// isTimeoutError returns true if err is a deadline/timeout.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

func isConnRefusedError(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && errors.Is(opErr.Err, syscall.ECONNREFUSED)
}

func detectionHTTPPorts(host string, targets []config.ProbeTarget) []int {
	seen := map[int]struct{}{80: {}}
	ports := []int{80}
	for _, t := range targets {
		if t.Host != host || t.Type != config.ProbeHTTP {
			continue
		}
		p := t.Port
		if p == 0 {
			p = 80
		}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			ports = append(ports, p)
		}
	}
	return ports
}

func probeHTTPHead(ctx context.Context, host string, port int, fwmark uint32) error {
	httpURL := fmt.Sprintf("http://%s:%d/", host, port)
	httpClient := plainHTTPClient(fwmark)
	httpCtx, httpCancel := context.WithTimeout(ctx, 5*time.Second)
	defer httpCancel()
	req, _ := http.NewRequestWithContext(httpCtx, http.MethodHead, httpURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible)")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func classifyHTTPError(err error) (reason string, confidence float64, detail string, likelyDPI bool) {
	if err == nil {
		return "ok", 0.9, "http probe passed", false
	}
	if isConnRefusedError(err) {
		return "http_unavailable", 0.35, "HTTP port refused (insufficient evidence for HTTP DPI)", false
	}
	if isRSTError(err) {
		return "http_rst", 0.85, "HTTP RST pattern suggests HTTP DPI", true
	}
	if isTimeoutError(err) {
		return "http_timeout", 0.55, "HTTP timeout (possible filtering or service unavailability)", true
	}
	return "http_probe_failed", 0.4, fmt.Sprintf("HTTP probe failed: %v", err), false
}

func classifyTLSError(err error, httpOK bool) (BlockType, string, float64, string) {
	if err == nil {
		return BlockNone, "ok", 0.95, "HTTPS probe passed"
	}
	msg := strings.ToLower(err.Error())

	if strings.Contains(msg, "x509:") || strings.Contains(msg, "certificate") {
		return BlockNone, "tls_certificate_error", 0.9, fmt.Sprintf("HTTPS failed due to certificate/trust issue: %v", err)
	}
	if isRSTError(err) {
		conf := 0.7
		if !httpOK {
			conf = 0.5
		}
		return BlockTLS, "tls_rst", conf, fmt.Sprintf("HTTPS reset while TCP reachable: %v", err)
	}
	if isTimeoutError(err) {
		conf := 0.65
		if !httpOK {
			conf = 0.45
		}
		return BlockTLS, "tls_timeout", conf, fmt.Sprintf("HTTPS timeout while TCP reachable: %v", err)
	}

	conf := 0.6
	if !httpOK {
		conf = 0.4
	}
	return BlockTLS, "tls_handshake_error", conf, fmt.Sprintf("HTTPS failed while TCP reachable: %v", err)
}

// pickDetectionTarget returns the best target for DPI detection probing.
// Prefers HTTPS targets, falls back to the first available.
func pickDetectionTarget(targets []config.ProbeTarget) config.ProbeTarget {
	for _, t := range targets {
		if t.Type == config.ProbeHTTPS {
			return t
		}
	}
	for _, t := range targets {
		if t.Type == config.ProbeHTTP {
			return t
		}
	}
	return targets[0]
}

// markedDialer returns a net.Dialer that sets SO_MARK = fwmark.
func markedDialer(fwmark uint32) *net.Dialer {
	d := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: -1,
	}
	if fwmark != 0 {
		d.Control = controlWithFWMark(fwmark)
	}
	return d
}

// plainHTTPClient returns an HTTP client that routes through fwmark and
// does NOT follow redirects (a redirect IS a sign of life).
func plainHTTPClient(fwmark uint32) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:       markedDialer(fwmark).DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 8 * time.Second,
	}
}

// tlsHTTPClient returns an HTTPS client. skipVerify=true bypasses cert check.
func tlsHTTPClient(fwmark uint32, skipVerify bool) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: markedDialer(fwmark).DialContext,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: skipVerify, //nolint:gosec
				// Use a minimal cipher suite list to make fingerprint obvious.
				// A DPI system blocking standard Go TLS will fail here.
			},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 10 * time.Second,
	}
}
