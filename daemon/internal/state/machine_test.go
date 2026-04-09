package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestMachine(t *testing.T) *Machine {
	t.Helper()
	dir := t.TempDir()
	return New(dir)
}

// TestInitialState verifies a fresh Machine starts HEALTHY.
func TestInitialState(t *testing.T) {
	m := newTestMachine(t)
	if m.Current() != StateHealthy {
		t.Fatalf("want HEALTHY, got %s", m.Current())
	}
}

// TestHealthyToDegraded: 3 failures with threshold=3 moves HEALTHY→DEGRADED.
func TestHealthyToDegraded(t *testing.T) {
	m := newTestMachine(t)
	const deg = 3
	for i := 0; i < deg-1; i++ {
		s := m.RecordFailure(deg, 2)
		if s != StateHealthy {
			t.Fatalf("after %d failures want HEALTHY, got %s", i+1, s)
		}
	}
	s := m.RecordFailure(deg, 2)
	if s != StateDegraded {
		t.Fatalf("want DEGRADED after %d failures, got %s", deg, s)
	}
}

// TestDegradedToProbing: additional probingThreshold failures push DEGRADED→PROBING.
func TestDegradedToProbing(t *testing.T) {
	m := newTestMachine(t)
	const deg, prob = 3, 2
	total := deg + prob
	for i := 0; i < total-1; i++ {
		m.RecordFailure(deg, prob)
	}
	s := m.RecordFailure(deg, prob)
	if s != StateProbing {
		t.Fatalf("want PROBING after %d failures, got %s", total, s)
	}
}

// TestRecordSuccessResetsDegraded: success while DEGRADED returns to HEALTHY.
func TestRecordSuccessResetsDegraded(t *testing.T) {
	m := newTestMachine(t)
	const deg = 3
	for i := 0; i < deg; i++ {
		m.RecordFailure(deg, 2)
	}
	if m.Current() != StateDegraded {
		t.Fatal("precondition: expected DEGRADED")
	}
	m.RecordSuccess(deg)
	if m.Current() != StateHealthy {
		t.Fatalf("want HEALTHY after success, got %s", m.Current())
	}
	if m.ConsecutiveFails() != 0 {
		t.Fatalf("consecutive fails not reset, got %d", m.ConsecutiveFails())
	}
}

// TestSwitchSucceeded: PROBING→SWITCHING→HEALTHY and active config updated.
func TestSwitchSucceeded(t *testing.T) {
	m := newTestMachine(t)
	m.current = StateProbing // bypass normal transition for unit test
	m.EnterSwitching("cfg-a")
	if m.Current() != StateSwitching {
		t.Fatalf("want SWITCHING, got %s", m.Current())
	}
	m.SwitchSucceeded("cfg-a")
	if m.Current() != StateHealthy {
		t.Fatalf("want HEALTHY after success, got %s", m.Current())
	}
	if m.ActiveConfigID() != "cfg-a" {
		t.Fatalf("want active=cfg-a, got %s", m.ActiveConfigID())
	}
}

// TestSwitchFailedRetry: SwitchFailed returns true when below maxAttempts.
func TestSwitchFailedRetry(t *testing.T) {
	m := newTestMachine(t)
	m.current = StateProbing
	m.EnterSwitching("cfg-b")  // attempts = 1
	retry := m.SwitchFailed(3) // max=3, should retry
	if !retry {
		t.Fatal("want retry=true when attempts < max")
	}
	if m.Current() != StateProbing {
		t.Fatalf("want PROBING after failed retry, got %s", m.Current())
	}
}

// TestSwitchFailedGiveUp: SwitchFailed returns false at maxAttempts.
func TestSwitchFailedGiveUp(t *testing.T) {
	m := newTestMachine(t)
	m.current = StateProbing
	m.switchAttempts = 3
	retry := m.SwitchFailed(3)
	if retry {
		t.Fatal("want retry=false at max attempts")
	}
}

// TestInCooldown: InCooldown is true immediately after SwitchSucceeded.
func TestInCooldown(t *testing.T) {
	m := newTestMachine(t)
	m.current = StateProbing
	m.EnterSwitching("cfg-c")
	m.SwitchSucceeded("cfg-c")

	if !m.InCooldown(5 * time.Minute) {
		t.Fatal("want in-cooldown immediately after switch")
	}
	if m.InCooldown(0) {
		t.Fatal("zero-duration cooldown should never be active")
	}
}

// TestPersistAndRestore: state persists across New() calls.
func TestPersistAndRestore(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	m.current = StateProbing
	m.EnterSwitching("cfg-d")
	m.SwitchSucceeded("cfg-d")

	// Create a new Machine from the same dir.
	m2 := New(dir)
	if m2.Current() != StateHealthy {
		t.Fatalf("restored state want HEALTHY, got %s", m2.Current())
	}
	if m2.ActiveConfigID() != "cfg-d" {
		t.Fatalf("restored active want cfg-d, got %s", m2.ActiveConfigID())
	}
}

// TestPersistFileMode: saved state.json has mode 0600.
func TestPersistFileMode(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	m.SetActiveConfig("cfg-e")

	path := filepath.Join(dir, "state.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("state.json not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("want mode 0600, got %04o", perm)
	}
}
