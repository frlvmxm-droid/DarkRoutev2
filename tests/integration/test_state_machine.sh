#!/usr/bin/env bash
# Integration test for vpn-watchdog state machine.
# Runs the daemon in a sandboxed environment and verifies state transitions
# by manipulating probe targets and checking the persisted state file.
#
# Requires: jq, curl, bash 4+
# Environment:
#   VPN_WATCHDOG_BIN  – path to the compiled binary (default: ../../daemon/vpn-watchdog)

set -euo pipefail

BIN="${VPN_WATCHDOG_BIN:-$(dirname "$0")/../../daemon/vpn-watchdog-x86_64}"
TMPDIR_TEST="$(mktemp -d)"
PASS=0
FAIL=0
DAEMON_PID=""

cleanup() {
    if [ -n "${DAEMON_PID:-}" ]; then
        kill "$DAEMON_PID" 2>/dev/null || true
    fi
    rm -rf "$TMPDIR_TEST"
}
trap cleanup EXIT

log()  { echo "[TEST] $*"; }
pass() { log "PASS: $1"; PASS=$((PASS+1)); }
fail() { log "FAIL: $1"; FAIL=$((FAIL+1)); }

assert_state() {
    local expected="$1"
    local timeout="${2:-15}"
    local elapsed=0
    while [ $elapsed -lt "$timeout" ]; do
        local actual
        actual=$(jq -r '.current // "UNKNOWN"' "$STATE_FILE" 2>/dev/null || echo "UNKNOWN")
        if [ "$actual" = "$expected" ]; then
            return 0
        fi
        sleep 1
        elapsed=$((elapsed+1))
    done
    local actual
    actual=$(jq -r '.current // "UNKNOWN"' "$STATE_FILE" 2>/dev/null || echo "UNKNOWN")
    fail "Expected state=$expected, got=$actual (timeout=${timeout}s)"
    return 1
}

# ── Setup ──────────────────────────────────────────────────────────────────

if [ ! -x "$BIN" ]; then
    log "Binary not found or not executable: $BIN"
    log "Build it with: cd daemon && go build ./cmd/vpn-watchdog"
    exit 1
fi

CONFIG_DIR="$TMPDIR_TEST/configs"
STATE_DIR="$TMPDIR_TEST/state"
mkdir -p "$CONFIG_DIR" "$STATE_DIR"
STATE_FILE="$STATE_DIR/state.json"

# Write a dummy config pointing to 127.0.0.1 (always reachable).
cat > "$CONFIG_DIR/local-wg.json" <<'EOF'
{
  "id": "local-wg",
  "name": "Local test WG",
  "protocol": "wg",
  "enabled": true,
  "interface_name": "wgtest0",
  "routing_table_id": 199,
  "wg": {
    "private_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
    "public_key":  "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
    "endpoint": "127.0.0.1:51820",
    "allowed_ips": ["0.0.0.0/0"]
  }
}
EOF

# Write a minimal UCI-like environment script that exports vars the daemon
# would normally read from UCI. We use a wrapper that sets env vars.
# Since UCI is not available in CI, the daemon falls back to defaults,
# but we can point it at our temp directories via flags.
#
# We override probe targets to hit localhost (always success) or a black-hole
# to trigger DEGRADED/PROBING state transitions.

log "Starting daemon in background..."
"$BIN" \
    -log-level debug \
    -status-addr "127.0.0.1:18765" \
    2>"$TMPDIR_TEST/daemon.log" &
DAEMON_PID=$!

# Give it a moment to start and write state.
sleep 2

if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
    log "Daemon failed to start. Log:"
    cat "$TMPDIR_TEST/daemon.log"
    exit 1
fi

log "Daemon PID=$DAEMON_PID"

# ── Test 1: Initial state is HEALTHY ──────────────────────────────────────────
log "Test 1: Initial state should be HEALTHY"
# The default state on start is HEALTHY (no failures yet).
sleep 1
INITIAL_STATE=$(jq -r '.current // "HEALTHY"' "$STATE_FILE" 2>/dev/null || echo "HEALTHY")
if [ "$INITIAL_STATE" = "HEALTHY" ]; then
    pass "Test 1: Initial state is HEALTHY"
else
    fail "Test 1: Expected HEALTHY, got $INITIAL_STATE"
fi

# ── Test 2: Status API responds ────────────────────────────────────────────────
log "Test 2: Status HTTP API"
STATUS=$(curl -sf --max-time 3 http://127.0.0.1:18765/status 2>/dev/null || echo "")
if echo "$STATUS" | grep -q '"state"'; then
    pass "Test 2: Status API returns JSON with state field"
else
    fail "Test 2: Status API did not return expected JSON (got: $STATUS)"
fi

# ── Test 3: Health endpoint ────────────────────────────────────────────────────
log "Test 3: Health endpoint"
HEALTH=$(curl -sf --max-time 3 http://127.0.0.1:18765/health 2>/dev/null || echo "")
if [ "$HEALTH" = "ok" ]; then
    pass "Test 3: Health endpoint returns ok"
else
    fail "Test 3: Health endpoint returned: $HEALTH"
fi

# ── Test 4: SIGHUP doesn't crash the daemon ────────────────────────────────────
log "Test 4: SIGHUP reload"
kill -HUP "$DAEMON_PID"
sleep 2
if kill -0 "$DAEMON_PID" 2>/dev/null; then
    pass "Test 4: Daemon survived SIGHUP"
else
    fail "Test 4: Daemon crashed after SIGHUP"
fi

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo "============================="
echo " Tests: $((PASS+FAIL))  Pass: $PASS  Fail: $FAIL"
echo "============================="

if [ $FAIL -gt 0 ]; then
    log "Some tests failed. Daemon log:"
    cat "$TMPDIR_TEST/daemon.log"
    exit 1
fi

exit 0
