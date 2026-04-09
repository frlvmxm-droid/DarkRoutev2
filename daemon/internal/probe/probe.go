// Package probe implements connectivity probing for the vpn-watchdog daemon.
//
// It supports ICMP, TCP-connect, HTTP, and HTTPS probes, all bound to a
// specific routing table so they travel through the VPN tunnel under test.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

// ProbeErrType classifies the failure mode of a probe attempt, providing
// richer signal for DPI detection.
type ProbeErrType int

const (
	// ProbeErrNone means no error.
	ProbeErrNone ProbeErrType = iota
	// ProbeErrTimeout means the probe timed out — likely a hard block or slow path.
	ProbeErrTimeout
	// ProbeErrRST means the connection was reset — typical of DPI RST injection.
	ProbeErrRST
	// ProbeErrTLSHandshake means TLS negotiation failed — possible fingerprint block.
	ProbeErrTLSHandshake
	// ProbeErrHTTPReject means an HTTP-level refusal — possible content-based DPI.
	ProbeErrHTTPReject
	// ProbeErrDNS means the DNS lookup failed.
	ProbeErrDNS
	// ProbeErrOther means an unclassified error.
	ProbeErrOther
)

// String returns a human-readable error type name.
func (e ProbeErrType) String() string {
	switch e {
	case ProbeErrNone:
		return "none"
	case ProbeErrTimeout:
		return "timeout"
	case ProbeErrRST:
		return "rst"
	case ProbeErrTLSHandshake:
		return "tls_handshake"
	case ProbeErrHTTPReject:
		return "http_reject"
	case ProbeErrDNS:
		return "dns"
	default:
		return "other"
	}
}

// Result is the outcome of a single probe attempt.
type Result struct {
	Target  config.ProbeTarget
	Success bool
	RTT     time.Duration
	Err     error
	ErrType ProbeErrType // classified failure mode for DPI detection
}

// AggResult is the aggregated outcome of probing all targets.
type AggResult struct {
	Success    bool
	AvgRTT     time.Duration
	PacketLoss float64 // 0.0–1.0
	Results    []Result
	// DominantErrType is the most common failure mode across all probes.
	// Useful for the DPI detector to classify blocking type.
	DominantErrType ProbeErrType
}

// Engine executes probes against a list of targets.
type Engine struct {
	cfg config.DaemonConfig
}

// New creates a probe Engine.
func New(cfg config.DaemonConfig) *Engine {
	return &Engine{cfg: cfg}
}

// ProbeAll runs all configured targets concurrently and returns an aggregated result.
// fwmark, if > 0, sets SO_MARK on sockets so the kernel routes packets
// through the routing table associated with that mark.
func (e *Engine) ProbeAll(ctx context.Context, fwmark uint32) AggResult {
	return e.ProbeTargets(ctx, e.selectTargets(), fwmark)
}

// ProbeTargets probes a specific list of targets.
func (e *Engine) ProbeTargets(ctx context.Context, targets []config.ProbeTarget, fwmark uint32) AggResult {
	if len(targets) == 0 {
		return AggResult{Success: false, PacketLoss: 1.0}
	}

	results := make([]Result, len(targets))
	var wg sync.WaitGroup
	wg.Add(len(targets))

	for i, t := range targets {
		go func(idx int, tgt config.ProbeTarget) {
			defer wg.Done()
			timeout := e.cfg.ProbeTimeout
			if timeout == 0 {
				timeout = 5 * time.Second
			}
			pCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			results[idx] = runProbe(pCtx, tgt, fwmark, e.cfg)
		}(i, t)
	}
	wg.Wait()

	return aggregate(results)
}

func runProbe(ctx context.Context, tgt config.ProbeTarget, fwmark uint32, cfg config.DaemonConfig) Result {
	start := time.Now()
	var err error

	switch tgt.Type {
	case config.ProbeICMP:
		err = probeICMP(ctx, tgt.Host, fwmark, cfg)
	case config.ProbeTCP:
		err = probeTCP(ctx, tgt.Host, tgt.Port, fwmark, cfg)
	case config.ProbeHTTP:
		err = probeHTTP(ctx, "http", tgt.Host, tgt.Port, fwmark, cfg)
	case config.ProbeHTTPS:
		err = probeHTTP(ctx, "https", tgt.Host, tgt.Port, fwmark, cfg)
	default:
		err = fmt.Errorf("unknown probe type: %s", tgt.Type)
	}

	rtt := time.Since(start)
	return Result{
		Target:  tgt,
		Success: err == nil,
		RTT:     rtt,
		Err:     err,
		ErrType: classifyErr(err),
	}
}

func (e *Engine) selectTargets() []config.ProbeTarget {
	targets := e.cfg.ProbeTargets
	if !e.cfg.ProbeRotateTargets || len(targets) <= 1 {
		return targets
	}
	pool := e.cfg.ProbeTargetPool
	if pool <= 0 || pool >= len(targets) {
		pool = len(targets)
	}
	src := rand.New(rand.NewSource(time.Now().UnixNano()))
	perm := src.Perm(len(targets))
	out := make([]config.ProbeTarget, 0, pool)
	for i := 0; i < pool; i++ {
		out = append(out, targets[perm[i]])
	}
	return out
}

// classifyErr maps a network error to a ProbeErrType for DPI analysis.
func classifyErr(err error) ProbeErrType {
	if err == nil {
		return ProbeErrNone
	}

	// DNS failure.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ProbeErrDNS
	}

	// Timeout.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ProbeErrTimeout
	}

	// Connection reset (RST) — hallmark of DPI RST injection.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if errors.Is(opErr.Err, syscall.ECONNRESET) ||
			errors.Is(opErr.Err, syscall.ECONNREFUSED) {
			return ProbeErrRST
		}
	}

	// TLS handshake failure.
	msg := err.Error()
	if strings.Contains(msg, "tls:") || strings.Contains(msg, "handshake") ||
		strings.Contains(msg, "certificate") || strings.Contains(msg, "x509") {
		return ProbeErrTLSHandshake
	}

	// HTTP-level rejection (connection closed after HTTP request sent).
	if strings.Contains(msg, "EOF") || strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") {
		return ProbeErrHTTPReject
	}

	return ProbeErrOther
}

// probeICMP uses a TCP fallback since raw ICMP requires CAP_NET_RAW on OpenWrt.
func probeICMP(ctx context.Context, host string, fwmark uint32, cfg config.DaemonConfig) error {
	addrs, err := lookupHost(ctx, host, fwmark, cfg)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("no addresses for %s", host)
	}
	addr := addrs[0]
	dialer := markedDialer(fwmark)
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr, "80"))
	if err != nil {
		conn, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr, "443"))
		if err != nil {
			return fmt.Errorf("icmp/tcp probe %s: %w", host, err)
		}
	}
	conn.Close()
	return nil
}

func lookupHost(ctx context.Context, host string, fwmark uint32, cfg config.DaemonConfig) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	if cfg.ProbeUseDoH {
		addrs, err := lookupHostDoH(ctx, host, fwmark, cfg.ProbeDoHEndpoint)
		if err == nil && len(addrs) > 0 {
			return addrs, nil
		}
	}
	return net.DefaultResolver.LookupHost(ctx, host)
}

type dohJSONResponse struct {
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

func lookupHostDoH(ctx context.Context, host string, fwmark uint32, endpoint string) ([]string, error) {
	if endpoint == "" {
		endpoint = "https://1.1.1.1/dns-query"
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("name", host)
	q.Set("type", "A")
	u.RawQuery = q.Encode()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: markedDialer(fwmark).DialContext,
		},
		Timeout: 6 * time.Second,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var payload dohJSONResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	addrs := make([]string, 0, len(payload.Answer))
	for _, ans := range payload.Answer {
		if ans.Type == 1 && net.ParseIP(ans.Data) != nil {
			addrs = append(addrs, ans.Data)
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no A records for %s", host)
	}
	return addrs, nil
}

func probeTCP(ctx context.Context, host string, port int, fwmark uint32, cfg config.DaemonConfig) error {
	if port == 0 {
		port = 80
	}
	addrs, err := lookupHost(ctx, host, fwmark, cfg)
	if err != nil {
		return err
	}
	dialer := markedDialer(fwmark)
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addrs[0], strconv.Itoa(port)))
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

func probeHTTP(ctx context.Context, scheme, host string, port int, fwmark uint32, cfg config.DaemonConfig) error {
	if port == 0 {
		if scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	}
	addrs, err := lookupHost(ctx, host, fwmark, cfg)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s://%s:%d/", scheme, addrs[0], port)

	transport := &http.Transport{
		DialContext:       markedDialer(fwmark).DialContext,
		DisableKeepAlives: true,
	}
	if scheme == "https" {
		transport = transportWithSkipVerify(transport)
	}

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	req.Host = host
	// Use a realistic browser User-Agent to avoid UA-based HTTP DPI.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "close")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// markedDialer returns a net.Dialer that sets SO_MARK = fwmark on sockets.
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

func aggregate(results []Result) AggResult {
	if len(results) == 0 {
		return AggResult{Success: false, PacketLoss: 1.0}
	}
	success := 0
	var totalRTT time.Duration
	errCounts := make(map[ProbeErrType]int)

	for _, r := range results {
		if r.Success {
			success++
			totalRTT += r.RTT
		} else {
			errCounts[r.ErrType]++
		}
	}

	loss := float64(len(results)-success) / float64(len(results))
	ok := float64(success)/float64(len(results)) > 0.5

	var avgRTT time.Duration
	if success > 0 {
		avgRTT = totalRTT / time.Duration(success)
	}

	// Find the dominant error type.
	dominant := ProbeErrOther
	maxCount := 0
	for et, count := range errCounts {
		if count > maxCount {
			maxCount = count
			dominant = et
		}
	}
	if success == len(results) {
		dominant = ProbeErrNone
	}

	return AggResult{
		Success:         ok,
		AvgRTT:          avgRTT,
		PacketLoss:      loss,
		Results:         results,
		DominantErrType: dominant,
	}
}
