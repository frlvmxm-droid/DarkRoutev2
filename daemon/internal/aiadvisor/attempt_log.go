package aiadvisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// AttemptLog stores bounded history for AI context.
type AttemptLog struct {
	mu      sync.Mutex
	path    string
	entries []AttemptEntry
	maxSize int
}

func NewAttemptLog(stateDir string, maxSize int) *AttemptLog {
	if maxSize <= 0 {
		maxSize = 300
	}
	l := &AttemptLog{path: filepath.Join(stateDir, "dpi_attempts.json"), maxSize: maxSize}
	l.load()
	return l
}

func (l *AttemptLog) Add(e AttemptEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
	if len(l.entries) > l.maxSize {
		l.entries = l.entries[len(l.entries)-l.maxSize:]
	}
	l.saveLocked()
}

func (l *AttemptLog) Recent(n int) []AttemptEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.entries) {
		n = len(l.entries)
	}
	out := make([]AttemptEntry, n)
	copy(out, l.entries[len(l.entries)-n:])
	return out
}

func (l *AttemptLog) load() {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &l.entries)
}

func (l *AttemptLog) saveLocked() {
	_ = os.MkdirAll(filepath.Dir(l.path), 0755)
	b, err := json.MarshalIndent(l.entries, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(l.path, b, 0600)
}
