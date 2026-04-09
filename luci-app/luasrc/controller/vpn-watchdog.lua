-- LuCI controller for vpn-watchdog
module("luci.controller.vpn-watchdog", package.seeall)

function index()
    if not nixio.fs.access("/etc/config/vpn-watchdog") then
        return
    end

    local page = entry({"admin", "vpn-watchdog"}, firstchild(), _("VPN Watchdog"), 60)
    page.dependent = false
    page.acl_depends = { "luci-app-vpn-watchdog" }

    entry({"admin", "vpn-watchdog", "dashboard"},
        template("vpn-watchdog/dashboard"), _("Dashboard"), 10)

    entry({"admin", "vpn-watchdog", "configs"},
        template("vpn-watchdog/configs"), _("Configurations"), 20)

    entry({"admin", "vpn-watchdog", "settings"},
        cbi("vpn-watchdog/settings"), _("Settings"), 30)

    entry({"admin", "vpn-watchdog", "logs"},
        template("vpn-watchdog/logs"), _("Logs"), 40)

    -- JSON API endpoints (called by JavaScript).
    entry({"admin", "vpn-watchdog", "api", "status"},
        call("api_status")).leaf = true

    entry({"admin", "vpn-watchdog", "api", "configs"},
        call("api_configs")).leaf = true

    entry({"admin", "vpn-watchdog", "api", "config_save"},
        call("api_config_save")).leaf = true

    entry({"admin", "vpn-watchdog", "api", "config_bulk_save"},
        call("api_config_bulk_save")).leaf = true

    entry({"admin", "vpn-watchdog", "api", "subscription_fetch"},
        call("api_subscription_fetch")).leaf = true

    entry({"admin", "vpn-watchdog", "api", "config_delete"},
        call("api_config_delete")).leaf = true

    entry({"admin", "vpn-watchdog", "api", "logs"},
        call("api_logs")).leaf = true

    entry({"admin", "vpn-watchdog", "api", "dpi_status"},
        call("api_dpi_status")).leaf = true
end

-- ── API helpers ───────────────────────────────────────────────────────────────

local function json_response(data)
    luci.http.prepare_content("application/json")
    luci.http.write(require("luci.jsonc").stringify(data, true))
end

local function fetch_daemon_status()
    -- The daemon exposes a local HTTP status API on port 8765.
    -- luci.sys.exec returns the command's stdout as a string.
    local body = luci.sys.exec(
        "curl -sf --max-time 2 http://127.0.0.1:8765/status 2>/dev/null")
    if body and body ~= "" then
        local ok, obj = pcall(require("luci.jsonc").parse, body)
        if ok then return obj end
    end
    -- Fallback: read persisted state file.
    local f = io.open("/tmp/vpn-watchdog/state.json", "r")
    if f then
        local content = f:read("*all")
        f:close()
        local ok, obj = pcall(require("luci.jsonc").parse, content)
        if ok then return obj end
    end
    return { state = "UNKNOWN", active_config_id = "", consecutive_failures = 0 }
end

function api_status()
    local status = fetch_daemon_status()
    -- Enrich with scoring data.
    local scores_file = "/tmp/vpn-watchdog/scores.json"
    local f = io.open(scores_file, "r")
    if f then
        local content = f:read("*all")
        f:close()
        local ok, scores = pcall(require("luci.jsonc").parse, content)
        if ok then status.scores = scores end
    end
    json_response(status)
end

function api_configs()
    local configs = {}
    local config_dir = luci.sys.exec(
        "uci -q get vpn-watchdog.global.config_dir 2>/dev/null"):gsub("%s+$", "")
    if config_dir == "" then config_dir = "/etc/vpn-watchdog/configs" end

    local find_out = luci.sys.exec(
        string.format("find %q -name '*.json' 2>/dev/null", config_dir))
    for path in find_out:gmatch("[^\n]+") do
        local f = io.open(path, "r")
        if f then
            local content = f:read("*all")
            f:close()
            local ok, obj = pcall(require("luci.jsonc").parse, content)
            if ok then
                -- Strip private keys from API response for security.
                if obj.wg then obj.wg.private_key = "***" end
                if obj.awg then obj.awg.private_key = "***" end
                configs[#configs + 1] = obj
            end
        end
    end
    json_response(configs)
end

local function get_config_dir()
    local config_dir = luci.sys.exec(
        "uci -q get vpn-watchdog.global.config_dir 2>/dev/null"):gsub("%s+$", "")
    if config_dir == "" then config_dir = "/etc/vpn-watchdog/configs" end
    return config_dir
end

local function sanitize_id(id)
    if type(id) ~= "string" then return "" end
    return id:gsub("[^%w%-_]", "")
end

local function preserve_private_keys(path, obj)
    local existing = nil
    local ef = io.open(path, "r")
    if ef then
        local content = ef:read("*all")
        ef:close()
        local eok, eobj = pcall(require("luci.jsonc").parse, content)
        if eok then existing = eobj end
    end
    if existing then
        if obj.wg and obj.wg.private_key == "***" and existing.wg then
            obj.wg.private_key = existing.wg.private_key
        end
        if obj.awg and obj.awg.private_key == "***" and existing.awg then
            obj.awg.private_key = existing.awg.private_key
        end
    end
end

local function write_config_file(config_dir, obj)
    local safe_id = sanitize_id(obj.id)
    if safe_id == "" then
        return nil, "invalid id"
    end
    luci.sys.call(string.format("mkdir -p %q", config_dir))
    local path = config_dir .. "/" .. safe_id .. ".json"
    preserve_private_keys(path, obj)

    local f = io.open(path, "w")
    if not f then
        return nil, "cannot write file"
    end
    f:write(require("luci.jsonc").stringify(obj, true))
    f:close()
    os.execute(string.format("chmod 600 %q", path))
    return safe_id, nil
end

function api_config_save()
    if luci.http.getenv("REQUEST_METHOD") ~= "POST" then
        luci.http.status(405, "Method Not Allowed")
        return
    end
    local body = luci.http.content()
    local ok, obj = pcall(require("luci.jsonc").parse, body)
    if not ok or not obj or not obj.id then
        luci.http.status(400, "Bad Request")
        json_response({ error = "invalid JSON or missing id" })
        return
    end

    local config_dir = get_config_dir()
    local safe_id, werr = write_config_file(config_dir, obj)
    if not safe_id then
        luci.http.status(400, "Bad Request")
        json_response({ error = werr or "cannot write file" })
        return
    end

    -- Signal the daemon to reload.
    luci.sys.call("kill -HUP $(cat /tmp/vpn-watchdog/vpn-watchdog.pid 2>/dev/null) 2>/dev/null")

    json_response({ ok = true, id = safe_id })
end

function api_config_bulk_save()
    if luci.http.getenv("REQUEST_METHOD") ~= "POST" then
        luci.http.status(405, "Method Not Allowed")
        return
    end
    local body = luci.http.content()
    local ok, obj = pcall(require("luci.jsonc").parse, body)
    if not ok or type(obj) ~= "table" or type(obj.configs) ~= "table" then
        luci.http.status(400, "Bad Request")
        json_response({ error = "invalid JSON or missing configs[]" })
        return
    end

    local config_dir = get_config_dir()
    local saved = {}
    local failed = {}
    for _, cfg in ipairs(obj.configs) do
        if type(cfg) == "table" and cfg.id then
            local sid, werr = write_config_file(config_dir, cfg)
            if sid then
                saved[#saved + 1] = sid
            else
                failed[#failed + 1] = { id = cfg.id, error = werr or "write failed" }
            end
        else
            failed[#failed + 1] = { id = "(missing)", error = "invalid config object" }
        end
    end

    luci.sys.call("kill -HUP $(cat /tmp/vpn-watchdog/vpn-watchdog.pid 2>/dev/null) 2>/dev/null")
    json_response({
        ok = (#saved > 0),
        saved = saved,
        failed = failed,
        total = #obj.configs
    })
end

function api_subscription_fetch()
    if luci.http.getenv("REQUEST_METHOD") ~= "POST" then
        luci.http.status(405, "Method Not Allowed")
        return
    end
    local body = luci.http.content()
    local ok, obj = pcall(require("luci.jsonc").parse, body)
    local url = ok and obj and obj.url or ""
    if type(url) ~= "string" or url == "" then
        luci.http.status(400, "Bad Request")
        json_response({ error = "missing url" })
        return
    end
    if not (url:match("^https?://")) then
        luci.http.status(400, "Bad Request")
        json_response({ error = "only http/https URLs are supported" })
        return
    end

    local cmd = string.format("curl -fsSL --max-time 12 %q 2>/dev/null", url)
    local out = luci.sys.exec(cmd)
    if not out or out == "" then
        luci.http.status(502, "Bad Gateway")
        json_response({ error = "failed to fetch subscription URL" })
        return
    end
    json_response({ ok = true, text = out })
end

function api_config_delete()
    if luci.http.getenv("REQUEST_METHOD") ~= "POST" then
        luci.http.status(405, "Method Not Allowed")
        return
    end
    local body = luci.http.content()
    local ok, obj = pcall(require("luci.jsonc").parse, body)
    if not ok or not obj or not obj.id then
        luci.http.status(400, "Bad Request")
        return
    end

    local config_dir = get_config_dir()

    local safe_id = obj.id:gsub("[^%w%-_]", "")
    local path = config_dir .. "/" .. safe_id .. ".json"
    os.remove(path)

    luci.sys.call("kill -HUP $(cat /tmp/vpn-watchdog/vpn-watchdog.pid 2>/dev/null) 2>/dev/null")
    json_response({ ok = true })
end

function api_logs()
    local log_path = "/tmp/log/vpn-watchdog"
    local lines = 200
    local content = luci.sys.exec(
        string.format("tail -n %d %q 2>/dev/null || logread -e vpn-watchdog 2>/dev/null | tail -n %d",
            lines, log_path, lines))
    json_response({ lines = content })
end

function api_dpi_status()
    -- Forward to the daemon's /dpi endpoint, then supplement with
    -- learned entries from the on-disk file for richer display.
    local body = luci.sys.exec(
        "curl -sf --max-time 2 http://127.0.0.1:8765/dpi 2>/dev/null")

    local result = {}
    if body and body ~= "" then
        local ok, obj = pcall(require("luci.jsonc").parse, body)
        if ok then result = obj end
    end

    -- Attach learned entries from persistent file.
    local learned = {}
    local f = io.open("/etc/vpn-watchdog/dpi_learned.json", "r")
    if not f then f = io.open("/tmp/vpn-watchdog/dpi_learned.json", "r") end
    if f then
        local content = f:read("*all")
        f:close()
        local ok, arr = pcall(require("luci.jsonc").parse, content)
        if ok and type(arr) == "table" then learned = arr end
    end
    result.learned_entries = learned

    -- Attach last detection result.
    local df = io.open("/tmp/vpn-watchdog/dpi_detection.json", "r")
    if df then
        local content = df:read("*all")
        df:close()
        local ok, det = pcall(require("luci.jsonc").parse, content)
        if ok then
            result.block_type = det.block_type
            result.evidence = det.evidence
            result.tested_at = det.tested_at
        end
    end

    json_response(result)
end
