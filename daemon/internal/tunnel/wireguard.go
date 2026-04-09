package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

// WGManager manages standard WireGuard tunnels using wg-quick / UCI.
type WGManager struct {
	cfg config.DaemonConfig
}

const wgConfTemplate = `[Interface]
PrivateKey = {{ .PrivateKey }}
Address = 10.0.0.2/32
MTU = {{ .MTU }}
Table = {{ .TableID }}
PostUp = ip rule add fwmark {{ .FWMark }} table {{ .TableID }} priority 100
PreDown = ip rule del fwmark {{ .FWMark }} table {{ .TableID }} priority 100

[Peer]
PublicKey = {{ .PublicKey }}
{{ if .PresharedKey -}}
PresharedKeyFile = {{ .PSKFile }}
{{- end }}
AllowedIPs = {{ .AllowedIPs }}
Endpoint = {{ .Endpoint }}
{{ if gt .Keepalive 0 -}}
PersistentKeepalive = {{ .Keepalive }}
{{- end }}
`

type wgConfData struct {
	PrivateKey   string
	PublicKey    string
	PresharedKey string
	PSKFile      string
	Endpoint     string
	AllowedIPs   string
	MTU          int
	TableID      int
	FWMark       uint32
	Keepalive    int
}

// Up renders a wg-quick config and brings up the interface.
func (m *WGManager) Up(ctx context.Context, tc *config.TunnelConfig) error {
	wgc := tc.WireGuard
	if wgc == nil {
		return fmt.Errorf("wg config is nil for %s", tc.ID)
	}

	tableID := routingTableID(tc)
	fwmark := fwmarkForTable(tableID)
	mtu := tc.MTU
	if mtu == 0 {
		mtu = 1420
	}

	data := wgConfData{
		PrivateKey:  wgc.PrivateKey,
		PublicKey:   wgc.PublicKey,
		Endpoint:    wgc.Endpoint,
		AllowedIPs:  strings.Join(wgc.AllowedIPs, ", "),
		MTU:         mtu,
		TableID:     tableID,
		FWMark:      fwmark,
		Keepalive:   wgc.PersistentKeepalive,
	}

	// Write PSK to a temp file with tight permissions.
	if wgc.PresharedKey != "" {
		pskPath := filepath.Join("/tmp/vpn-watchdog", tc.ID+"-psk")
		if err := os.WriteFile(pskPath, []byte(wgc.PresharedKey+"\n"), 0600); err != nil {
			return fmt.Errorf("write psk: %w", err)
		}
		data.PSKFile = pskPath
	}

	confPath, err := m.writeConf(tc, data)
	if err != nil {
		return err
	}

	// Bring down first if already exists.
	if interfaceExists(tc.InterfaceName) {
		_ = runCmd(ctx, "wg-quick", "down", confPath)
	}

	if err := runCmd(ctx, "wg-quick", "up", confPath); err != nil {
		return fmt.Errorf("wg-quick up: %w", err)
	}

	if !waitForInterface(tc.InterfaceName, 5*1e9) {
		return fmt.Errorf("interface %s did not appear after wg-quick up", tc.InterfaceName)
	}

	slog.Info("wg: tunnel up", "iface", tc.InterfaceName, "table", tableID)
	return nil
}

// Down tears down the WireGuard interface.
func (m *WGManager) Down(ctx context.Context, tc *config.TunnelConfig) error {
	confPath := m.confPath(tc)
	if _, err := os.Stat(confPath); os.IsNotExist(err) {
		// No config on disk – use ip link del.
		if interfaceExists(tc.InterfaceName) {
			return runCmd(ctx, "ip", "link", "del", tc.InterfaceName)
		}
		return nil
	}
	return runCmd(ctx, "wg-quick", "down", confPath)
}

// IsUp returns true if the interface exists and has a peer.
func (m *WGManager) IsUp(tc *config.TunnelConfig) bool {
	return interfaceExists(tc.InterfaceName)
}

// FWMark returns the SO_MARK value for this tunnel.
func (m *WGManager) FWMark(tc *config.TunnelConfig) uint32 {
	return fwmarkForTable(routingTableID(tc))
}

func (m *WGManager) confPath(tc *config.TunnelConfig) string {
	return filepath.Join("/tmp/vpn-watchdog", tc.InterfaceName+".conf")
}

func (m *WGManager) writeConf(tc *config.TunnelConfig, data wgConfData) (string, error) {
	if err := os.MkdirAll("/tmp/vpn-watchdog", 0700); err != nil {
		return "", err
	}
	tmpl, err := template.New("wg").Parse(wgConfTemplate)
	if err != nil {
		return "", err
	}
	path := m.confPath(tc)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := tmpl.Execute(f, data); err != nil {
		return "", err
	}
	return path, nil
}
