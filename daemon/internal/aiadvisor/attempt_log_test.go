package aiadvisor

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAttemptLogBounds(t *testing.T) {
	dir := t.TempDir()
	log := NewAttemptLog(dir, 2)
	log.Add(AttemptEntry{Timestamp: time.Now(), ConfigID: "a"})
	log.Add(AttemptEntry{Timestamp: time.Now(), ConfigID: "b"})
	log.Add(AttemptEntry{Timestamp: time.Now(), ConfigID: "c"})
	r := log.Recent(10)
	if len(r) != 2 || r[0].ConfigID != "b" || r[1].ConfigID != "c" {
		t.Fatalf("unexpected bounded log: %+v", r)
	}
	if _, err := filepath.Abs(filepath.Join(dir, "dpi_attempts.json")); err != nil {
		t.Fatalf("expected persisted file path to be valid: %v", err)
	}
}
