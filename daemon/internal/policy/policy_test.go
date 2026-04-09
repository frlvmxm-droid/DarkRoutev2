package policy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

func TestCollectSelectorsFromIPsAndFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "domains.txt")
	if err := os.WriteFile(file, []byte("localhost\n#comment\nlocalhost\n"), 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	m := New(config.DaemonConfig{
		VPNIPs:         []string{"1.1.1.1", "1.1.1.1"},
		VPNDomains:     []string{"localhost"},
		VPNDomainFiles: []string{file},
	})

	got := m.collectSelectors(context.Background())
	if len(got) < 2 {
		t.Fatalf("expected selectors from ip+domain sources, got %v", got)
	}
}

func TestNormalizeSelector(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "1.1.1.1", want: "1.1.1.1", ok: true},
		{in: " 1.1.1.1/24 ", want: "1.1.1.1/24", ok: true},
		{in: "2001:db8::1", ok: false},
		{in: "example.com", ok: false},
	}
	for _, tt := range tests {
		got, ok := normalizeSelector(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("normalizeSelector(%q) = (%q,%v), want (%q,%v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestDetectToolchainPrefersNFT(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(file string) (string, error) {
		switch file {
		case "nft":
			return "/usr/sbin/nft", nil
		default:
			return "", errors.New("missing")
		}
	}
	tc := detectToolchain()
	if tc.backend != backendNFT || tc.nft == "" {
		t.Fatalf("expected nft backend, got %+v", tc)
	}
}

func TestDetectToolchainFallsBackToIPTables(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(file string) (string, error) {
		switch file {
		case "ipset":
			return "/usr/sbin/ipset", nil
		case "iptables-nft":
			return "/usr/sbin/iptables-nft", nil
		default:
			return "", errors.New("missing")
		}
	}
	tc := detectToolchain()
	if tc.backend != backendIPTables || tc.iptables == "" || tc.ipset == "" {
		t.Fatalf("expected iptables backend, got %+v", tc)
	}
}
