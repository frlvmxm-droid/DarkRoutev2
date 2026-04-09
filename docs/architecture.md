# vpn-watchdog — Architecture

## Overview

`vpn-watchdog` is an OpenWrt daemon that maintains internet connectivity
by automatically switching between VPN configurations when the active
tunnel becomes unreachable.

```
┌─────────────────────────────────────────────────────────────────┐
│                         vpn-watchdog                            │
│                                                                 │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────────┐  │
│  │ Probe Engine │───▶│ State Machine│───▶│  Switch Engine   │  │
│  │              │    │              │    │                  │  │
│  │ ICMP / TCP / │    │  HEALTHY     │    │ parallel probe   │  │
│  │ HTTP / HTTPS │    │  DEGRADED    │    │ score & rank     │  │
│  │              │    │  PROBING     │    │ apply + verify   │  │
│  │ fwmark-based │    │  SWITCHING   │    │ rollback support │  │
│  │ routing      │    │              │    │                  │  │
│  └──────────────┘    └──────────────┘    └──────────────────┘  │
│         │                   │                     │            │
│         └───────────────────┴─────────────────────┘            │
│                             │                                   │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────────┐  │
│  │ Config Store │    │ Scoring DB   │    │ Tunnel Managers  │  │
│  │              │    │              │    │                  │  │
│  │ JSON files   │    │ EWMA RTT     │    │ WireGuard        │  │
│  │ /etc/vpn-    │    │ packet loss  │    │ AmneziaWG        │  │
│  │ watchdog/    │    │ session      │    │ VLESS/sing-box   │  │
│  │ configs/     │    │ success rate │    │                  │  │
│  └──────────────┘    └──────────────┘    └──────────────────┘  │
│                                                                 │
│  HTTP status API  /etc/init.d/vpn-watchdog  LuCI web UI        │
└─────────────────────────────────────────────────────────────────┘
```

## State Machine

```
         ┌─────────────────────────────────────────────┐
         │                                             │
         ▼                                             │
    ┌─────────┐   ≥N failures    ┌──────────┐          │
    │ HEALTHY │ ─────────────── ▶│ DEGRADED │          │
    │         │ ◀──────────────  │          │          │
    └─────────┘  success         └──────────┘          │
                                      │                │
                               ≥M more│                │
                               failures│               │
                                      ▼                │
                               ┌──────────┐            │
                               │ PROBING  │            │
                               │          │            │
                               └──────────┘            │
                                      │                │
                            best config│               │
                            selected   │               │
                                      ▼                │
                               ┌──────────┐            │
                               │SWITCHING │            │
                               │          │            │
                               └──────────┘            │
                                      │                │
                              verified│ working        │
                                      └────────────────┘
```

| State     | Probe interval | Transition triggers                               |
|-----------|---------------|---------------------------------------------------|
| HEALTHY   | 30 s (default)| ≥3 consecutive failures → DEGRADED                |
| DEGRADED  | 10 s          | ≥3 more failures → PROBING; any success → HEALTHY |
| PROBING   | 5 s           | Best config selected → SWITCHING                  |
| SWITCHING | —             | Verify OK → HEALTHY; verify fail → PROBING        |

## Probe Engine

Probes run concurrently against all configured targets using SO_MARK to
bind sockets to a specific routing table (each tunnel gets its own table).
This allows testing connectivity through a specific VPN interface without
affecting the system default route.

Result aggregation: majority vote (>50% targets must succeed).

## Scoring Engine

Each config is ranked by a weighted multi-criteria score (with EWMA inputs, α=0.3):

```
final = 0.40*availability_dpi + 0.35*performance + 0.15*security + 0.10*reliability
```

Where:
- `availability_dpi`: combines historical DPI/TSPU bypass success with session success,
- `performance`: derived from RTT and packet loss,
- `security`: protocol-aware security baseline (e.g. VLESS Reality > TLS > none),
- `reliability`: session success weighted by sample confidence.

Higher score = tried first in PROBING. New configs start from neutral defaults.

## Tunnel Managers

### WireGuard
- Generates `/tmp/vpn-watchdog/<iface>.conf` and calls `wg-quick up/down`
- Uses `Table = <id>` directive so WG manages its own routing table
- `PostUp` adds `ip rule` for fwmark-based routing
- DPI auto-tune can generate endpoint-port variants (e.g. 443/80/53) and MTU variants

### AmneziaWG
- Same as WireGuard but uses `awg-quick` and writes extra Jc/Jmin/Jmax/S1/S2/H1-H4 fields
- Requires `amnezia-wireguard` kernel module and `awg-tools` package

### VLESS (via sing-box)
- Launches `sing-box run -c <config>` as a child process
- sing-box exposes local SOCKS5 port
- `iptables REDIRECT` routes marked traffic to SOCKS5 port
- Teardown: SIGTERM → SIGKILL + iptables cleanup
- DPI auto-tune rotates TLS fingerprints/transports and can try camouflage ports (443/80/53)

## Routing Architecture

```
packet from LAN
    │
    ▼  ip rule: if fwmark == TABLE_ID → use routing table TABLE_ID
routing table TABLE_ID
    │
    ├── WG/AWG: ip route default dev wg0   (kernel WG routes)
    └── VLESS:  iptables REDIRECT → 127.0.0.1:SOCKS5_PORT → sing-box
```

Each tunnel uses a unique routing table ID (100–200, derived from config ID).
The SO_MARK value equals the table ID. This enables:
- Independent connectivity testing per tunnel
- Zero-disruption parallel probing (no traffic mixing)

## Security Notes

- Config files containing private keys are stored at mode 0600
- The state directory (`/tmp/vpn-watchdog/`) is mode 0700
- PSK (pre-shared keys) are written to tmpfs temp files, not flash
- The LuCI API strips private keys from config list responses
- The status API binds to 127.0.0.1 only (not exposed on WAN)

## Package Dependencies

| Package          | Purpose                                  |
|------------------|------------------------------------------|
| wireguard-tools  | `wg-quick` for WG tunnel management      |
| kmod-wireguard   | WireGuard kernel module                  |
| sing-box         | VLESS tunnel runtime                     |
| amnezia-wg       | AmneziaWG kernel module (optional)       |
| awg-tools        | `awg-quick` for AWG management (optional)|
| curl             | HTTP probing                             |
| iputils-ping     | ICMP probing fallback                    |
| iptables         | Traffic routing for VLESS                |
| iproute2         | Policy routing (`ip rule`, `ip route`)   |


## Optional AI Advisor

When standard DPI variants fail, an optional external LLM advisor can receive structured diagnostics (last DPI result + bounded attempt history) and return a constrained recommendation.

Safety model:
- recommendation-only (no shell execution),
- strict allowlist validation for tunable fields,
- rate limits and minimum confidence threshold,
- temporary presets with verify + cooldown rollback.
