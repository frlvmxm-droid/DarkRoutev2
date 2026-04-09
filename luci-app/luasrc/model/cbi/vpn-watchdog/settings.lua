-- CBI model for vpn-watchdog global settings
local m, s, o

m = Map("vpn-watchdog", translate("VPN Watchdog Settings"),
    translate("Configure the VPN Watchdog daemon behaviour and probe targets."))

-- ── Global settings ───────────────────────────────────────────────────────────
s = m:section(NamedSection, "global", "vpn-watchdog", translate("General"))
s.addremove = false
s.anonymous = false

o = s:option(Flag, "enabled", translate("Enable"), translate("Start vpn-watchdog on boot"))
o.rmempty = false

o = s:option(Value, "probe_interval_healthy", translate("Healthy probe interval (s)"),
    translate("How often to probe when everything is working (default: 30)"))
o.datatype = "uinteger"
o.default = "30"

o = s:option(Value, "probe_interval_degraded", translate("Degraded probe interval (s)"),
    translate("Faster probe interval after first failures detected (default: 10)"))
o.datatype = "uinteger"
o.default = "10"

o = s:option(Value, "degraded_threshold", translate("Failures → DEGRADED"),
    translate("Consecutive failures before entering DEGRADED state (default: 3)"))
o.datatype = "uinteger"
o.default = "3"

o = s:option(Value, "probing_threshold", translate("Additional failures → PROBING"),
    translate("Additional failures in DEGRADED before switching configs (default: 3)"))
o.datatype = "uinteger"
o.default = "3"

o = s:option(Value, "switch_verify_timeout", translate("Switch verify timeout (s)"),
    translate("Seconds to wait after applying a config before verifying (default: 60)"))
o.datatype = "uinteger"
o.default = "60"

o = s:option(Value, "post_switch_cooldown", translate("Post-switch cooldown (s)"),
    translate("Seconds after a successful switch before re-enabling degraded detection (default: 90)"))
o.datatype = "uinteger"
o.default = "90"

o = s:option(Value, "max_switch_attempts", translate("Max switch attempts"),
    translate("Maximum consecutive config switches before backing off (default: 3)"))
o.datatype = "uinteger"
o.default = "3"

o = s:option(Value, "config_dir", translate("Config directory"),
    translate("Directory with tunnel config JSON files"))
o.default = "/etc/vpn-watchdog/configs"

o = s:option(Value, "sing_box_bin", translate("sing-box binary path"))
o.default = "/usr/bin/sing-box"

o = s:option(DynamicList, "vpn_domain", translate("VPN domains"),
    translate("Domains that should be treated as priority bypass targets through VPN (one per entry)."))
o.placeholder = "example.com"

o = s:option(DynamicList, "vpn_ip", translate("VPN IPs / subnets"),
    translate("IPs or CIDRs that should be routed via VPN in policy-enabled mode."))
o.placeholder = "203.0.113.5 or 203.0.113.0/24"

o = s:option(DynamicList, "vpn_domain_file", translate("Domain list files"),
    translate("Paths to local files with domain lists (one domain per line) for VPN bypass policies."))
o.placeholder = "/etc/vpn-watchdog/lists/blocked-domains.txt"

-- ── DPI settings ──────────────────────────────────────────────────────────────
s = m:section(NamedSection, "global", "vpn-watchdog", translate("DPI Auto-Tune"))
s.addremove = false
s.anonymous = false

o = s:option(Flag, "dpi_auto_tune", translate("Enable DPI auto-tuning"),
    translate("Automatically generate and test obfuscation variants (MTU, AWG profiles, VLESS fingerprints) " ..
              "when DPI blocking is detected. Learned working variants are prioritised in future sessions."))
o.default = "1"
o.rmempty = false

o = s:option(Value, "dpi_max_variants", translate("Max DPI variants per config"),
    translate("Limit on auto-generated variants tested per base config per PROBING cycle (default: 8)"))
o.datatype = "uinteger"
o.default = "8"

o = s:option(ListValue, "dpi_profile", translate("DPI strategy profile"),
    translate("Controls how aggressively auto-tune generates and tests bypass variants."))
o:value("compat", translate("Compat (safe/minimal changes)"))
o:value("balanced", translate("Balanced (recommended)"))
o:value("aggressive", translate("Aggressive (maximum bypass attempts)"))
o.default = "balanced"

-- ── Probe targets ─────────────────────────────────────────────────────────────
s = m:section(TypedSection, "probe_target", translate("Probe Targets"),
    translate("Hosts to probe for connectivity checking. At least 2 recommended."))
s.addremove = true
s.anonymous = true
s.template = "cbi/tblsection"

o = s:option(Value, "host", translate("Host"))
o.rmempty = false
o.placeholder = "1.1.1.1 or api.telegram.org"

o = s:option(Value, "port", translate("Port"))
o.datatype = "port"
o.placeholder = "443"

o = s:option(ListValue, "type", translate("Probe type"))
o:value("icmp", "ICMP (ping)")
o:value("tcp", "TCP connect")
o:value("http", "HTTP GET")
o:value("https", "HTTPS GET")
o.default = "https"

return m
