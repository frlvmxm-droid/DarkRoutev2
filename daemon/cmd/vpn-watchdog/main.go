// vpn-watchdog – OpenWrt daemon for automatic VPN failover across
// WireGuard, AmneziaWG, and VLESS protocols.
//
// Usage:
//
//	vpn-watchdog [-log-level debug] [-status-addr 127.0.0.1:8765]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
	"github.com/frlvmxm-droid/darkroute/daemon/internal/watchdog"
)

var (
	logLevel   = flag.String("log-level", "info", "Log level: debug|info|warn|error")
	statusAddr = flag.String("status-addr", "127.0.0.1:8765", "HTTP address for status/control API (empty to disable)")
	version    = "dev" // overridden by -ldflags at build time
)

func main() {
	flag.Parse()

	// Configure structured logging (goes to stderr → procd → logd).
	level := slog.LevelInfo
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	slog.Info("vpn-watchdog starting", "version", version)

	// Load configuration from UCI; falls back to built-in defaults.
	cfg := config.LoadDaemonConfigFromUCI()

	// Ensure runtime state directory exists (tmpfs on OpenWrt).
	if err := os.MkdirAll(cfg.StateDir, 0755); err != nil {
		slog.Error("cannot create state dir", "path", cfg.StateDir, "err", err)
		os.Exit(1)
	}

	// Write PID file so procd / init scripts can track the process.
	pidPath := cfg.StateDir + "/vpn-watchdog.pid"
	writePID(pidPath)
	defer os.Remove(pidPath)

	// Initialise the watchdog (probe engine, state machine, scoring DB, switch engine).
	wd := watchdog.New(cfg)

	// Start the JSON status/control HTTP API.
	if *statusAddr != "" {
		startAPI(*statusAddr, wd)
	}

	// Run the main event loop; blocks until SIGTERM/SIGINT or ctx cancel.
	ctx := context.Background()
	if err := wd.Run(ctx); err != nil && err != context.Canceled {
		slog.Error("watchdog exited with error", "err", err)
		os.Exit(1)
	}
	slog.Info("vpn-watchdog stopped")
}

// startAPI registers HTTP handlers and starts a non-blocking server.
func startAPI(addr string, wd *watchdog.Watchdog) {
	mux := http.NewServeMux()

	// GET /status – returns JSON status snapshot.
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status := wd.Status()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(status); err != nil {
			slog.Warn("status API encode error", "err", err)
		}
	})

	// GET /dpi – returns the last DPI detection result and learned variant count.
	mux.HandleFunc("/dpi", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status := wd.Status()
		type dpiResponse struct {
			BlockType    string    `json:"block_type"`
			ReasonCode   string    `json:"reason_code"`
			Confidence   float64   `json:"confidence"`
			Evidence     []string  `json:"evidence"`
			TestedAt     time.Time `json:"tested_at"`
			LearnedCount int       `json:"learned_variants_count"`
		}
		resp := dpiResponse{
			BlockType:    status.DPIBlockType,
			ReasonCode:   status.DPIReasonCode,
			Confidence:   status.DPIConfidence,
			Evidence:     status.DPIEvidence,
			TestedAt:     status.DPITestedAt,
			LearnedCount: status.LearnedCount,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Warn("dpi API encode error", "err", err)
		}
	})

	// GET /ai – returns AI advisor runtime status.
	mux.HandleFunc("/ai", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status := wd.Status()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(status.AIStatus); err != nil {
			slog.Warn("ai API encode error", "err", err)
		}
	})

	// GET /health – liveness check for procd watchdog.
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() {
		slog.Info("status API listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("status API error", "err", err)
		}
	}()
}

func writePID(path string) {
	data := fmt.Sprintf("%d\n", os.Getpid())
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		slog.Warn("failed to write PID file", "path", path, "err", err)
	}
}
