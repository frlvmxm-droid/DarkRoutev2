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

// AWGManager manages AmneziaWG tunnels using awg-quick.
// AmneziaWG is a WireGuard fork with obfuscation; its tooling mirrors wg-quick
// but reads extra Amnezia-specific fields in the [Interface] section.
type AWGManager struct {
	cfg config.DaemonConfig
}

const awgConfTemplate = `[Interface]
PrivateKey = {{ .PrivateKey }}
Address = 10.0.0.2/32
MTU = {{ .MTU }}
Table = {{ .TableID }}
Jc = {{ .Jc }}
Jmin = {{ .Jmin }}
Jmax = {{ .Jmax }}
S1 = {{ .S1 }}
S2 = {{ .S2 }}
H1 = {{ .H1 }}
H2 = {{ .H2 }}
H3 = {{ .H3 }}
H4 = {{ .H4 }}
PostUp = ip rule add fwmark {{ .FWMark }} table {{ .TableID }} priority 100
PreDown = ip rule del fwmark {{ .FWMark }} table {{ .TableID }} priority 100

[Peer]
PublicKey = {{ .PublicKey }}
{{ if .PresharedKey -}}
PresharedKey = {{ .PresharedKey }}
{{- end }}
AllowedIPs = {{ .AllowedIPs }}
Endpoint = {{ .Endpoint }}
{{ if gt .Keepalive 0 -}}
PersistentKeepalive = {{ .Keepalive }}
{{- end }}
`

type awgConfData struct {
	PrivateKey   string
	PublicKey    string
	PresharedKey string
	Endpoint     string
	AllowedIPs   string
	MTU          int
	TableID      int
	FWMark       uint32
	Keepalive    int
	// Amnezia obfuscation.
	Jc   int // JunkPacketCount
	Jmin int // JunkPacketMinSize
	Jmax int // JunkPacketMaxSize
	S1   int // InitPacketJunkSize
	S2   int // ResponsePacketJunkSize
	H1   int // InitPacketMagicHeader
	H2   int // ResponsePacketMagicHeader
	H3   int // UnderLoadPacketMagicHeader
	H4   int // TransportPacketMagicHeader
}

// Up writes an awg-quick config file and brings the interface up.
func (m *AWGManager) Up(ctx context.Context, tc *config.TunnelConfig) error {
	awg := tc.AmneziaWG
	if awg == nil {
		return fmt.Errorf("awg config is nil for %s", tc.ID)
	}

	tableID := routingTableID(tc)
	fwmark := fwmarkForTable(tableID)
	mtu := tc.MTU
	if mtu == 0 {
		mtu = 1420
	}

	data := awgConfData{
		PrivateKey:   awg.PrivateKey,
		PublicKey:    awg.PublicKey,
		PresharedKey: awg.PresharedKey,
		Endpoint:     awg.Endpoint,
		AllowedIPs:   strings.Join(awg.AllowedIPs, ", "),
		MTU:          mtu,
		TableID:      tableID,
		FWMark:       fwmark,
		Keepalive:    awg.PersistentKeepalive,
		Jc:           awg.JunkPacketCount,
		Jmin:         awg.JunkPacketMinSize,
		Jmax:         awg.JunkPacketMaxSize,
		S1:           awg.InitPacketJunkSize,
		S2:           awg.ResponsePacketJunkSize,
		H1:           awg.InitPacketMagicHeader,
		H2:           awg.ResponsePacketMagicHeader,
		H3:           awg.UnderLoadPacketMagicHeader,
		H4:           awg.TransportPacketMagicHeader,
	}

	confPath, err := m.writeConf(tc, data)
	if err != nil {
		return err
	}

	// Determine awg-quick binary.
	awgQuick := "awg-quick"
	if m.cfg.AWGBin != "" {
		awgQuick = m.cfg.AWGBin + "-quick"
	}

	if interfaceExists(tc.InterfaceName) {
		_ = runCmd(ctx, awgQuick, "down", confPath)
	}

	if err := runCmd(ctx, awgQuick, "up", confPath); err != nil {
		return fmt.Errorf("awg-quick up: %w", err)
	}

	if !waitForInterface(tc.InterfaceName, 5*1e9) {
		return fmt.Errorf("interface %s did not appear after awg-quick up", tc.InterfaceName)
	}

	slog.Info("awg: tunnel up", "iface", tc.InterfaceName, "table", tableID)
	return nil
}

// Down tears down the AmneziaWG interface.
func (m *AWGManager) Down(ctx context.Context, tc *config.TunnelConfig) error {
	awgQuick := "awg-quick"
	if m.cfg.AWGBin != "" {
		awgQuick = m.cfg.AWGBin + "-quick"
	}
	confPath := m.confPath(tc)
	if _, err := os.Stat(confPath); os.IsNotExist(err) {
		if interfaceExists(tc.InterfaceName) {
			return runCmd(ctx, "ip", "link", "del", tc.InterfaceName)
		}
		return nil
	}
	return runCmd(ctx, awgQuick, "down", confPath)
}

// IsUp returns true if the AWG interface exists.
func (m *AWGManager) IsUp(tc *config.TunnelConfig) bool {
	return interfaceExists(tc.InterfaceName)
}

// FWMark returns the SO_MARK for this tunnel.
func (m *AWGManager) FWMark(tc *config.TunnelConfig) uint32 {
	return fwmarkForTable(routingTableID(tc))
}

func (m *AWGManager) confPath(tc *config.TunnelConfig) string {
	return filepath.Join("/tmp/vpn-watchdog", tc.InterfaceName+"-awg.conf")
}

func (m *AWGManager) writeConf(tc *config.TunnelConfig, data awgConfData) (string, error) {
	if err := os.MkdirAll("/tmp/vpn-watchdog", 0700); err != nil {
		return "", err
	}
	tmpl, err := template.New("awg").Parse(awgConfTemplate)
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
