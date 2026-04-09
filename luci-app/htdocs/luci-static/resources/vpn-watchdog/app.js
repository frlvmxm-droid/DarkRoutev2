'use strict';

/**
 * VWApp – shared JavaScript for all vpn-watchdog LuCI pages.
 *
 * Uses native fetch() (available in OpenWrt 21.02+ with modern LuCI).
 * Falls back to XMLHttpRequest for older firmware.
 */
window.VWApp = (function () {

  // ── Utilities ──────────────────────────────────────────────────────────────

  function esc(str) {
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function apiBase() {
    return window.location.pathname.replace(/\/[^\/]+$/, '') + '/api';
  }

  function get(path, cb) {
    fetch(apiBase() + path)
      .then(function (r) { return r.json(); })
      .then(cb)
      .catch(function (e) { console.error('VWApp GET ' + path, e); });
  }

  function post(path, body, cb) {
    fetch(apiBase() + path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    })
      .then(function (r) { return r.json(); })
      .then(cb || function () {})
      .catch(function (e) { console.error('VWApp POST ' + path, e); });
  }

  // ── Scoring formula (mirrors Go scoring.Entry.Score() including DPI bonus) ──
  function score(entry) {
    var rttMs = entry.ewma_rtt_ms || 1000;
    if (rttMs <= 0) rttMs = 1000;
    var loss = Math.min(entry.ewma_loss || 0, 1.0);
    var sw = entry.session_success_weight || 0.5;
    // dpiBonus: 1.0 (no history) to 1.5 (always bypasses DPI).
    var dpiBonus = 1.0 + (entry.dpi_bypass_success || 0) * 0.5;
    return (1 - loss) * 1000 / rttMs * sw * dpiBonus;
  }

  // ── Status ─────────────────────────────────────────────────────────────────
  function fetchStatus(cb) {
    get('/status', cb);
  }

  // ── DPI Status ─────────────────────────────────────────────────────────────
  function fetchDPIStatus(cb) {
    get('/dpi_status', cb);
  }

  // ── Configs ────────────────────────────────────────────────────────────────
  var _editingId = null;

  function loadConfigs() {
    get('/configs', function (configs) {
      var table = document.getElementById('vw-configs-table');
      if (!table) return;

      // Remove old data rows.
      table.querySelectorAll('tr.data-row').forEach(function (r) {
        r.parentNode.removeChild(r);
      });

      if (!configs || configs.length === 0) {
        var tr = document.createElement('tr');
        tr.className = 'tr data-row';
        tr.innerHTML = '<td class="td" colspan="6"><em>No configurations defined.</em></td>';
        table.appendChild(tr);
        return;
      }

      configs.forEach(function (cfg) {
        var endpoint = '';
        if (cfg.wg) endpoint = cfg.wg.endpoint || '';
        else if (cfg.awg) endpoint = cfg.awg.endpoint || '';
        else if (cfg.vless) endpoint = (cfg.vless.address || '') + ':' + (cfg.vless.port || '');

        var tr = document.createElement('tr');
        tr.className = 'tr data-row';
        tr.innerHTML =
          '<td class="td col-2">' + esc(cfg.id) + '</td>' +
          '<td class="td col-3">' + esc(cfg.name || '') + '</td>' +
          '<td class="td col-2">' + esc(cfg.protocol) + '</td>' +
          '<td class="td col-3">' + esc(endpoint) + '</td>' +
          '<td class="td col-1">' + (cfg.enabled ? '✓' : '✗') + '</td>' +
          '<td class="td col-2">' +
            '<button class="btn cbi-button cbi-button-edit" ' +
              'onclick=\'VWApp.openEditModal(' + JSON.stringify(cfg) + ')\'>Edit</button> ' +
            '<button class="btn cbi-button cbi-button-remove" ' +
              'onclick=\'VWApp.deleteConfig("' + esc(cfg.id) + '")\'>Delete</button>' +
          '</td>';
        table.appendChild(tr);
      });
    });
  }

  function openAddModal() {
    _editingId = null;
    document.getElementById('vw-modal-title').textContent = 'Add Configuration';
    clearForm();
    switchProtocol('wg');
    document.getElementById('vw-modal').style.display = 'block';
  }

  function openEditModal(cfg) {
    _editingId = cfg.id;
    document.getElementById('vw-modal-title').textContent = 'Edit — ' + cfg.id;
    clearForm();
    fillForm(cfg);
    switchProtocol(cfg.protocol);
    document.getElementById('vw-modal').style.display = 'block';
  }

  function closeModal() {
    document.getElementById('vw-modal').style.display = 'none';
  }

  function clearForm() {
    ['id','name','interface','table','mtu'].forEach(function (f) {
      document.getElementById('f-' + f).value = '';
    });
    document.getElementById('f-enabled').checked = true;
    document.getElementById('f-dpi-autotune').checked = true;
    document.getElementById('f-protocol').value = 'wg';
    // WG fields.
    ['privkey','pubkey','psk','endpoint','allowedips','keepalive'].forEach(function (f) {
      document.getElementById('f-wg-' + f).value = '';
    });
    // AWG extra.
    ['jc','jmin','jmax','s1','s2','h1','h2','h3','h4'].forEach(function (f) {
      document.getElementById('f-awg-' + f).value = '';
    });
    // VLESS.
    ['uuid','addr','port','flow','sni','fp','rpk','rsi','path','lport'].forEach(function (f) {
      document.getElementById('f-vl-' + f).value = '';
    });
    document.getElementById('f-vl-security').value = 'reality';
    document.getElementById('f-vl-transport').value = 'tcp';
    document.getElementById('vw-import-text').value = '';
  }

  function fillForm(cfg) {
    document.getElementById('f-id').value = cfg.id || '';
    document.getElementById('f-name').value = cfg.name || '';
    document.getElementById('f-protocol').value = cfg.protocol || 'wg';
    document.getElementById('f-interface').value = cfg.interface_name || '';
    document.getElementById('f-table').value = cfg.routing_table_id || '';
    document.getElementById('f-mtu').value = cfg.mtu || '';
    document.getElementById('f-enabled').checked = !!cfg.enabled;
    document.getElementById('f-dpi-autotune').checked =
      !cfg.dpi || cfg.dpi.auto_tune !== false;

    var wg = cfg.wg || cfg.awg;
    if (wg) {
      document.getElementById('f-wg-pubkey').value = wg.public_key || '';
      document.getElementById('f-wg-endpoint').value = wg.endpoint || '';
      document.getElementById('f-wg-allowedips').value = (wg.allowed_ips || []).join(', ');
      document.getElementById('f-wg-keepalive').value = wg.persistent_keepalive || '';
    }
    if (cfg.awg) {
      document.getElementById('f-awg-jc').value = cfg.awg.junk_packet_count || '';
      document.getElementById('f-awg-jmin').value = cfg.awg.junk_packet_min_size || '';
      document.getElementById('f-awg-jmax').value = cfg.awg.junk_packet_max_size || '';
      document.getElementById('f-awg-s1').value = cfg.awg.init_packet_junk_size || '';
      document.getElementById('f-awg-s2').value = cfg.awg.response_packet_junk_size || '';
      document.getElementById('f-awg-h1').value = cfg.awg.init_packet_magic_header || '';
      document.getElementById('f-awg-h2').value = cfg.awg.response_packet_magic_header || '';
      document.getElementById('f-awg-h3').value = cfg.awg.under_load_packet_magic_header || '';
      document.getElementById('f-awg-h4').value = cfg.awg.transport_packet_magic_header || '';
    }
    if (cfg.vless) {
      var v = cfg.vless;
      document.getElementById('f-vl-uuid').value = v.uuid || '';
      document.getElementById('f-vl-addr').value = v.address || '';
      document.getElementById('f-vl-port').value = v.port || '';
      document.getElementById('f-vl-security').value = v.security || 'reality';
      document.getElementById('f-vl-flow').value = v.flow || '';
      document.getElementById('f-vl-transport').value = v.transport || 'tcp';
      document.getElementById('f-vl-sni').value = v.sni || '';
      document.getElementById('f-vl-fp').value = v.fingerprint || '';
      document.getElementById('f-vl-rpk').value = v.reality_public_key || '';
      document.getElementById('f-vl-rsi').value = v.reality_short_id || '';
      document.getElementById('f-vl-path').value = v.transport_path || '';
      document.getElementById('f-vl-lport').value = v.local_port || '';
    }
  }

  function switchProtocol(proto) {
    document.getElementById('f-protocol').value = proto;
    document.getElementById('wg-fields').style.display  = (proto === 'wg' || proto === 'awg') ? '' : 'none';
    document.getElementById('awg-fields').style.display = (proto === 'awg') ? '' : 'none';
    document.getElementById('vless-fields').style.display = (proto === 'vless') ? '' : 'none';
  }

  function saveConfig() {
    var proto = document.getElementById('f-protocol').value;
    var obj = {
      id:             document.getElementById('f-id').value.trim(),
      name:           document.getElementById('f-name').value.trim(),
      protocol:       proto,
      interface_name: document.getElementById('f-interface').value.trim() || proto + '0',
      routing_table_id: parseInt(document.getElementById('f-table').value) || 0,
      mtu:            parseInt(document.getElementById('f-mtu').value) || 0,
      enabled:        document.getElementById('f-enabled').checked,
      dpi:            { auto_tune: document.getElementById('f-dpi-autotune').checked },
    };

    if (!obj.id) { alert('ID is required'); return; }

    if (proto === 'wg') {
      obj.wg = {
        private_key:          document.getElementById('f-wg-privkey').value.trim(),
        public_key:           document.getElementById('f-wg-pubkey').value.trim(),
        preshared_key:        document.getElementById('f-wg-psk').value.trim(),
        endpoint:             document.getElementById('f-wg-endpoint').value.trim(),
        allowed_ips:          document.getElementById('f-wg-allowedips').value.split(',').map(function(s){return s.trim();}),
        persistent_keepalive: parseInt(document.getElementById('f-wg-keepalive').value) || 0,
      };
    } else if (proto === 'awg') {
      obj.awg = {
        private_key:                    document.getElementById('f-wg-privkey').value.trim(),
        public_key:                     document.getElementById('f-wg-pubkey').value.trim(),
        preshared_key:                  document.getElementById('f-wg-psk').value.trim(),
        endpoint:                       document.getElementById('f-wg-endpoint').value.trim(),
        allowed_ips:                    document.getElementById('f-wg-allowedips').value.split(',').map(function(s){return s.trim();}),
        persistent_keepalive:           parseInt(document.getElementById('f-wg-keepalive').value) || 0,
        junk_packet_count:              parseInt(document.getElementById('f-awg-jc').value) || 4,
        junk_packet_min_size:           parseInt(document.getElementById('f-awg-jmin').value) || 40,
        junk_packet_max_size:           parseInt(document.getElementById('f-awg-jmax').value) || 70,
        init_packet_junk_size:          parseInt(document.getElementById('f-awg-s1').value) || 0,
        response_packet_junk_size:      parseInt(document.getElementById('f-awg-s2').value) || 0,
        init_packet_magic_header:       parseInt(document.getElementById('f-awg-h1').value) || 0,
        response_packet_magic_header:   parseInt(document.getElementById('f-awg-h2').value) || 0,
        under_load_packet_magic_header: parseInt(document.getElementById('f-awg-h3').value) || 0,
        transport_packet_magic_header:  parseInt(document.getElementById('f-awg-h4').value) || 0,
      };
    } else if (proto === 'vless') {
      obj.vless = {
        uuid:              document.getElementById('f-vl-uuid').value.trim(),
        address:           document.getElementById('f-vl-addr').value.trim(),
        port:              parseInt(document.getElementById('f-vl-port').value) || 443,
        security:          document.getElementById('f-vl-security').value,
        flow:              document.getElementById('f-vl-flow').value.trim(),
        transport:         document.getElementById('f-vl-transport').value,
        sni:               document.getElementById('f-vl-sni').value.trim(),
        fingerprint:       document.getElementById('f-vl-fp').value.trim(),
        reality_public_key: document.getElementById('f-vl-rpk').value.trim(),
        reality_short_id:  document.getElementById('f-vl-rsi').value.trim(),
        transport_path:    document.getElementById('f-vl-path').value.trim(),
        local_port:        parseInt(document.getElementById('f-vl-lport').value) || 0,
      };
    }

    post('/config_save', obj, function (res) {
      if (res.ok) {
        closeModal();
        loadConfigs();
      } else {
        alert('Error: ' + (res.error || 'unknown'));
      }
    });
  }

  function deleteConfig(id) {
    if (!confirm('Delete configuration "' + id + '"?')) return;
    post('/config_delete', { id: id }, function () { loadConfigs(); });
  }

  // ── Import parser ──────────────────────────────────────────────────────────
  function parseImport() {
    var text = document.getElementById('vw-import-text').value.trim();
    if (!text) return;

    // vless:// URI
    if (text.startsWith('vless://')) {
      try {
        fillVlessForm(parseVlessURIToConfig(text));
      } catch (e) {
        alert('Failed to parse VLESS URI: ' + e.message);
      }
      return;
    }

    // WireGuard / AmneziaWG config block
    if (text.includes('[Interface]')) {
      parseWGConfig(text);
      return;
    }

    alert('Unrecognised format. Supported: vless:// URI, WireGuard/AmneziaWG [Interface]+[Peer] block.');
  }

  function normalizeID(raw, fallback) {
    var id = String(raw || '').toLowerCase().replace(/[^a-z0-9_-]/g, '-').replace(/-+/g, '-');
    id = id.replace(/^-+|-+$/g, '');
    return id || fallback;
  }

  function parseVlessURIToConfig(uri, seq) {
    // vless://UUID@host:port?params#name
    var withoutScheme = uri.slice(8); // strip 'vless://'
    var hashIdx = withoutScheme.lastIndexOf('#');
    var name = '';
    if (hashIdx >= 0) {
      name = decodeURIComponent(withoutScheme.slice(hashIdx + 1));
      withoutScheme = withoutScheme.slice(0, hashIdx);
    }
    var atIdx = withoutScheme.indexOf('@');
    if (atIdx < 1) throw new Error('invalid vless URI (missing UUID@)');
    var uuid = withoutScheme.slice(0, atIdx);
    var rest = withoutScheme.slice(atIdx + 1);
    var qIdx = rest.indexOf('?');
    var hostPort = rest;
    var params = {};
    if (qIdx >= 0) {
      hostPort = rest.slice(0, qIdx);
      rest.slice(qIdx + 1).split('&').forEach(function (kv) {
        var eq = kv.indexOf('=');
        if (eq >= 0) params[kv.slice(0, eq)] = decodeURIComponent(kv.slice(eq + 1));
      });
    }
    var lastColon = hostPort.lastIndexOf(':');
    if (lastColon < 1) throw new Error('invalid vless URI (missing host:port)');
    var host = hostPort.slice(0, lastColon);
    var port = parseInt(hostPort.slice(lastColon + 1), 10);
    if (!port) port = 443;

    var baseName = name || host;
    var fallbackID = 'vless-' + (seq || 1);
    return {
      id: normalizeID(baseName, fallbackID),
      name: baseName || fallbackID,
      protocol: 'vless',
      enabled: true,
      interface_name: 'vpn0',
      routing_table_id: 0,
      mtu: 0,
      dpi: { auto_tune: true },
      vless: {
        uuid: uuid,
        address: host,
        port: port,
        security: params.security || 'reality',
        flow: params.flow || '',
        transport: params.type || 'tcp',
        sni: params.sni || params.serverName || '',
        fingerprint: params.fp || '',
        reality_public_key: params.pbk || '',
        reality_short_id: params.sid || '',
        transport_path: params.path || params.serviceName || '',
        local_port: 0,
      },
    };
  }

  function fillVlessForm(cfg) {
    if (!cfg || !cfg.vless) return;
    switchProtocol('vless');
    document.getElementById('f-protocol').value = 'vless';
    document.getElementById('f-name').value = cfg.name || '';
    document.getElementById('f-id').value = cfg.id || '';
    document.getElementById('f-vl-uuid').value = cfg.vless.uuid || '';
    document.getElementById('f-vl-addr').value = cfg.vless.address || '';
    document.getElementById('f-vl-port').value = cfg.vless.port || 443;
    document.getElementById('f-vl-security').value = cfg.vless.security || 'reality';
    document.getElementById('f-vl-flow').value = cfg.vless.flow || '';
    document.getElementById('f-vl-transport').value = cfg.vless.transport || 'tcp';
    document.getElementById('f-vl-sni').value = cfg.vless.sni || '';
    document.getElementById('f-vl-fp').value = cfg.vless.fingerprint || '';
    document.getElementById('f-vl-rpk').value = cfg.vless.reality_public_key || '';
    document.getElementById('f-vl-rsi').value = cfg.vless.reality_short_id || '';
    document.getElementById('f-vl-path').value = cfg.vless.transport_path || '';
    document.getElementById('f-vl-lport').value = cfg.vless.local_port || '';
  }

  function extractVlessURIs(text) {
    if (!text) return [];
    var matches = text.match(/vless:\/\/[^\s"'<>]+/g) || [];
    return matches;
  }

  function decodeMaybeBase64(text) {
    var compact = String(text || '').replace(/\s+/g, '');
    if (!compact || compact.indexOf('vless://') >= 0 || compact.length % 4 !== 0) return text;
    if (!/^[A-Za-z0-9+/=]+$/.test(compact)) return text;
    try {
      return atob(compact);
    } catch (_) {
      return text;
    }
  }

  function ensureUniqueIDs(configs) {
    var seen = {};
    return configs.map(function (cfg, idx) {
      var base = normalizeID(cfg.id, 'cfg-' + (idx + 1));
      var id = base;
      var n = 2;
      while (seen[id]) {
        id = base + '-' + n;
        n++;
      }
      seen[id] = true;
      cfg.id = id;
      if (!cfg.interface_name || cfg.interface_name === 'vpn0') {
        cfg.interface_name = 'vpn' + idx;
      }
      return cfg;
    });
  }

  function importVlessBatch() {
    var text = document.getElementById('vw-import-text').value.trim();
    if (!text) {
      alert('Paste one or more vless:// links (or subscription content) first.');
      return;
    }
    var decoded = decodeMaybeBase64(text);
    var uris = extractVlessURIs(decoded);
    if (uris.length === 0) {
      alert('No vless:// links found.');
      return;
    }
    var configs = [];
    var errors = [];
    uris.forEach(function (uri, idx) {
      try {
        configs.push(parseVlessURIToConfig(uri, idx + 1));
      } catch (e) {
        errors.push('[' + (idx + 1) + '] ' + e.message);
      }
    });
    if (configs.length === 0) {
      alert('All entries failed to parse:\n' + errors.join('\n'));
      return;
    }
    configs = ensureUniqueIDs(configs);
    post('/config_bulk_save', { configs: configs }, function (res) {
      if (!res || !res.ok) {
        alert('Bulk import failed: ' + ((res && res.error) || 'unknown'));
        return;
      }
      loadConfigs();
      var msg = 'Imported ' + (res.saved ? res.saved.length : 0) + '/' + configs.length + ' VLESS configs.';
      if (res.failed && res.failed.length) {
        msg += '\nFailed: ' + res.failed.map(function (f) { return f.id + ' (' + f.error + ')'; }).join(', ');
      }
      if (errors.length) {
        msg += '\nParse errors: ' + errors.join('; ');
      }
      alert(msg);
    });
  }

  function importSubscription() {
    var url = (document.getElementById('vw-subscription-url').value || '').trim();
    if (!url) {
      alert('Enter subscription URL first.');
      return;
    }
    post('/subscription_fetch', { url: url }, function (res) {
      if (!res || !res.ok || !res.text) {
        alert('Failed to load subscription: ' + ((res && res.error) || 'unknown'));
        return;
      }
      document.getElementById('vw-import-text').value = res.text;
      importVlessBatch();
    });
  }

  function loadImportFile(input) {
    var f = input && input.files && input.files[0];
    if (!f) return;
    var reader = new FileReader();
    reader.onload = function () {
      var text = String(reader.result || '');
      document.getElementById('vw-import-text').value = text;
      if (text.indexOf('[Interface]') >= 0) {
        parseWGConfig(text);
      } else if (text.indexOf('vless://') >= 0) {
        try {
          fillVlessForm(parseVlessURIToConfig(extractVlessURIs(text)[0], 1));
        } catch (e) {
          alert('Failed to parse file: ' + e.message);
        }
      }
    };
    reader.readAsText(f);
  }

  function parseWGConfig(text) {
    var lines = text.split('\n');
    var section = '';
    var iface = {}, peer = {};
    lines.forEach(function (line) {
      line = line.trim();
      if (line === '[Interface]') { section = 'iface'; return; }
      if (line === '[Peer]') { section = 'peer'; return; }
      if (line.startsWith('#') || !line) return;
      var eq = line.indexOf('=');
      if (eq < 0) return;
      var k = line.slice(0, eq).trim();
      var v = line.slice(eq + 1).trim();
      if (section === 'iface') iface[k] = v;
      else if (section === 'peer') peer[k] = v;
    });

    // Detect AmneziaWG by presence of Jc field.
    var proto = (iface['Jc'] !== undefined) ? 'awg' : 'wg';
    switchProtocol(proto);

    document.getElementById('f-wg-privkey').value = iface['PrivateKey'] || '';
    document.getElementById('f-wg-pubkey').value = peer['PublicKey'] || '';
    document.getElementById('f-wg-psk').value = peer['PresharedKey'] || '';
    document.getElementById('f-wg-endpoint').value = peer['Endpoint'] || '';
    document.getElementById('f-wg-allowedips').value = peer['AllowedIPs'] || '0.0.0.0/0, ::/0';
    document.getElementById('f-wg-keepalive').value = peer['PersistentKeepalive'] || '';
    document.getElementById('f-mtu').value = iface['MTU'] || '';

    if (proto === 'awg') {
      document.getElementById('f-awg-jc').value = iface['Jc'] || '';
      document.getElementById('f-awg-jmin').value = iface['Jmin'] || '';
      document.getElementById('f-awg-jmax').value = iface['Jmax'] || '';
      document.getElementById('f-awg-s1').value = iface['S1'] || '';
      document.getElementById('f-awg-s2').value = iface['S2'] || '';
      document.getElementById('f-awg-h1').value = iface['H1'] || '';
      document.getElementById('f-awg-h2').value = iface['H2'] || '';
      document.getElementById('f-awg-h3').value = iface['H3'] || '';
      document.getElementById('f-awg-h4').value = iface['H4'] || '';
    }
  }

  // ── Logs ───────────────────────────────────────────────────────────────────
  var _logTimer = null;

  function fetchLogs() {
    get('/logs', function (data) {
      var el = document.getElementById('vw-log-output');
      if (!el) return;
      el.textContent = data.lines || '';
      var autoScroll = document.getElementById('vw-autoscroll');
      if (autoScroll && autoScroll.checked) {
        el.scrollTop = el.scrollHeight;
      }
    });
  }

  function startLogRefresh() {
    _logTimer = setInterval(fetchLogs, 5000);
  }

  function toggleLogRefresh(enabled) {
    if (enabled) {
      startLogRefresh();
    } else {
      clearInterval(_logTimer);
    }
  }

  function clearLogView() {
    var el = document.getElementById('vw-log-output');
    if (el) el.textContent = '';
  }

  // ── Public API ─────────────────────────────────────────────────────────────
  return {
    esc: esc,
    score: score,
    fetchStatus: fetchStatus,
    fetchDPIStatus: fetchDPIStatus,
    loadConfigs: loadConfigs,
    openAddModal: openAddModal,
    openEditModal: openEditModal,
    closeModal: closeModal,
    switchProtocol: switchProtocol,
    saveConfig: saveConfig,
    deleteConfig: deleteConfig,
    parseImport: parseImport,
    importVlessBatch: importVlessBatch,
    importSubscription: importSubscription,
    loadImportFile: loadImportFile,
    fetchLogs: fetchLogs,
    startLogRefresh: startLogRefresh,
    toggleLogRefresh: toggleLogRefresh,
    clearLogView: clearLogView,
  };
})();
