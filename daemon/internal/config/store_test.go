package config

import "testing"

func TestAppendDomainProbeTargetsAddsUniqueLimitedEntries(t *testing.T) {
	base := []ProbeTarget{
		{Host: "1.1.1.1", Type: ProbeICMP},
		{Host: "already.example", Type: ProbeHTTPS, Port: 443},
	}
	domains := []string{"already.example", "one.example", "two.example", "three.example", "four.example"}

	out := appendDomainProbeTargets(base, domains, 3)

	// 2 base + 3 new (unique and limited).
	if len(out) != 5 {
		t.Fatalf("expected 5 targets, got %d", len(out))
	}

	// Ensure added targets are HTTPS:443.
	for _, tgt := range out {
		if tgt.Host == "one.example" || tgt.Host == "two.example" || tgt.Host == "three.example" {
			if tgt.Type != ProbeHTTPS || tgt.Port != 443 {
				t.Fatalf("unexpected target for %s: type=%s port=%d", tgt.Host, tgt.Type, tgt.Port)
			}
		}
	}
}

func TestAppendDomainProbeTargetsNoopForEmptyInput(t *testing.T) {
	base := []ProbeTarget{{Host: "1.1.1.1", Type: ProbeICMP}}
	out := appendDomainProbeTargets(base, nil, 3)
	if len(out) != len(base) {
		t.Fatalf("expected no change, got %d", len(out))
	}
}

