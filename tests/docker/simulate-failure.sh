#!/usr/bin/env bash
# simulate-failure.sh — inject or remove WireGuard traffic blocking
# to test vpn-watchdog failover behaviour.
#
# Usage:
#   ./simulate-failure.sh drop      # block WG UDP on wan network
#   ./simulate-failure.sh restore   # restore connectivity
#   ./simulate-failure.sh status    # show current iptables rules

set -euo pipefail

WG_SERVER_IP="${WG_SERVER_IP:-172.30.0.10}"
WG_PORT="${WG_PORT:-51820}"
ROUTER_CONTAINER="${ROUTER_CONTAINER:-vw-router}"

action="${1:-status}"

case "$action" in
  drop)
    echo ">> Blocking WireGuard traffic (UDP $WG_SERVER_IP:$WG_PORT)..."
    docker exec "$ROUTER_CONTAINER" iptables -I OUTPUT \
      -d "$WG_SERVER_IP" -p udp --dport "$WG_PORT" -j DROP
    docker exec "$ROUTER_CONTAINER" iptables -I INPUT \
      -s "$WG_SERVER_IP" -p udp --sport "$WG_PORT" -j DROP
    echo ">> Done. Watch vpn-watchdog logs:"
    echo "   docker exec $ROUTER_CONTAINER tail -f /tmp/log/vpn-watchdog"
    ;;

  restore)
    echo ">> Restoring WireGuard connectivity..."
    docker exec "$ROUTER_CONTAINER" iptables -D OUTPUT \
      -d "$WG_SERVER_IP" -p udp --dport "$WG_PORT" -j DROP 2>/dev/null || true
    docker exec "$ROUTER_CONTAINER" iptables -D INPUT \
      -s "$WG_SERVER_IP" -p udp --sport "$WG_PORT" -j DROP 2>/dev/null || true
    echo ">> Connectivity restored."
    ;;

  partial)
    echo ">> Simulating 40% packet loss on WireGuard UDP..."
    docker exec "$ROUTER_CONTAINER" tc qdisc add dev eth0 root netem loss 40%
    echo ">> Loss applied. vpn-watchdog should enter DEGRADED but NOT switch."
    ;;

  restore-partial)
    docker exec "$ROUTER_CONTAINER" tc qdisc del dev eth0 root 2>/dev/null || true
    echo ">> Packet loss removed."
    ;;

  status)
    echo "=== iptables OUTPUT ==="
    docker exec "$ROUTER_CONTAINER" iptables -L OUTPUT -n -v 2>/dev/null || echo "(container not running)"
    echo "=== vpn-watchdog state ==="
    cat /tmp/vpn-watchdog/state.json 2>/dev/null || \
      docker exec "$ROUTER_CONTAINER" cat /tmp/vpn-watchdog/state.json 2>/dev/null || \
      echo "(state file not found)"
    ;;

  *)
    echo "Usage: $0 drop|restore|partial|restore-partial|status"
    exit 1
    ;;
esac
