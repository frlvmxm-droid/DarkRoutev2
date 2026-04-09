// Package policy applies user-defined domain/IP selectors for VPN routing.
package policy

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

const (
	ipsetName  = "vw_vpn_dst"
	chainTable = "mangle"
	chainName  = "OUTPUT"
	nftTable   = "vpn_watchdog"
	nftSet     = "vpn_dst"
	nftChain   = "output"
)

// Manager configures runtime selectors (domain/ip lists) into ipset/iptables.
type Manager struct {
	cfg         config.DaemonConfig
	toolchain   toolchain
	warnOnce    sync.Once
	dnsTimeout  time.Duration
	dnsResolver dnsResolver
}

func New(cfg config.DaemonConfig) *Manager {
	return &Manager{
		cfg:         cfg,
		toolchain:   detectToolchain(),
		dnsTimeout:  2 * time.Second,
		dnsResolver: netDefaultResolver{},
	}
}

// Apply creates/updates the ipset and mangle rule for the given fwmark.
// If no selectors are configured, it ensures old rules are removed.
func (m *Manager) Apply(ctx context.Context, fwmark uint32) error {
	if !m.toolchain.available() {
		m.warnOnce.Do(func() {
			slog.Warn("policy: no supported firewall backend found, selector enforcement disabled",
				"backend", m.toolchain.backend,
				"nft", m.toolchain.nft,
				"ipset", m.toolchain.ipset,
				"iptables", m.toolchain.iptables)
		})
		return nil
	}

	selectors := m.collectSelectors(ctx)
	if len(selectors) == 0 {
		return m.Clear(ctx, fwmark)
	}

	switch m.toolchain.backend {
	case backendNFT:
		if err := m.applyNFT(ctx, selectors, fwmark); err != nil {
			return err
		}
	default:
		if err := m.applyIPTables(ctx, selectors, fwmark); err != nil {
			return err
		}
	}

	slog.Info("policy: applied selectors", "count", len(selectors), "fwmark", fwmark)
	return nil
}

// Clear removes runtime policy rules.
func (m *Manager) Clear(ctx context.Context, fwmark uint32) error {
	if !m.toolchain.available() {
		return nil
	}
	switch m.toolchain.backend {
	case backendNFT:
		_ = m.run(ctx, m.toolchain.nft, "delete", "table", "inet", nftTable)
	default:
		_ = m.run(ctx, m.toolchain.iptables, "-t", chainTable, "-D", chainName,
			"-m", "set", "--match-set", ipsetName, "dst",
			"-j", "MARK", "--set-mark", fmt.Sprint(fwmark))
		_ = m.run(ctx, m.toolchain.ipset, "destroy", ipsetName)
	}
	return nil
}

func (m *Manager) collectSelectors(ctx context.Context) []string {
	seen := map[string]struct{}{}
	var out []string

	add := func(v string) {
		if norm, ok := normalizeSelector(v); ok {
			if _, ok := seen[norm]; ok {
				return
			}
			seen[norm] = struct{}{}
			out = append(out, norm)
			return
		}
		m.resolveDomainSelectors(ctx, v, seen, &out)
	}

	for _, ip := range m.cfg.VPNIPs {
		add(ip)
	}
	for _, d := range m.cfg.VPNDomains {
		add(d)
	}
	for _, path := range m.cfg.VPNDomainFiles {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			add(line)
		}
		_ = f.Close()
	}
	return out
}

func (m *Manager) run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) resolveDomainSelectors(ctx context.Context, domain string, seen map[string]struct{}, out *[]string) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return
	}
	lookupCtx, cancel := context.WithTimeout(ctx, m.dnsTimeout)
	defer cancel()
	ips, err := m.dnsResolver.LookupNetIP(lookupCtx, "ip4", domain)
	if err != nil {
		return
	}
	for _, ip := range ips {
		v := ip.String()
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		*out = append(*out, v)
	}
}

func normalizeSelector(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	if ip, err := netip.ParseAddr(v); err == nil && ip.Is4() {
		return ip.String(), true
	}
	if pfx, err := netip.ParsePrefix(v); err == nil && pfx.Addr().Is4() {
		return pfx.String(), true
	}
	return "", false
}

type toolchain struct {
	backend  backendType
	nft      string
	ipset    string
	iptables string
}

func (tc toolchain) available() bool {
	switch tc.backend {
	case backendNFT:
		return tc.nft != ""
	case backendIPTables:
		return tc.ipset != "" && tc.iptables != ""
	default:
		return false
	}
}

type backendType string

const (
	backendNone     backendType = "none"
	backendNFT      backendType = "nft"
	backendIPTables backendType = "iptables"
)

func detectToolchain() toolchain {
	if nft := firstAvailable("nft"); nft != "" {
		return toolchain{
			backend: backendNFT,
			nft:     nft,
		}
	}
	tc := toolchain{
		backend:  backendIPTables,
		ipset:    firstAvailable("ipset"),
		iptables: firstAvailable("iptables", "iptables-nft"),
	}
	if !tc.available() {
		tc.backend = backendNone
	}
	return tc
}

func firstAvailable(candidates ...string) string {
	for _, name := range candidates {
		if path, err := lookPath(name); err == nil && path != "" {
			return path
		}
	}
	return ""
}

type dnsResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type netDefaultResolver struct{}

func (netDefaultResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

var lookPath = exec.LookPath

func (m *Manager) applyIPTables(ctx context.Context, selectors []string, fwmark uint32) error {
	if err := m.run(ctx, m.toolchain.ipset, "create", ipsetName, "hash:net", "-exist"); err != nil {
		return err
	}
	if err := m.run(ctx, m.toolchain.ipset, "flush", ipsetName); err != nil {
		return err
	}
	for _, ip := range selectors {
		if err := m.run(ctx, m.toolchain.ipset, "add", ipsetName, ip, "-exist"); err != nil {
			slog.Warn("policy: failed to add selector to ipset", "selector", ip, "err", err)
		}
	}

	_ = m.run(ctx, m.toolchain.iptables, "-t", chainTable, "-D", chainName,
		"-m", "set", "--match-set", ipsetName, "dst",
		"-j", "MARK", "--set-mark", fmt.Sprint(fwmark))
	return m.run(ctx, m.toolchain.iptables, "-t", chainTable, "-A", chainName,
		"-m", "set", "--match-set", ipsetName, "dst",
		"-j", "MARK", "--set-mark", fmt.Sprint(fwmark))
}

func (m *Manager) applyNFT(ctx context.Context, selectors []string, fwmark uint32) error {
	// Keep policy in a dedicated table to avoid direct edits in fw4-managed tables.
	_ = m.run(ctx, m.toolchain.nft, "add", "table", "inet", nftTable)
	_ = m.run(ctx, m.toolchain.nft, "add", "chain", "inet", nftTable, nftChain, "{ type route hook output priority mangle ; policy accept ; }")
	_ = m.run(ctx, m.toolchain.nft, "add", "set", "inet", nftTable, nftSet, "{ type ipv4_addr ; flags interval ; }")

	if err := m.run(ctx, m.toolchain.nft, "flush", "set", "inet", nftTable, nftSet); err != nil {
		return err
	}
	for _, selector := range selectors {
		if err := m.run(ctx, m.toolchain.nft, "add", "element", "inet", nftTable, nftSet, "{ "+selector+" }"); err != nil {
			slog.Warn("policy: failed to add selector to nft set", "selector", selector, "err", err)
		}
	}
	if err := m.run(ctx, m.toolchain.nft, "flush", "chain", "inet", nftTable, nftChain); err != nil {
		return err
	}
	return m.run(ctx, m.toolchain.nft, "add", "rule", "inet", nftTable, nftChain,
		"ip", "daddr", "@"+nftSet, "meta", "mark", "set", fmt.Sprint(fwmark))
}
