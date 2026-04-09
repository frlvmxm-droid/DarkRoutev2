package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

// VLESSManager manages VLESS tunnels via sing-box.
// sing-box runs as a child process and exposes a local SOCKS5 port.
// Traffic routing is achieved by directing packets to that SOCKS5 port via
// an ip rule + iptables REDIRECT (or tproxy) in the routing table for this config.
type VLESSManager struct {
	cfg     config.DaemonConfig
	mu      sync.Mutex
	procMap map[string]*exec.Cmd // configID → sing-box process
}

// singBoxConfig is the JSON structure for sing-box.
type singBoxOutbound struct {
	Tag       string            `json:"tag"`
	Type      string            `json:"type"`
	Server    string            `json:"server"`
	Port      int               `json:"server_port"`
	UUID      string            `json:"uuid"`
	Flow      string            `json:"flow,omitempty"`
	TLS       *singBoxTLS       `json:"tls,omitempty"`
	Transport *singBoxTransport `json:"transport,omitempty"`
}

type singBoxTLS struct {
	Enabled    bool            `json:"enabled"`
	ServerName string          `json:"server_name,omitempty"`
	Insecure   bool            `json:"insecure,omitempty"`
	UTLS       *singBoxUTLS    `json:"utls,omitempty"`
	ECH        *singBoxECH     `json:"ech,omitempty"`
	Reality    *singBoxReality `json:"reality,omitempty"`
}

type singBoxECH struct {
	Enabled bool   `json:"enabled"`
	Config  string `json:"config,omitempty"`
}

type singBoxUTLS struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type singBoxReality struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
}

type singBoxTransport struct {
	Type        string            `json:"type"`
	Path        string            `json:"path,omitempty"`
	ServiceName string            `json:"service_name,omitempty"`
	Host        string            `json:"host,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

type singBoxInbound struct {
	Tag    string `json:"tag"`
	Type   string `json:"type"`
	Listen string `json:"listen"`
	Port   int    `json:"listen_port"`
	Sniff  bool   `json:"sniff,omitempty"`
}

type singBoxConfig struct {
	Log       map[string]string      `json:"log"`
	Inbounds  []singBoxInbound       `json:"inbounds"`
	Outbounds []singBoxOutbound      `json:"outbounds"`
	Route     map[string]interface{} `json:"route"`
}

// Up starts a sing-box process for the VLESS config.
func (m *VLESSManager) Up(ctx context.Context, tc *config.TunnelConfig) error {
	vless := tc.VLESS
	if vless == nil {
		return fmt.Errorf("vless config is nil for %s", tc.ID)
	}

	// Kill any existing sing-box for this config.
	m.killProc(tc.ID)

	localPort := vless.LocalPort
	if localPort == 0 {
		localPort = 10800 + (routingTableID(tc) % 200)
	}

	confPath, err := m.writeConf(tc, localPort)
	if err != nil {
		return fmt.Errorf("write sing-box config: %w", err)
	}

	singBox := m.cfg.SingBoxBin
	if singBox == "" {
		singBox = "sing-box"
	}

	cmd := exec.Command(singBox, "run", "-c", confPath)
	cmd.Stdout = os.Stderr // log to stderr for syslog capture
	cmd.Stderr = os.Stderr
	// Run in its own process group for clean teardown.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sing-box: %w", err)
	}

	m.mu.Lock()
	m.procMap[tc.ID] = cmd
	m.mu.Unlock()

	// Wait for SOCKS5 port to be ready.
	if !m.waitForPort(localPort, 10*time.Second) {
		_ = m.killProc(tc.ID)
		return fmt.Errorf("sing-box did not listen on :%d within timeout", localPort)
	}

	// Set up routing: ip rule + iptables to redirect marked traffic to SOCKS5.
	tableID := routingTableID(tc)
	fwmark := fwmarkForTable(tableID)
	if err := m.setupRouting(ctx, tc, localPort, tableID, int(fwmark)); err != nil {
		_ = m.killProc(tc.ID)
		return fmt.Errorf("setup routing: %w", err)
	}

	slog.Info("vless: sing-box up", "config", tc.ID, "socks5_port", localPort, "table", tableID)
	return nil
}

// Down stops the sing-box process and removes routing rules.
func (m *VLESSManager) Down(ctx context.Context, tc *config.TunnelConfig) error {
	m.teardownRouting(ctx, tc)
	return m.killProc(tc.ID)
}

// IsUp returns true if the sing-box process is running.
func (m *VLESSManager) IsUp(tc *config.TunnelConfig) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procMap == nil {
		return false
	}
	cmd, ok := m.procMap[tc.ID]
	if !ok || cmd == nil || cmd.Process == nil {
		return false
	}
	return cmd.ProcessState == nil // still running
}

// FWMark returns the routing fwmark for this VLESS config.
func (m *VLESSManager) FWMark(tc *config.TunnelConfig) uint32 {
	return fwmarkForTable(routingTableID(tc))
}

func (m *VLESSManager) killProc(id string) error {
	m.mu.Lock()
	if m.procMap == nil {
		m.mu.Unlock()
		return nil
	}
	cmd, ok := m.procMap[id]
	if !ok || cmd == nil || cmd.Process == nil {
		m.mu.Unlock()
		return nil
	}
	delete(m.procMap, id)
	m.mu.Unlock()

	// Kill the entire process group (outside the lock to avoid holding it during sleep).
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	time.Sleep(500 * time.Millisecond)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_, _ = cmd.Process.Wait()
	return nil
}

func (m *VLESSManager) waitForPort(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func (m *VLESSManager) setupRouting(ctx context.Context, tc *config.TunnelConfig, socksPort, tableID, fwmark int) error {
	fwStr := fmt.Sprint(fwmark)
	tblStr := fmt.Sprint(tableID)

	if err := runCmd(ctx, "ip", "rule", "add", "fwmark", fwStr,
		"table", tblStr, "priority", "100"); err != nil {
		return fmt.Errorf("ip rule add: %w", err)
	}

	if err := runCmd(ctx, "ip", "route", "add", "default",
		"via", "127.0.0.1",
		"table", tblStr); err != nil {
		// Roll back the rule we just added.
		_ = runCmd(ctx, "ip", "rule", "del", "fwmark", fwStr,
			"table", tblStr, "priority", "100")
		return fmt.Errorf("ip route add: %w", err)
	}

	// Use iptables REDIRECT to push marked TCP traffic to sing-box SOCKS5.
	if err := runCmd(ctx, "iptables", "-t", "nat", "-A", "OUTPUT",
		"-m", "mark", "--mark", fwStr,
		"-p", "tcp",
		"-j", "REDIRECT", "--to-ports", fmt.Sprint(socksPort)); err != nil {
		// Roll back routing entries.
		_ = runCmd(ctx, "ip", "route", "flush", "table", tblStr)
		_ = runCmd(ctx, "ip", "rule", "del", "fwmark", fwStr,
			"table", tblStr, "priority", "100")
		return fmt.Errorf("iptables REDIRECT: %w", err)
	}

	return nil
}

func (m *VLESSManager) teardownRouting(ctx context.Context, tc *config.TunnelConfig) {
	tableID := routingTableID(tc)
	fwmark := fwmarkForTable(tableID)
	localPort := tc.VLESS.LocalPort
	if localPort == 0 {
		localPort = 10800 + (tableID % 200)
	}

	_ = runCmd(ctx, "ip", "rule", "del", "fwmark", fmt.Sprint(fwmark),
		"table", fmt.Sprint(tableID), "priority", "100")
	_ = runCmd(ctx, "ip", "route", "flush", "table", fmt.Sprint(tableID))
	_ = runCmd(ctx, "iptables", "-t", "nat", "-D", "OUTPUT",
		"-m", "mark", "--mark", fmt.Sprint(fwmark),
		"-p", "tcp",
		"-j", "REDIRECT", "--to-ports", fmt.Sprint(localPort))
}

func (m *VLESSManager) confPath(tc *config.TunnelConfig) string {
	return filepath.Join("/tmp/vpn-watchdog", tc.ID+"-singbox.json")
}

func (m *VLESSManager) writeConf(tc *config.TunnelConfig, localPort int) (string, error) {
	vless := tc.VLESS
	if err := os.MkdirAll("/tmp/vpn-watchdog", 0700); err != nil {
		return "", err
	}

	outbound := singBoxOutbound{
		Tag:    "vless-out",
		Type:   "vless",
		Server: vless.Address,
		Port:   vless.Port,
		UUID:   vless.UUID,
		Flow:   vless.Flow,
	}

	// TLS configuration.
	if vless.Security == "tls" || vless.Security == "reality" {
		tls := &singBoxTLS{
			Enabled:    true,
			ServerName: vless.SNI,
		}
		if vless.Fingerprint != "" {
			tls.UTLS = &singBoxUTLS{
				Enabled:     true,
				Fingerprint: vless.Fingerprint,
			}
		}
		if vless.Security == "reality" {
			tls.Reality = &singBoxReality{
				Enabled:   true,
				PublicKey: vless.RealityPublicKey,
				ShortID:   vless.RealityShortID,
			}
		}
		if vless.ECH {
			tls.ECH = &singBoxECH{
				Enabled: true,
				Config:  vless.ECHConfig,
			}
		}
		outbound.TLS = tls
	}

	// Transport configuration.
	switch vless.Transport {
	case "ws":
		outbound.Transport = &singBoxTransport{
			Type: "ws",
			Path: vless.TransportPath,
		}
	case "grpc":
		outbound.Transport = &singBoxTransport{
			Type:        "grpc",
			ServiceName: vless.TransportPath,
		}
	case "httpupgrade":
		outbound.Transport = &singBoxTransport{
			Type: "httpupgrade",
			Path: vless.TransportPath,
		}
	case "quic":
		outbound.Transport = &singBoxTransport{Type: "quic"}
	case "h3":
		outbound.Transport = &singBoxTransport{Type: "http"}
	}
	if vless.DomainFronting && vless.FrontingHost != "" && outbound.Transport != nil {
		outbound.Transport.Host = vless.FrontingHost
		if outbound.Transport.Headers == nil {
			outbound.Transport.Headers = map[string]string{}
		}
		outbound.Transport.Headers["Host"] = vless.FrontingHost
	}

	cfg := singBoxConfig{
		Log: map[string]string{
			"level": "warn",
		},
		Inbounds: []singBoxInbound{
			{
				Tag:    "socks-in",
				Type:   "socks",
				Listen: "127.0.0.1",
				Port:   localPort,
				Sniff:  true,
			},
		},
		Outbounds: []singBoxOutbound{outbound},
		Route: map[string]interface{}{
			"final": "vless-out",
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}

	path := m.confPath(tc)
	return path, os.WriteFile(path, data, 0600)
}
