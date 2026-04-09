// Package tunnel provides a unified interface for managing VPN tunnels.
package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

// vlessSingleton is the package-level shared VLESSManager so that procMap
// (sing-box child processes) survives across multiple ForConfig() calls.
var (
	vlessMu      sync.Mutex
	vlessSingleton *VLESSManager
)

// Manager is implemented by each tunnel backend.
type Manager interface {
	// Up brings the tunnel up using the provided config.
	Up(ctx context.Context, tc *config.TunnelConfig) error
	// Down tears the tunnel down.
	Down(ctx context.Context, tc *config.TunnelConfig) error
	// IsUp returns true if the tunnel interface is currently operational.
	IsUp(tc *config.TunnelConfig) bool
	// FWMark returns the SO_MARK / routing-table mark used for this tunnel's
	// traffic, so the probe engine can bind sockets to the right table.
	FWMark(tc *config.TunnelConfig) uint32
}

// ForConfig returns the appropriate Manager for the tunnel's protocol.
func ForConfig(tc *config.TunnelConfig, cfg config.DaemonConfig) (Manager, error) {
	switch tc.Protocol {
	case config.ProtocolWireGuard:
		return &WGManager{cfg: cfg}, nil
	case config.ProtocolAmneziaWG:
		return &AWGManager{cfg: cfg}, nil
	case config.ProtocolVLESS:
		vlessMu.Lock()
		if vlessSingleton == nil {
			vlessSingleton = &VLESSManager{cfg: cfg, procMap: make(map[string]*exec.Cmd)}
		} else {
			// Update config in case daemon was reloaded.
			vlessSingleton.cfg = cfg
		}
		m := vlessSingleton
		vlessMu.Unlock()
		return m, nil
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", tc.Protocol)
	}
}

// runCmd executes a shell command, logging output on failure.
func runCmd(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("command failed", "cmd", name, "args", args,
			"output", strings.TrimSpace(string(out)), "err", err)
		return fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
	}
	if len(out) > 0 {
		slog.Debug("command output", "cmd", name, "output", strings.TrimSpace(string(out)))
	}
	return nil
}

// interfaceExists checks if a network interface is present in the kernel.
func interfaceExists(name string) bool {
	_, err := os.Stat("/sys/class/net/" + name)
	return err == nil
}

// waitForInterface polls until the interface appears or timeout expires.
func waitForInterface(name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if interfaceExists(name) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// routingTableID returns the routing table ID for a tunnel config.
// Uses RoutingTableID from config, or derives one from the fwmark range 100–200.
func routingTableID(tc *config.TunnelConfig) int {
	if tc.RoutingTableID > 0 {
		return tc.RoutingTableID
	}
	// Derive a stable ID from the config ID string.
	h := 0
	for _, c := range tc.ID {
		h = h*31 + int(c)
	}
	return 100 + (h%100+100)%100
}

// fwmarkForTable returns the fwmark corresponding to a routing table ID.
// Convention: fwmark == tableID.
func fwmarkForTable(tableID int) uint32 {
	return uint32(tableID)
}
