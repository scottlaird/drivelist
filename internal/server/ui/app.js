'use strict';
// drivelist's web interface: a read-only view of the fleet server, one
// page per listing, every key a link. Everything on the page is built
// from DOM nodes with textContent; nothing that comes from the server is
// ever parsed as HTML, so a drive whose model is "<script>" is a drive
// whose model is "<script>". The server's Content-Security-Policy backs
// that up by refusing inline scripts and styles.
(() => {
  const main = document.getElementById('main');

  // ---------- demo ----------
  // A static copy of the page (drivelist demo export) carries a meta tag
  // naming its manifest. Then every call reads a file under data/ that
  // the export wrote, named exactly as demoKey() says; there is no token;
  // and the clock stops at the export, so "3h ago" stays true.
  const demoMeta = document.querySelector('meta[name="drivelist-demo"]');
  let demo = null;
  function demoKey(body) {
    const parts = [];
    for (const k of Object.keys(body || {}).sort()) {
      const v = body[k];
      if (k === 'since' || v === undefined || v === null || v === false || v === '' || (Array.isArray(v) && !v.length)) continue;
      parts.push(k + '=' + encodeURIComponent(Array.isArray(v) ? v.join('+') : String(v)));
    }
    return parts.length ? parts.join(',') : 'index';
  }
  async function demoRPC(name, body) {
    const key = demoKey(body);
    const res = await fetch('data/' + name + '/' + encodeURIComponent(key) + '.json');
    if (!res.ok) throw new Error('the demo has no ' + name + ' for ' + key);
    return res.json();
  }
  const now = () => demo ? demo.asOf : Date.now();

  // ---------- token ----------
  const tokenKey = 'drivelist.token';
  function getToken() { if (demo) return 'demo'; try { return localStorage.getItem(tokenKey) || ''; } catch (e) { return ''; } }
  function setToken(t) { try { localStorage.setItem(tokenKey, t); } catch (e) { /* private mode */ } }
  // askToken shows a form in the page rather than a browser dialog, which
  // some browsers suppress and no test harness can drive.
  function askToken(message) {
    clear(main);
    const input = el('input', { type: 'password', size: '48', autocomplete: 'off', placeholder: 'viewer or operator token' });
    const form = el('form', { class: 'tokenform' }, el('p', { class: 'note', text: message || 'A token is needed to read the server.' }), el('label', null, 'token ', input), ' ', el('button', { type: 'submit', text: 'use' }));
    const use = () => { setToken(input.value.trim()); route(); };
    form.addEventListener('submit', ev => { ev.preventDefault(); use(); });
    input.addEventListener('keydown', ev => { if (ev.key === 'Enter') { ev.preventDefault(); use(); } });
    main.append(form);
    input.focus();
  }
  document.getElementById('token').addEventListener('click', () => askToken('Enter a token to use from now on.'));

  // ---------- RPC ----------
  class AuthError extends Error {}
  async function rpc(name, body) {
    if (demo) return demoRPC(name, body);
    const res = await fetch('/drivelist.v1.Query/' + name, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + getToken() },
      body: JSON.stringify(body || {}),
    });
    if (res.status === 401) throw new AuthError('unauthorized');
    const text = await res.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch (e) { data = {}; }
    if (!res.ok) throw new Error(data.message || (res.status + ' ' + res.statusText));
    return data;
  }

  // ---------- DOM ----------
  function el(tag, attrs, ...children) {
    const n = document.createElement(tag);
    if (attrs) for (const [k, v] of Object.entries(attrs)) {
      if (k === 'class') n.className = v;
      else if (k === 'text') n.textContent = v;
      else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
      else n.setAttribute(k, v);
    }
    for (const c of children) {
      if (c === null || c === undefined) continue;
      n.append(c instanceof Node ? c : document.createTextNode(String(c)));
    }
    return n;
  }
  function link(text, hash) { return el('a', { href: hash }, text); }
  function clear(n) { while (n.firstChild) n.removeChild(n.firstChild); }
  const enc = encodeURIComponent;

  // ---------- formatting ----------
  const num = v => (v === undefined || v === null || v === '') ? null : Number(v);
  function bytes(v) {
    v = num(v); if (v === null) return '-';
    const units = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];
    let i = 0; let x = v;
    while (x >= 1000 && i < units.length - 1) { x /= 1000; i++; }
    return (i >= 3 && x < 10 ? x.toFixed(1) : Math.round(x)) + ' ' + units[i];
  }
  const ms = v => (v === undefined || v === null) ? '-' : Number(v).toFixed(1) + 'ms';
  const pct = v => (v === undefined || v === null) ? '-' : Math.round(Number(v) * 100) + '%';
  const dash = v => (v === undefined || v === null || v === '') ? '-' : String(v);
  function when(ts) {
    if (!ts) return '-';
    const d = new Date(ts);
    const p = n => String(n).padStart(2, '0');
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
  }
  function ago(ts) {
    if (!ts) return 'never';
    const s = (now() - new Date(ts).getTime()) / 1000;
    if (s < 90) return 'just now';
    if (s < 3600) return Math.round(s / 60) + 'm ago';
    if (s < 172800) return Math.round(s / 3600) + 'h ago';
    return Math.round(s / 86400) + 'd ago';
  }
  const gap = secs => { const s = Number(secs) || 0; if (s < 3600) return Math.round(s / 60) + 'm'; if (s < 172800) return Math.floor(s / 3600) + 'h' + String(Math.round(s / 60) % 60).padStart(2, '0') + 'm'; return Math.floor(s / 86400) + 'd'; };
  const busName = b => ({ BUS_SAS: 'SAS', BUS_SATA: 'SATA', BUS_NVME: 'NVMe', BUS_USB: 'USB', BUS_VIRTIO: 'virtio' })[b] || '-';
  function slotText(p) {
    if (!p) return '-';
    const where = p.enclosureName || p.enclosureVia || p.enclosure || '';
    const bay = p.bayLabel || p.bay || '';
    if (!where && !bay) return '-';
    if (!bay) return where;
    return where + ' bay ' + bay;
  }
  function slotLink(p) {
    if (!p || !p.enclosure) return document.createTextNode(slotText(p));
    return link(slotText(p), '#/enclosure/' + enc(p.enclosure));
  }
  function useSummary(uses) {
    if (!uses || !uses.length) return '-';
    return uses.map(u => {
      if (u.startsWith('zfs > ')) {
        const parts = u.split(' > ');
        return 'zfs ' + parts[1].split(' ')[0] + (parts[2] ? '/' + parts[2].split(' ')[0] : '');
      }
      return u;
    }).join(', ');
  }

  // ---------- table ----------
  // spec: { key, columns: [{name, header, value(row) -> string|Node, sort(row), num, extra, cls(row)}], rows, sort }
  // Column choice and sort order persist per key in localStorage.
  function prefs(key) {
    try { return JSON.parse(localStorage.getItem('drivelist.table.' + key) || '{}'); } catch (e) { return {}; }
  }
  function savePrefs(key, p) { try { localStorage.setItem('drivelist.table.' + key, JSON.stringify(p)); } catch (e) { /* ignore */ } }
  function compare(a, b) {
    const ma = a === null || a === undefined || a === '' || a === '-';
    const mb = b === null || b === undefined || b === '' || b === '-';
    if (ma && mb) return 0;
    if (ma) return 1;
    if (mb) return -1;
    if (typeof a === 'number' && typeof b === 'number') return a - b;
    if (typeof a === 'boolean' && typeof b === 'boolean') return (a === b) ? 0 : (a ? -1 : 1);
    return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' });
  }
  function table(spec) {
    const p = prefs(spec.key);
    const chosen = new Set(p.columns || spec.columns.filter(c => !c.extra).map(c => c.name));
    let sortBy = p.sort !== undefined ? p.sort : (spec.sort || '');
    const wrap = el('div');
    const toolbar = el('div', { class: 'toolbar' });
    const picker = el('details', { class: 'columns' }, el('summary', { text: 'columns' }));
    const picks = el('div', { class: 'picks' });
    for (const c of spec.columns) {
      const box = el('input', { type: 'checkbox' });
      box.checked = chosen.has(c.name);
      box.addEventListener('change', () => {
        if (box.checked) chosen.add(c.name); else chosen.delete(c.name);
        savePrefs(spec.key, { columns: spec.columns.filter(x => chosen.has(x.name)).map(x => x.name), sort: sortBy });
        render();
      });
      picks.append(el('label', null, box, ' ', c.header));
    }
    picker.append(picks);
    toolbar.append(picker, el('span', { class: 'count', text: spec.rows.length + ' rows' }));
    const holder = el('div', { class: 'tablewrap' });
    wrap.append(toolbar, holder);
    function render() {
      clear(holder);
      const cols = spec.columns.filter(c => chosen.has(c.name));
      const rows = spec.rows.slice();
      if (sortBy) {
        const desc = sortBy.startsWith('-');
        const name = desc ? sortBy.slice(1) : sortBy;
        const c = spec.columns.find(x => x.name === name);
        if (c) {
          const keyOf = r => c.sort ? c.sort(r) : (typeof c.value(r) === 'string' ? c.value(r) : null);
          rows.sort((a, b) => { const k = compare(keyOf(a), keyOf(b)); return desc && k !== 0 && !isMissing(keyOf(a)) && !isMissing(keyOf(b)) ? -k : k; });
        }
      }
      const thead = el('tr');
      for (const c of cols) {
        const active = sortBy === c.name || sortBy === '-' + c.name;
        const arrow = el('span', { class: 'arrow', text: active ? (sortBy.startsWith('-') ? '▼' : '▲') : '' });
        // Numeric columns are right-aligned, heading included, with the
        // sort arrow on the outside so the label lines up with the digits.
        const th = el('th', { class: [active ? 'sorted' : '', c.num ? 'num' : ''].join(' ').trim() },
          ...(c.num ? [arrow, ' ', c.header] : [c.header, ' ', arrow]));
        th.addEventListener('click', () => {
          sortBy = sortBy === c.name ? '-' + c.name : c.name;
          savePrefs(spec.key, { columns: spec.columns.filter(x => chosen.has(x.name)).map(x => x.name), sort: sortBy });
          render();
        });
        thead.append(th);
      }
      const tbody = el('tbody');
      for (const r of rows) {
        const tr = el('tr');
        for (const c of cols) {
          const v = c.value(r);
          const td = el('td', { class: [c.num ? 'num' : '', c.mono ? 'mono' : '', c.cls ? (c.cls(r) || '') : ''].join(' ').trim() });
          td.append(v instanceof Node ? v : document.createTextNode(v === undefined || v === null ? '' : String(v)));
          tr.append(td);
        }
        tbody.append(tr);
      }
      holder.append(el('table', null, el('thead', null, thead), tbody));
    }
    render();
    return wrap;
  }
  const isMissing = v => v === null || v === undefined || v === '' || v === '-';

  // ---------- events ----------
  function detailOf(e) { try { return JSON.parse(e.detail || '{}'); } catch (x) { return {}; } }
  function slotD(d, prefix) {
    const where = d[prefix + 'enclosure_name'] || d[prefix + 'enclosure_via'] || d[prefix + 'enclosure'] || d[prefix + 'expander_dev'] || d[prefix + 'expander'] || '';
    const bay = d[prefix + 'bay_label'] || d[prefix + 'bay'] || '';
    return !where && !bay ? '-' : (!bay ? where : where + ' bay ' + bay);
  }
  function describe(e) {
    const d = detailOf(e);
    const actor = (e.source || '').replace(/^user:/, '');
    switch (e.kind) {
      case 'first_seen': return 'first seen  ' + slotD(d, '') + '  ' + useSummary(d.uses);
      case 'appeared': return 'appeared  ' + slotD(d, '') + '  ' + useSummary(d.uses);
      case 'vanished': return 'vanished  ' + slotD(d, '') + (d.still_in_pool ? '; pool ' + d.still_in_pool + ' still references it' : '');
      case 'reappeared': return (d.same_slot ? 'reappeared  ' : 'moved  ') + slotD(d, '');
      case 'moved_host': return 'moved  ' + slotD(d, 'to_') + '  (from ' + d.from_host + ' ' + slotD(d, 'from_') + ')';
      case 'moved_bay': return 'moved bay  ' + slotD(d, 'to_') + '  (from ' + slotD(d, 'from_') + ')';
      case 'use_changed': return 'use changed  ' + useSummary(d.to_uses) + '  (was ' + useSummary(d.from_uses) + ')';
      case 'enclosure_renamed': case 'expander_renamed': return 'enclosure  ' + (d.from_name || d.from_via || d.from_dev || d.from) + ' is now ' + (d.to_name || d.to_via || d.to_dev || d.to) + ' (' + d.drives + ' drives kept their bays)';
      case 'member_state_changed': return 'zfs state  ' + d.from + ' -> ' + d.to;
      case 'status_changed': return 'status  ' + d.status + '  ' + actor + ': ' + JSON.stringify(d.note || '');
      case 'note': return 'note  ' + actor + ': ' + JSON.stringify(d.note || '');
      case 'merged': return 'merged  record ' + d.from_serial + ' folded in by ' + actor;
      case 'host_merged': return 'host merged  absorbed ' + d.from_hostname;
      case 'smart_warning': return 'smart  ' + JSON.stringify(d.reasons || []) + '  ' + (d.dev_name || '');
      case 'kernel_warning': return 'kernel  ' + d.class + ' ' + (d.code || '') + ' ×' + d.count + '  ' + (d.dev_name || '');
      case 'sas_link_changed': return 'sas link  ' + d.owner_name + ' phy ' + d.phy + '  ' + (d.from || '-') + ' -> ' + (d.to || '-');
      case 'sas_attached_changed': return 'sas recabled  ' + d.owner_name + ' phy ' + d.phy + '  now ' + d.to_attached + ' (was ' + d.from_attached + ')';
      case 'sas_port_changed': return 'sas port  ' + d.owner_name + ' ' + d.port + '  ' + d.from + ' -> ' + d.to + ' phys';
      case 'sas_errors': { const g = d.grew || {}; const parts = []; for (const [k, v] of Object.entries(g)) if (v > 0) parts.push('+' + v + ' ' + k.replace(/_/g, ' ')); return 'sas errors  ' + d.owner_name + ' phy ' + d.phy + '  ' + parts.join(', '); }
      case 'sas_node_changed': return 'sas node  ' + d.name + ' (' + (d.product || '').trim() + ') ' + d.change + (d.change === 'revision' ? ' ' + d.from + ' -> ' + d.to : '');
      case 'host_first_seen': return 'host seen';
      case 'host_stale': return 'host stale';
      case 'host_resumed': return 'host resumed  silent ' + Math.round((d.silent_secs || 0) / 60) + 'm';
      case 'host_rebooted': return 'host rebooted' + (d.up_secs ? '  up ' + gap(d.up_secs) + ' before' : '') + (d.silent_secs ? '  silent ' + gap(d.silent_secs) : '');
      case 'hardware_error': return 'hardware  ' + d.class + ' ' + (d.code || '') + ' ×' + d.count + '  ' + (d.sample || '');
      case 'report_degraded': return 'report degraded  unidentified ' + JSON.stringify(d.unidentified || []);
      case 'pool_missing_member': return 'pool member missing  ' + (d.pool || '') + ' ' + (d.path || '');
      case 'identity_conflict': return 'identity conflict  ' + JSON.stringify(d.keys || []);
      default: return e.kind + '  ' + (e.detail || '');
    }
  }
  const eventCols = [
    { name: 'time', header: 'TIME', value: e => when(e.ts), sort: e => e.ts ? new Date(e.ts).getTime() : null },
    { name: 'host', header: 'HOST', value: e => e.hostname ? link(e.hostname, '#/host/' + enc(e.hostname)) : '-', sort: e => e.hostname || null },
    { name: 'drive', header: 'DRIVE', value: e => e.serial ? link(e.serial, '#/drive/' + enc(e.serial)) : '-', sort: e => e.serial || null, mono: true },
    { name: 'kind', header: 'KIND', value: e => e.kind },
    { name: 'event', header: 'EVENT', value: e => describe(e) },
    { name: 'source', header: 'SOURCE', value: e => e.source || '', extra: true },
  ];

  // ---------- columns ----------
  const hostCols = [
    { name: 'host', header: 'HOST', value: h => link(h.hostname, '#/host/' + enc(h.hostname)), sort: h => h.hostname },
    { name: 'drives', header: 'DRIVES', value: h => h.driveCount || 0, sort: h => h.driveCount || 0, num: true },
    { name: 'missing', header: 'MISSING', value: h => h.missingCount || 0, sort: h => h.missingCount || 0, num: true, cls: h => (h.missingCount || 0) > 0 ? 'warn' : '' },
    { name: 'ghosts', header: 'GHOSTS', value: h => h.ghostCount || 0, sort: h => h.ghostCount || 0, num: true },
    { name: 'last', header: 'LAST REPORT', value: h => ago(h.lastReport), sort: h => h.lastReport ? new Date(h.lastReport).getTime() : null },
    { name: 'state', header: 'STATE', value: h => h.staleSince ? 'stale since ' + when(h.staleSince) : 'ok', cls: h => h.staleSince ? 'bad' : 'ok' },
    { name: 'agent', header: 'AGENT', value: h => dash(h.agentVersion) },
    { name: 'up', header: 'UP', value: h => h.bootedAt ? gap((now() - new Date(h.bootedAt).getTime()) / 1000) : '-', sort: h => h.bootedAt ? new Date(h.bootedAt).getTime() : null },
    { name: 'machineid', header: 'MACHINE ID', value: h => h.machineId || '', mono: true, extra: true },
    { name: 'os', header: 'OS', value: h => dash(h.os), extra: true },
    { name: 'first', header: 'FIRST SEEN', value: h => when(h.firstSeen), sort: h => h.firstSeen ? new Date(h.firstSeen).getTime() : null, extra: true },
  ];
  function placementOf(d) { return d.current || d.last || null; }
  const driveCols = [
    { name: 'host', header: 'HOST', value: d => { const p = placementOf(d); if (!p) return '-'; const a = link(p.hostname, '#/host/' + enc(p.hostname)); return d.current ? a : el('span', null, '(', a, ')'); }, sort: d => { const p = placementOf(d); return p ? p.hostname : null; } },
    { name: 'slot', header: 'SLOT', value: d => slotLink(placementOf(d)), sort: d => slotText(placementOf(d)) },
    { name: 'device', header: 'DEVICE', value: d => d.current ? dash(d.current.devName) : '-', mono: true },
    { name: 'model', header: 'MODEL', value: d => d.model },
    { name: 'serial', header: 'SERIAL', value: d => link(d.serial, '#/drive/' + enc(d.serial)), sort: d => d.serial, mono: true },
    { name: 'size', header: 'SIZE', value: d => bytes(d.sizeBytes), sort: d => num(d.sizeBytes), num: true },
    { name: 'bus', header: 'BUS', value: d => busName(d.bus) },
    { name: 'status', header: 'STATUS', value: d => d.status, cls: d => d.status === 'ok' ? '' : (d.status === 'bad' ? 'bad' : 'warn') },
    { name: 'zfs', header: 'ZFS', value: d => dash(d.memberState), cls: d => d.memberState && d.memberState !== 'ONLINE' ? 'warn' : '' },
    { name: 'uses', header: 'USES', value: d => { const p = placementOf(d); return p ? useSummary(p.uses) : '-'; } },
    { name: 'wwn', header: 'WWN', value: d => dash(d.wwn), mono: true, extra: true },
    { name: 'vendor', header: 'VENDOR', value: d => dash(d.vendor), extra: true },
    { name: 'bay', header: 'BAY ID', value: d => { const p = placementOf(d); return p ? dash(p.bay) : '-'; }, extra: true },
    { name: 'since', header: 'SINCE', value: d => { const p = placementOf(d); return p ? when(p.firstSeen) : '-'; }, sort: d => { const p = placementOf(d); return p && p.firstSeen ? new Date(p.firstSeen).getTime() : null; }, extra: true },
    { name: 'firstseen', header: 'FIRST SEEN', value: d => when(d.firstSeen), sort: d => d.firstSeen ? new Date(d.firstSeen).getTime() : null, extra: true },
    { name: 'lastseen', header: 'LAST SEEN', value: d => when(d.lastSeen), sort: d => d.lastSeen ? new Date(d.lastSeen).getTime() : null, extra: true },
  ];
  const enclosureCols = [
    { name: 'host', header: 'HOST', value: e => link(e.hostname, '#/host/' + enc(e.hostname)), sort: e => e.hostname },
    { name: 'via', header: 'VIA', value: e => dash(e.via), mono: true },
    { name: 'model', header: 'MODEL', value: e => dash(e.product) },
    { name: 'name', header: 'NAME', value: e => e.name ? link(e.name, '#/enclosure/' + enc(e.enclosure)) : link('(unnamed)', '#/enclosure/' + enc(e.enclosure)), sort: e => e.name || null },
    { name: 'drives', header: 'DRIVES', value: e => e.drives || 0, sort: e => e.drives || 0, num: true },
    { name: 'bays', header: 'BAYS', value: e => e.bays || '-', sort: e => e.bays || null, num: true },
    { name: 'key', header: 'KEY', value: e => link(e.enclosure, '#/enclosure/' + enc(e.enclosure)), sort: e => e.enclosure, mono: true },
    { name: 'note', header: 'NOTE', value: e => e.note || '' },
    { name: 'profile', header: 'PROFILE', value: e => dash(e.profile), extra: true },
    { name: 'board', header: 'BOARD', value: e => dash(e.board), extra: true },
  ];
  const smartCols = [
    { name: 'host', header: 'HOST', value: r => link(r.drive.current.hostname, '#/host/' + enc(r.drive.current.hostname)), sort: r => r.drive.current.hostname },
    { name: 'slot', header: 'SLOT', value: r => slotLink(r.drive.current), sort: r => slotText(r.drive.current) },
    { name: 'device', header: 'DEVICE', value: r => dash(r.drive.current.devName), mono: true },
    { name: 'serial', header: 'SERIAL', value: r => link(r.drive.serial, '#/drive/' + enc(r.drive.serial)), sort: r => r.drive.serial, mono: true },
    { name: 'model', header: 'MODEL', value: r => r.drive.model },
    { name: 'health', header: 'HEALTH', value: r => !r.sample ? (r.lastSkipped ? 'skipped: ' + r.lastSkipped : 'no reading') : (r.sample.summary && r.sample.summary.healthy === false ? 'FAILED' : (r.sample.summary && r.sample.summary.healthy === true ? 'ok' : '-')), sort: r => r.problem ? 0 : 1, cls: r => r.problem ? 'bad' : '' },
    { name: 'hours', header: 'HOURS', value: r => sm(r, 'powerOnHours'), sort: r => num(smRaw(r, 'powerOnHours')), num: true },
    { name: 'temp', header: 'TEMP', value: r => { const v = smRaw(r, 'tempC'); return v === null ? '' : v + '°C'; }, sort: r => num(smRaw(r, 'tempC')), num: true },
    { name: 'realloc', header: 'REALLOC', value: r => sm(r, 'reallocated'), sort: r => num(smRaw(r, 'reallocated')), num: true, cls: r => num(smRaw(r, 'reallocated')) > 0 ? 'warn' : '' },
    { name: 'pending', header: 'PENDING', value: r => sm(r, 'pending'), sort: r => num(smRaw(r, 'pending')), num: true, cls: r => num(smRaw(r, 'pending')) > 0 ? 'warn' : '' },
    { name: 'uncorr', header: 'UNCORR', value: r => sm(r, 'uncorrectable'), sort: r => num(smRaw(r, 'uncorrectable')), num: true, cls: r => num(smRaw(r, 'uncorrectable')) > 0 ? 'warn' : '' },
    { name: 'crc', header: 'CRC', value: r => sm(r, 'crcErrors'), sort: r => num(smRaw(r, 'crcErrors')), num: true, cls: r => num(smRaw(r, 'crcErrors')) > 0 ? 'warn' : '' },
    { name: 'wear', header: 'WEAR', value: r => { const v = smRaw(r, 'percentUsed'); return v === null ? '' : v + '%'; }, sort: r => num(smRaw(r, 'percentUsed')), num: true, cls: r => num(smRaw(r, 'percentUsed')) >= 80 ? 'warn' : '' },
    { name: 'selftest', header: 'SELF-TEST', value: r => r.sample && r.sample.summary ? dash(r.sample.summary.selftestLast) : '' },
    { name: 'sampled', header: 'SAMPLED', value: r => r.sample ? ago(r.sample.ts) + (r.lastSkipped ? ' (now ' + r.lastSkipped + ')' : '') : '', sort: r => r.sample && r.sample.ts ? new Date(r.sample.ts).getTime() : null },
    { name: 'read', header: 'READ', value: r => r.sample && r.sample.summary ? bytes(r.sample.summary.readBytes) : '', sort: r => num(smRaw(r, 'readBytes')), num: true, extra: true },
    { name: 'written', header: 'WRITTEN', value: r => r.sample && r.sample.summary ? bytes(r.sample.summary.writeBytes) : '', sort: r => num(smRaw(r, 'writeBytes')), num: true, extra: true },
    { name: 'protocol', header: 'PROTOCOL', value: r => r.sample && r.sample.summary ? dash(r.sample.summary.protocol) : '', extra: true },
  ];
  function smRaw(r, k) { const m = r.sample && r.sample.summary; if (!m || m[k] === undefined || m[k] === null) return null; return m[k]; }
  function sm(r, k) { const v = smRaw(r, k); return r.sample ? (v === null ? '-' : String(v)) : ''; }
  const ratioText = (v, base) => (!base ? '-' : (v / base).toFixed(1) + '×');
  const ioCols = [
    { name: 'vdev', header: 'VDEV', value: r => (r.group || 'no pool') + ' (' + r.groupSize + ')', sort: r => r.group || '' },
    { name: 'host', header: 'HOST', value: r => link(r.hostname, '#/host/' + enc(r.hostname)), sort: r => r.hostname },
    { name: 'device', header: 'DEVICE', value: r => r.devName, mono: true },
    { name: 'serial', header: 'SERIAL', value: r => link(r.serial, '#/drive/' + enc(r.serial)), sort: r => r.serial, mono: true },
    { name: 'model', header: 'MODEL', value: r => r.model },
    { name: 'r_await', header: 'R_AWAIT', value: r => ms(r.rAwaitMs), sort: r => num(r.rAwaitMs), num: true },
    { name: 'r_ratio', header: 'vs MED', value: r => ratioText(r.rAwaitMs || 0, r.groupRAwaitMs || 0), sort: r => r.groupRAwaitMs ? (r.rAwaitMs || 0) / r.groupRAwaitMs : null, num: true, cls: r => r.groupRAwaitMs && (r.rAwaitMs || 0) / r.groupRAwaitMs >= 3 ? 'warn' : '' },
    { name: 'w_await', header: 'W_AWAIT', value: r => ms(r.wAwaitMs), sort: r => num(r.wAwaitMs), num: true },
    { name: 'w_ratio', header: 'vs MED', value: r => ratioText(r.wAwaitMs || 0, r.groupWAwaitMs || 0), sort: r => r.groupWAwaitMs ? (r.wAwaitMs || 0) / r.groupWAwaitMs : null, num: true },
    { name: 'util', header: 'UTIL', value: r => pct(r.util), sort: r => num(r.util), num: true },
    { name: 'reads', header: 'READS', value: r => dash(r.reads), sort: r => num(r.reads), num: true },
    { name: 'writes', header: 'WRITES', value: r => dash(r.writes), sort: r => num(r.writes), num: true },
    { name: 'dropped', header: 'DROPPED', value: r => r.glitches || '-', sort: r => r.glitches || null, num: true, extra: true },
  ];
  const sasErrorCols = [
    { name: 'host', header: 'HOST', value: r => link(r.hostname, '#/host/' + enc(r.hostname)), sort: r => r.hostname },
    { name: 'node', header: 'NODE', value: r => dash(r.ownerName), mono: true },
    { name: 'phy', header: 'PHY', value: r => r.phyId || 0, sort: r => r.phyId || 0, num: true },
    { name: 'port', header: 'PORT', value: r => dash(r.port), mono: true },
    { name: 'attached', header: 'ATTACHED', value: r => r.devName ? el('span', null, r.devName + ' ', link(r.serial, '#/drive/' + enc(r.serial)), r.bay ? ' bay ' + r.bay : '') : (r.attachedKind === 'upstream' ? 'upstream' : dash(r.attached)) },
    { name: 'invalid', header: 'INVALID', value: r => r.invalidDword || 0, sort: r => num(r.invalidDword) || 0, num: true },
    { name: 'disparity', header: 'DISPARITY', value: r => r.disparityError || 0, sort: r => num(r.disparityError) || 0, num: true },
    { name: 'dwsync', header: 'DWSYNC', value: r => r.lossDwordSync || 0, sort: r => num(r.lossDwordSync) || 0, num: true },
    { name: 'reset', header: 'RESET', value: r => r.phyResetProblem || 0, sort: r => num(r.phyResetProblem) || 0, num: true },
    { name: 'reports', header: 'REPORTS', value: r => r.samples || 0, sort: r => r.samples || 0, num: true },
    { name: 'last', header: 'LAST', value: r => when(r.lastAt), sort: r => r.lastAt ? new Date(r.lastAt).getTime() : null },
  ];
  const phyCols = [
    { name: 'node', header: 'NODE', value: p => p.ownerName || p.phy.ownerAddress, mono: true },
    { name: 'phy', header: 'PHY', value: p => p.phy.phyId || 0, sort: p => p.phy.phyId || 0, num: true },
    { name: 'port', header: 'PORT', value: p => dash(p.phy.port), mono: true },
    { name: 'rate', header: 'RATE', value: p => p.phy.rateGbit ? p.phy.rate : (p.phy.rate === 'Unknown' ? '-' : dash(p.phy.rate)), sort: p => num(p.phy.rateGbit) || 0, num: true },
    { name: 'attached', header: 'ATTACHED', value: p => { const q = p.phy; if (q.attachedKind === 'drive') return el('span', null, q.devName + ' ', link(p.serial || q.devName, '#/drive/' + enc(p.serial || '')), q.bay ? ' bay ' + q.bay : ''); if (q.attachedKind === 'expander') return q.attached + (q.portWidth > 1 ? ' ×' + q.portWidth : ''); if (q.attachedKind === 'upstream') return 'upstream'; if (q.attachedKind === 'device') return (q.bay ? 'bay ' + q.bay + ' ' : '') + 'no disk'; return '-'; } },
    { name: 'invalid', header: 'INVALID', value: p => p.phy.invalidDword || 0, sort: p => num(p.phy.invalidDword) || 0, num: true, cls: p => num(p.phy.invalidDword) > 0 ? 'warn' : '' },
    { name: 'disparity', header: 'DISPARITY', value: p => p.phy.disparityError || 0, sort: p => num(p.phy.disparityError) || 0, num: true, cls: p => num(p.phy.disparityError) > 0 ? 'warn' : '' },
    { name: 'dwsync', header: 'DWSYNC', value: p => p.phy.lossDwordSync || 0, sort: p => num(p.phy.lossDwordSync) || 0, num: true, cls: p => num(p.phy.lossDwordSync) > 0 ? 'warn' : '' },
    { name: 'reset', header: 'RESET', value: p => p.phy.phyResetProblem || 0, sort: p => num(p.phy.phyResetProblem) || 0, num: true, cls: p => num(p.phy.phyResetProblem) > 0 ? 'warn' : '' },
    { name: 'last', header: 'LAST SEEN', value: p => when(p.lastSeen), sort: p => p.lastSeen ? new Date(p.lastSeen).getTime() : null, extra: true },
  ];

  // ---------- pages ----------
  function heading(text, sub) {
    const h = el('h1', { text: text });
    return sub ? el('div', null, h, el('p', { class: 'note', text: sub })) : h;
  }
  function kv(pairs) {
    const dl = el('dl', { class: 'kv' });
    for (const [k, v] of pairs) { dl.append(el('dt', { text: k }), el('dd', null, v instanceof Node ? v : dash(v))); }
    return dl;
  }
  const sinceDays = d => new Date(now() - d * 86400000).toISOString();

  // ---------- summary ----------
  function tile(label, value, opts) {
    const o = opts || {};
    const v = el('div', { class: 'value' });
    v.append(o.href ? link(String(value), o.href) : document.createTextNode(String(value)));
    const t = el('div', { class: 'tile' + (o.cls ? ' ' + o.cls : '') }, el('div', { class: 'label', text: label }), v);
    if (o.sub) t.append(el('div', { class: 'sub', text: o.sub }));
    return t;
  }
  async function pageSummary() {
    const [hosts, drives, missing, events, smart, sas, hw] = await Promise.all([
      rpc('ListHosts'), rpc('ListDrives'), rpc('ListMissing'), rpc('ListEvents', { limit: 15 }),
      rpc('ListSmart', { problems: true }), rpc('ListSASErrors', { since: sinceDays(7) }), rpc('ListEvents', { limit: 500, kinds: ['hardware_error'] }),
    ]);
    const weekAgo = now() - 7 * 86400000;
    const hwHosts = new Set((hw.events || []).filter(e => new Date(e.ts).getTime() >= weekAgo).map(e => e.hostname));
    const hs = hosts.hosts || [];
    const ds = drives.drives || [];
    const placed = ds.filter(d => d.current);
    const stale = hs.filter(h => h.staleSince).length;
    let total = 0;
    for (const d of placed) total += num(d.sizeBytes) || 0;
    const byStatus = {};
    for (const d of placed) byStatus[d.status] = (byStatus[d.status] || 0) + 1;
    const unused = placed.filter(d => !(d.current.uses || []).length).length;
    const byBus = {};
    for (const d of placed) { const b = busName(d.bus); byBus[b] = (byBus[b] || 0) + 1; }
    const gone = (missing.drives || []).filter(d => d.last).length;
    const ghosts = (missing.ghosts || []).length;
    const problems = (smart.rows || []).length;
    const sasRows = (sas.rows || []).length;
    const notOK = ds.length - placed.length;
    main.append(heading('Fleet', hs.length + ' hosts, ' + placed.length + ' drives present, ' + bytes(total) + ' of storage'));
    const tiles = el('div', { class: 'tiles' });
    tiles.append(
      tile('hosts', hs.length, { href: '#/hosts', sub: stale ? stale + ' stale' : 'all reporting', cls: stale ? 'bad' : '' }),
      tile('drives present', placed.length, { href: '#/drives', sub: Object.entries(byBus).sort().map(([k, v]) => v + ' ' + k).join(', ') }),
      tile('storage', bytes(total), { sub: 'on drives present now' }),
      tile('ok', byStatus.ok || 0, { href: '#/drives' }),
      tile('suspect', byStatus.suspect || 0, { href: '#/drives', cls: byStatus.suspect ? 'warn' : '' }),
      tile('bad', byStatus.bad || 0, { href: '#/drives', cls: byStatus.bad ? 'bad' : '' }),
      tile('unused', unused, { href: '#/drives', sub: 'present, in no pool or mount' }),
      tile('missing', gone, { href: '#/missing', sub: ghosts ? ghosts + ' pool ghosts' : 'no pool ghosts', cls: gone || ghosts ? 'warn' : '' }),
      tile('smart problems', problems, { href: '#/smart?problems=1', cls: problems ? 'warn' : '' }),
      tile('sas errors, 7d', sasRows, { href: '#/sas-errors', sub: sasRows ? 'phys with counter growth' : 'no counter growth', cls: sasRows ? 'warn' : '' }),
      tile('hardware errors, 7d', hwHosts.size, { href: '#/events?kind=hardware_error', sub: hwHosts.size ? [...hwHosts].sort().join(', ') : 'no memory or machine-check errors', cls: hwHosts.size ? 'bad' : '' }),
    );
    if (byStatus.shelved || byStatus.retired || notOK) {
      tiles.append(tile('not present', notOK, { href: '#/drives', sub: [byStatus.shelved ? byStatus.shelved + ' shelved' : '', byStatus.retired ? byStatus.retired + ' retired' : ''].filter(Boolean).join(', ') || 'known but absent' }));
    }
    main.append(tiles);
    main.append(el('h2', { text: 'Recent events' }), table({ key: 'summary.events', columns: eventCols, rows: events.events || [] }));
    main.append(el('p', { class: 'note' }, link('all events', '#/events')));
  }

  async function pageHosts() {
    const res = await rpc('ListHosts');
    main.append(heading('Hosts'), table({ key: 'hosts', columns: hostCols, rows: res.hosts || [] }));
  }
  async function pageHost(name) {
    const [hosts, drives, encls, events] = await Promise.all([rpc('ListHosts'), rpc('ListDrives', { host: name }), rpc('ListEnclosures'), rpc('ListEvents', { host: name, limit: 100 })]);
    const h = (hosts.hosts || []).find(x => x.hostname === name);
    if (!h) throw new Error('no host ' + name);
    main.append(heading(h.hostname), kv([
      ['agent', h.agentVersion], ['os', h.os], ['machine id', el('span', { class: 'mono', text: h.machineId || '' })],
      ['last report', ago(h.lastReport) + (h.staleSince ? ', stale since ' + when(h.staleSince) : '')], ['first seen', when(h.firstSeen)],
      ['booted', h.bootedAt ? when(h.bootedAt) + ', up ' + gap((now() - new Date(h.bootedAt).getTime()) / 1000) : 'unknown'],
      ['drives', String(h.driveCount || 0) + (h.missingCount ? ', ' + h.missingCount + ' missing' : '') + (h.ghostCount ? ', ' + h.ghostCount + ' ghosts' : '')],
      ['sas', link('topology', '#/sas/' + enc(name))],
    ]));
    const mine = (encls.enclosures || []).filter(e => e.hostname === name);
    if (mine.length) { main.append(el('h2', { text: 'Enclosures' }), table({ key: 'host.enclosures', columns: enclosureCols.filter(c => c.name !== 'host'), rows: mine })); }
    main.append(el('h2', { text: 'Drives' }), table({ key: 'host.drives', columns: driveCols.filter(c => c.name !== 'host'), rows: drives.drives || [] }));
    main.append(el('h2', { text: 'Recent events' }), table({ key: 'host.events', columns: eventCols.filter(c => c.name !== 'host'), rows: events.events || [] }));
  }
  async function pageDrives() {
    const res = await rpc('ListDrives');
    main.append(heading('Drives'), table({ key: 'drives', columns: driveCols, rows: res.drives || [] }));
  }
  async function pageDrive(ref) {
    const [g, hist, smart, io, kernel] = await Promise.all([
      rpc('GetDrive', { ref }), rpc('GetDriveHistory', { ref }),
      rpc('GetSmart', { ref, since: sinceDays(30) }), rpc('GetIO', { ref, since: sinceDays(7) }), rpc('GetKernel', { ref, since: sinceDays(7) }),
    ]);
    const d = g.drive;
    const p = d.current || d.last;
    main.append(heading(d.serial, [d.vendor, d.model].filter(Boolean).join(' ') + '  ' + bytes(d.sizeBytes) + '  ' + busName(d.bus)));
    main.append(kv([
      ['status', el('span', { class: d.status === 'ok' ? 'ok' : 'warn', text: d.status + (g.lastStatus ? '  (' + describe(g.lastStatus) + ')' : '') })],
      ['wwn', el('span', { class: 'mono', text: d.wwn || '-' })],
      ['keys', el('span', { class: 'mono', text: (g.keys || []).join(', ') })],
      [d.current ? 'now' : 'last', p ? el('span', null, link(p.hostname, '#/host/' + enc(p.hostname)), '  ', slotLink(p), '  ', dash(p.devName), '  ', useSummary(p.uses), '  since ' + when(p.firstSeen) + (d.current ? ', confirmed ' + when(p.lastSeen) : ', until ' + when(p.endedAt) + ' (' + p.endReason + ')')) : '-'],
      ['zfs', d.memberState],
    ]));
    main.append(el('h2', { text: 'History' }), table({ key: 'drive.events', columns: eventCols.filter(c => c.name !== 'drive'), rows: hist.events || [], sort: '-time' }));
    main.append(el('h2', { text: 'Placements' }), table({ key: 'drive.placements', rows: hist.placements || [], columns: [
      { name: 'host', header: 'HOST', value: q => link(q.hostname, '#/host/' + enc(q.hostname)) },
      { name: 'slot', header: 'SLOT', value: q => slotLink(q) },
      { name: 'device', header: 'DEVICE', value: q => dash(q.devName), mono: true },
      { name: 'uses', header: 'USES', value: q => useSummary(q.uses) },
      { name: 'from', header: 'FROM', value: q => when(q.firstSeen), sort: q => q.firstSeen ? new Date(q.firstSeen).getTime() : null },
      { name: 'to', header: 'TO', value: q => q.endedAt ? when(q.endedAt) + ' (' + q.endReason + ')' : 'now', sort: q => q.endedAt ? new Date(q.endedAt).getTime() : Infinity },
      { name: 'confirmed', header: 'LAST CONFIRMED', value: q => when(q.lastSeen), sort: q => q.lastSeen ? new Date(q.lastSeen).getTime() : null },
    ] }));
    const smartRows = (smart.samples || []).map(s => ({ sample: s, drive: d }));
    main.append(el('h2', { text: 'SMART, last 30 days' }));
    if (!smartRows.length) main.append(el('p', { class: 'note', text: 'no samples' }));
    else main.append(table({ key: 'drive.smart', rows: smartRows, sort: '-time', columns: [
      { name: 'time', header: 'TIME', value: r => when(r.sample.ts), sort: r => new Date(r.sample.ts).getTime() },
      { name: 'host', header: 'HOST', value: r => r.sample.hostname || '' },
      { name: 'health', header: 'HEALTH', value: r => r.sample.skipped ? 'skipped: ' + r.sample.skipped : (r.sample.summary && r.sample.summary.healthy === false ? 'FAILED' : (r.sample.summary && r.sample.summary.healthy === true ? 'ok' : '-')), cls: r => r.sample.summary && r.sample.summary.healthy === false ? 'bad' : '' },
      ...smartCols.filter(c => ['hours', 'temp', 'realloc', 'pending', 'uncorr', 'crc', 'wear', 'selftest', 'read', 'written'].includes(c.name)).map(c => ({ ...c, extra: false })),
    ] }));
    main.append(el('h2', { text: 'I/O, last 7 days' }));
    if (!(io.samples || []).length) main.append(el('p', { class: 'note', text: 'no samples' }));
    else main.append(table({ key: 'drive.io', rows: io.samples, sort: '-start', columns: [
      { name: 'start', header: 'START', value: s => when(s.bucketStart), sort: s => new Date(s.bucketStart).getTime() },
      { name: 'span', header: 'SPAN', value: s => Math.round((s.bucketSecs || 0) / 60) + 'm', num: true },
      { name: 'host', header: 'HOST', value: s => s.hostname || '' },
      { name: 'reads', header: 'READS', value: s => dash(s.reads), sort: s => num(s.reads), num: true },
      { name: 'writes', header: 'WRITES', value: s => dash(s.writes), sort: s => num(s.writes), num: true },
      { name: 'read', header: 'READ', value: s => bytes(s.readBytes), sort: s => num(s.readBytes), num: true },
      { name: 'written', header: 'WRITTEN', value: s => bytes(s.writeBytes), sort: s => num(s.writeBytes), num: true },
      { name: 'r_await', header: 'R_AWAIT', value: s => num(s.awaitReads) ? ms(num(s.readMs) / num(s.awaitReads)) : '-', sort: s => num(s.awaitReads) ? num(s.readMs) / num(s.awaitReads) : null, num: true },
      { name: 'w_await', header: 'W_AWAIT', value: s => num(s.awaitWrites) ? ms(num(s.writeMs) / num(s.awaitWrites)) : '-', sort: s => num(s.awaitWrites) ? num(s.writeMs) / num(s.awaitWrites) : null, num: true },
      { name: 'util', header: 'UTIL', value: s => s.bucketSecs ? pct(num(s.ioMs) / (s.bucketSecs * 1000)) : '-', sort: s => s.bucketSecs ? num(s.ioMs) / (s.bucketSecs * 1000) : null, num: true },
      { name: 'r_max', header: 'R_MAX', value: s => ms(s.rAwaitMaxMs), sort: s => num(s.rAwaitMaxMs), num: true },
      { name: 'w_max', header: 'W_MAX', value: s => ms(s.wAwaitMaxMs), sort: s => num(s.wAwaitMaxMs), num: true },
      { name: 'dropped', header: 'DROPPED', value: s => s.glitches || '-', num: true, extra: true },
    ] }));
    main.append(el('h2', { text: 'Kernel log, last 7 days' }));
    if (!(kernel.samples || []).length) main.append(el('p', { class: 'note', text: 'nothing logged' }));
    else main.append(table({ key: 'drive.kernel', rows: kernel.samples, sort: '-hour', columns: [
      { name: 'hour', header: 'HOUR', value: s => when(s.bucketStart), sort: s => new Date(s.bucketStart).getTime() },
      { name: 'class', header: 'CLASS', value: s => s.class, cls: s => ['predictive_failure', 'medium_error', 'hardware_error'].includes(s.class) ? 'bad' : '' },
      { name: 'code', header: 'CODE', value: s => dash(s.scsiCode), mono: true },
      { name: 'count', header: 'COUNT', value: s => s.count || 0, sort: s => s.count || 0, num: true },
      { name: 'sample', header: 'SAMPLE', value: s => s.sample || '', mono: true },
    ] }));
  }
  async function pageEnclosures() {
    const res = await rpc('ListEnclosures');
    main.append(heading('Enclosures'), table({ key: 'enclosures', columns: enclosureCols, rows: res.enclosures || [] }));
  }
  async function pageEnclosure(key) {
    const res = await rpc('ListBays', { ref: key });
    const e = res.enclosure || {};
    main.append(heading(e.name || e.via || key, [e.product, e.via, e.hostname ? 'on ' + e.hostname : ''].filter(Boolean).join('  ')));
    main.append(kv([['host', e.hostname ? link(e.hostname, '#/host/' + enc(e.hostname)) : '-'], ['key', el('span', { class: 'mono', text: e.enclosure || key })], ['profile', e.profile || 'none: bays are as the firmware names them'], ['note', e.note]]));
    const bays = res.bays || [];
    if (res.rows > 0 && res.columns > 0) {
      const grid = el('div', { class: 'grid' });
      grid.style.gridTemplateColumns = 'repeat(' + res.columns + ', max-content)';
      const declared = bays.filter(b => b.declared);
      const cells = [];
      declared.forEach((b, i) => {
        const r = res.order === 'row-major' ? Math.floor(i / res.columns) : i % res.rows;
        const c = res.order === 'row-major' ? i % res.columns : Math.floor(i / res.rows);
        cells.push({ r, c, b });
      });
      for (let r = 0; r < res.rows; r++) for (let c = 0; c < res.columns; c++) {
        const found = cells.find(x => x.r === r && x.c === c);
        const cell = el('div', { class: 'cell' + (found && found.b.present ? '' : ' empty') });
        if (found) {
          cell.append(el('div', { class: 'bay', text: 'bay ' + found.b.label }));
          cell.append(found.b.present ? link(found.b.serial || found.b.devName, '#/drive/' + enc(found.b.serial || '')) : document.createTextNode('empty'));
        }
        grid.append(cell);
      }
      main.append(el('h2', { text: 'Layout' }), grid);
    }
    main.append(el('h2', { text: 'Bays' }), table({ key: 'enclosure.bays', rows: bays, columns: [
      { name: 'bay', header: 'BAY', value: b => b.label + (b.declared ? '' : ' (not in profile)') },
      { name: 'ids', header: 'IDS', value: b => (b.ids || []).join(' ') || '-', mono: true },
      { name: 'device', header: 'DEVICE', value: b => b.present ? dash(b.devName) : 'empty', mono: true, cls: b => b.present ? '' : 'muted' },
      { name: 'serial', header: 'SERIAL', value: b => b.present && b.serial ? link(b.serial, '#/drive/' + enc(b.serial)) : '', mono: true },
      { name: 'model', header: 'MODEL', value: b => b.model || '' },
      { name: 'status', header: 'STATUS', value: b => b.status || '' },
      { name: 'uses', header: 'USES', value: b => b.present ? useSummary(b.uses) : '' },
    ] }));
  }
  async function pageSmart(q) {
    const problems = q.get('problems') === '1';
    const res = await rpc('ListSmart', { problems });
    const toggle = el('input', { type: 'checkbox' });
    toggle.checked = problems;
    toggle.addEventListener('change', () => { location.hash = '#/smart' + (toggle.checked ? '?problems=1' : ''); });
    main.append(heading('SMART', 'the newest reading per drive, problems first'), el('div', { class: 'toolbar' }, el('label', null, toggle, ' problems only')));
    main.append(table({ key: 'smart', columns: smartCols, rows: res.rows || [] }));
  }
  async function pageIO(q) {
    const host = q.get('host') || '';
    const res = await rpc('CompareIO', { host, since: sinceDays(1) });
    main.append(heading('I/O, last 24 hours', 'each drive next to the median of its vdev; ×3 or worse is worth a look'));
    main.append(table({ key: 'io', columns: ioCols, rows: res.rows || [] }));
  }
  async function pageSASErrors() {
    const res = await rpc('ListSASErrors', { since: sinceDays(7) });
    main.append(heading('SAS errors, last 7 days', 'phys whose error counters grew, most first'));
    if (!(res.rows || []).length) main.append(el('p', { class: 'note', text: 'no counter growth in the window' }));
    else main.append(table({ key: 'saserrors', columns: sasErrorCols, rows: res.rows }));
  }
  async function pageSAS(host) {
    const res = await rpc('GetSAS', { host });
    main.append(heading('SAS topology: ' + host, 'wide ports are heavy lines; a bay links to its drive, hover for detail'));
    const nodes = (res.nodes || []).filter(n => !n.goneAt);
    if (!nodes.length) main.append(el('p', { class: 'note', text: 'no SAS topology reported' }));
    else {
      try {
        await sasDiagram(main, host, res);
      } catch (e) {
        main.append(el('p', { class: 'note', text: 'no diagram: ' + e.message }));
      }
    }
    for (const n of nodes) {
      const node = n.node;
      const phys = (res.phys || []).filter(p => !p.goneAt && p.phy.ownerAddress === node.address);
      main.append(el('h2', { text: node.name + '  ' + [node.vendor, node.product, node.revision].filter(Boolean).join(' ') + '  ' + node.address + (node.upstreamPort ? '  via ' + node.upstreamPort : '') }));
      main.append(table({ key: 'sas.phys', columns: phyCols.filter(c => c.name !== 'node'), rows: phys }));
    }
  }
  async function pageEvents(q) {
    const kind = q.get('kind') || '';
    const res = await rpc('ListEvents', { limit: 500, kinds: kind ? [kind] : [] });
    const kinds = ['', 'first_seen', 'appeared', 'vanished', 'reappeared', 'moved_host', 'moved_bay', 'use_changed', 'enclosure_renamed', 'member_state_changed', 'status_changed', 'note', 'merged', 'host_merged', 'smart_warning', 'kernel_warning', 'sas_link_changed', 'sas_attached_changed', 'sas_port_changed', 'sas_errors', 'sas_node_changed', 'host_first_seen', 'host_stale', 'host_resumed', 'host_rebooted', 'hardware_error', 'report_degraded', 'pool_missing_member', 'identity_conflict'];
    const sel = el('select');
    for (const k of kinds) { const o = el('option', { value: k, text: k || 'every kind' }); if (k === kind) o.selected = true; sel.append(o); }
    sel.addEventListener('change', () => { location.hash = '#/events' + (sel.value ? '?kind=' + enc(sel.value) : ''); });
    main.append(heading('Events', 'the newest 500'), el('div', { class: 'toolbar' }, el('label', null, 'kind ', sel)));
    main.append(table({ key: 'events', columns: eventCols, rows: res.events || [] }));
  }
  async function pageMissing() {
    const res = await rpc('ListMissing');
    main.append(heading('Missing', 'drives that vanished and are not marked bad, shelved or retired'));
    const gone = (res.drives || []).filter(d => d.last);
    if (!gone.length) main.append(el('p', { class: 'note', text: 'nothing missing' }));
    else main.append(table({ key: 'missing', rows: gone, columns: [
      { name: 'serial', header: 'SERIAL', value: d => link(d.serial, '#/drive/' + enc(d.serial)), sort: d => d.serial, mono: true },
      { name: 'model', header: 'MODEL', value: d => d.model },
      { name: 'status', header: 'STATUS', value: d => d.status },
      { name: 'host', header: 'LAST HOST', value: d => link(d.last.hostname, '#/host/' + enc(d.last.hostname)), sort: d => d.last.hostname },
      { name: 'slot', header: 'LAST SLOT', value: d => slotLink(d.last), sort: d => slotText(d.last) },
      { name: 'uses', header: 'LAST USES', value: d => useSummary(d.last.uses) },
      { name: 'confirmed', header: 'LAST CONFIRMED', value: d => when(d.last.lastSeen), sort: d => new Date(d.last.lastSeen).getTime() },
      { name: 'gone', header: 'NOTICED GONE', value: d => when(d.last.endedAt), sort: d => new Date(d.last.endedAt).getTime() },
    ] }));
    if ((res.ghosts || []).length) {
      main.append(el('h2', { text: 'Pool members with no present device' }), table({ key: 'ghosts', rows: res.ghosts, columns: [
        { name: 'host', header: 'HOST', value: g => link(g.hostname, '#/host/' + enc(g.hostname)) },
        { name: 'pool', header: 'POOL', value: g => g.pool },
        { name: 'member', header: 'MEMBER', value: g => g.path, mono: true },
        { name: 'state', header: 'STATE', value: g => g.state },
        { name: 'drive', header: 'DRIVE', value: g => g.serial ? link(g.serial, '#/drive/' + enc(g.serial)) : '-', mono: true },
        { name: 'since', header: 'SINCE', value: g => when(g.firstSeen) },
      ] }));
    }
  }


  // ---------- SAS topology diagram ----------
  // Mermaid, loaded from the CDN the policy admits the first time a
  // topology page opens. The diagram source is built from data, so every
  // label goes through mlabel(), which keeps only characters that cannot
  // open a Mermaid construct, and Mermaid runs at securityLevel strict
  // with SVG text labels, no HTML. The page never touches the SVG's
  // markup afterwards, only adds <title> tooltips and click handlers.
  const MERMAID_URL = 'https://cdn.jsdelivr.net/npm/mermaid@11.17.2/dist/mermaid.min.js';
  let mermaidLoading = null;
  function loadMermaid() {
    if (mermaidLoading) return mermaidLoading;
    mermaidLoading = new Promise((resolve, reject) => {
      const s = el('script', { src: MERMAID_URL });
      s.addEventListener('load', () => {
        const dark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
        window.mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: dark ? 'dark' : 'neutral', flowchart: { htmlLabels: false, curve: 'basis', padding: 6, nodeSpacing: 10, rankSpacing: 22 }, maxTextSize: 400000, maxEdges: 4000 });
        resolve(window.mermaid);
      });
      s.addEventListener('error', () => { mermaidLoading = null; reject(new Error('could not load Mermaid from ' + MERMAID_URL)); });
      document.head.append(s);
    });
    return mermaidLoading;
  }
  const mlabel = v => String(v === undefined || v === null ? '' : v).replace(/[^A-Za-z0-9 ._:/#×·+-]/g, '?').slice(0, 60) || '?';
  const gbit = r => { r = num(r); if (!r) return ''; return (Number.isInteger(r) ? r : r.toFixed(1)) + 'G'; };

  // sasDiagram draws host -> HBAs -> expanders -> the enclosures each one
  // reaches, with a tiny node per bay laid out as the hardware profile
  // says (else twelve across). Wide ports are heavy lines labelled with
  // their rate and width; a bay's node links to the drive in it.
  async function sasDiagram(into, host, sas) {
    const nodes = (sas.nodes || []).filter(n => !n.goneAt).map(n => n.node);
    if (!nodes.length) return;
    const phys = (sas.phys || []).filter(p => !p.goneAt);
    const byName = {};
    for (const n of nodes) byName[n.name] = n;
    const encl = await rpc('ListEnclosures');
    const enclosures = (encl.enclosures || []).filter(e => e.hostname === host && byName[e.via]);
    const bays = {};
    for (const e of enclosures) bays[e.enclosure] = await rpc('ListBays', { ref: e.enclosure });

    const lines = ['flowchart TD'];
    const ids = new Map();      // node id -> { hash, tip }
    let seq = 0;
    const id = () => 'n' + (seq++);
    const label = v => '["' + mlabel(v) + '"]';
    const rateOf = {};          // drive serial -> rate label
    for (const p of phys) if (p.phy.attachedKind === 'drive' && p.serial) rateOf[p.serial] = gbit(p.phy.rateGbit);

    const hostId = id();
    lines.push(hostId + label(host) + ':::host');
    ids.set(hostId, { hash: '#/host/' + enc(host), tip: host });
    const nodeId = {};
    for (const n of nodes) {
      const nid = id();
      nodeId[n.address] = nid;
      const what = [n.vendor, n.product].filter(Boolean).join(' ');
      lines.push(nid + label(n.name.toUpperCase() + (what ? ' · ' + what : '')) + ':::' + (n.kind === 'hba' ? 'hba' : 'expander'));
      ids.set(nid, { hash: '#/sas/' + enc(host) + '#' + n.name, tip: [n.name, what, n.revision ? 'fw ' + n.revision : '', n.address].filter(Boolean).join('\n') });
    }
    // Upstream links: an HBA hangs off the host; an expander off its parent
    // through the parent's port, one edge per port, heavy when wide.
    for (const n of nodes) {
      if (!n.parentAddress || !nodeId[n.parentAddress]) { lines.push(hostId + ' --> ' + nodeId[n.address]); continue; }
      const ports = {};
      for (const p of phys) {
        if (p.phy.ownerAddress !== n.parentAddress || p.phy.attachedAddress !== n.address) continue;
        const key = p.phy.port || p.phy.name;
        (ports[key] = ports[key] || []).push(p.phy);
      }
      const keys = Object.keys(ports);
      if (!keys.length) { lines.push(nodeId[n.parentAddress] + ' --> ' + nodeId[n.address]); continue; }
      for (const key of keys) {
        const group = ports[key];
        const width = Math.max(group.length, group[0].portWidth || 0);
        const text = [gbit(group[0].rateGbit), width > 1 ? 'x' + width : ''].filter(Boolean).join(' ');
        lines.push(nodeId[n.parentAddress] + (width > 1 ? ' ==>' : ' -->') + (text ? '|"' + mlabel(text) + '"|' : '') + ' ' + nodeId[n.address]);
      }
    }
    // Enclosures: a subgraph of bay nodes under the node that reaches it.
    for (const e of enclosures) {
      const view = bays[e.enclosure] || {};
      const list = view.bays || [];
      if (!list.length) continue;
      const sid = id();
      const title = [e.name || e.product || e.enclosure, e.name && e.product ? e.product : ''].filter(Boolean).join(' · ');
      lines.push('subgraph ' + sid + label(title));
      lines.push('direction TB');
      ids.set(sid, { hash: '#/enclosure/' + enc(e.enclosure), tip: title });
      let columns = view.columns > 0 ? view.columns : Math.min(list.length, 12);
      let rows = Math.ceil(list.length / columns);
      const cell = [];
      list.forEach((b, i) => {
        const bid = id();
        const text = /^\d+$/.test(b.label || '') ? '#' + b.label : (b.label || '?');
        lines.push(bid + label(text) + ':::' + (b.present ? 'drive' : 'empty'));
        const tip = b.present ? [text, b.serial, b.model, b.devName, rateOf[b.serial] || '', b.status, useSummary(b.uses)].filter(v => v && v !== '-').join('\n') : text + '\nempty';
        ids.set(bid, { hash: b.present && b.serial ? '#/drive/' + enc(b.serial) : '', tip });
        const r = view.order === 'column-major' ? i % rows : Math.floor(i / columns);
        const c = view.order === 'column-major' ? Math.floor(i / rows) : i % columns;
        (cell[r] = cell[r] || [])[c] = bid;
      });
      // Invisible edges down each column keep the bays in a grid.
      for (let r = 1; r < cell.length; r++) for (let c = 0; c < columns; c++) {
        if (cell[r] && cell[r][c] && cell[r - 1] && cell[r - 1][c]) lines.push(cell[r - 1][c] + ' ~~~ ' + cell[r][c]);
      }
      lines.push('end');
      const rates = new Set();
      for (const b of list) if (b.present && rateOf[b.serial]) rates.add(rateOf[b.serial]);
      const rate = [...rates].sort((a, b) => parseFloat(b) - parseFloat(a)).join('/');
      lines.push(nodeId[byName[e.via].address] + ' -->' + (rate ? '|"' + mlabel(rate) + '"|' : '') + ' ' + sid);
    }
    lines.push('classDef host font-weight:bold');
    lines.push('classDef hba stroke-width:2px');
    lines.push('classDef expander stroke-width:2px');
    lines.push('classDef drive font-size:11px');
    lines.push('classDef empty font-size:11px,stroke-dasharray:3 2,opacity:0.45');

    const pre = el('pre', { class: 'mermaid', text: lines.join('\n') });
    const box = el('div', { class: 'diagram' }, pre);
    into.append(box);   // Mermaid measures text, so the element must be in the document
    const mermaid = await loadMermaid();
    try {
      await mermaid.run({ nodes: [pre], suppressErrors: false });
    } catch (e) {
      box.remove();
      throw e;
    }
    // Drawn at its natural size; the container scrolls sideways.
    const svg = box.querySelector('svg');
    if (svg && svg.viewBox && svg.viewBox.baseVal.width) svg.setAttribute('width', Math.ceil(svg.viewBox.baseVal.width));
    // Tooltips and links go on the rendered nodes by their Mermaid ids,
    // which end in the id given in the source.
    for (const g of box.querySelectorAll('g.node, g.cluster')) {
      const m = (g.id || '').match(/flowchart-(n\d+)-\d+$/) || (g.id || '').match(/(?:^|-)(n\d+)$/);
      const info = m ? ids.get(m[1]) : null;
      if (!info) continue;
      if (info.tip) { const t = document.createElementNS('http://www.w3.org/2000/svg', 'title'); t.textContent = info.tip; g.prepend(t); }
      if (info.hash) { g.classList.add('clickable'); g.addEventListener('click', () => { location.hash = info.hash; }); }
    }
  }

  // ---------- router ----------
  const routes = [
    [/^\/?$/, () => pageSummary()],
    [/^\/hosts$/, () => pageHosts()],
    [/^\/host\/([^/]+)$/, m => pageHost(decodeURIComponent(m[1]))],
    [/^\/drives$/, () => pageDrives()],
    [/^\/drive\/([^/]+)$/, m => pageDrive(decodeURIComponent(m[1]))],
    [/^\/enclosures$/, () => pageEnclosures()],
    [/^\/enclosure\/([^/]+)$/, m => pageEnclosure(decodeURIComponent(m[1]))],
    [/^\/smart$/, (m, q) => pageSmart(q)],
    [/^\/io$/, (m, q) => pageIO(q)],
    [/^\/sas-errors$/, () => pageSASErrors()],
    [/^\/sas\/([^/]+)$/, m => pageSAS(decodeURIComponent(m[1]))],
    [/^\/events$/, (m, q) => pageEvents(q)],
    [/^\/missing$/, () => pageMissing()],
  ];
  async function route() {
    const hash = location.hash.replace(/^#/, '') || '/';
    const [path, query] = hash.split('?');
    const q = new URLSearchParams(query || '');
    const section = path === '/' ? '#/' : '#' + path.split('/').slice(0, 2).join('/');
    for (const a of document.querySelectorAll('nav a')) a.className = (a.getAttribute('href') === section) ? 'active' : '';
    clear(main);
    if (!getToken()) { askToken(); return; }
    main.append(el('p', { class: 'note', text: 'loading…' }));
    try {
      for (const [re, handler] of routes) {
        const m = path.match(re);
        if (m) { clear(main); await handler(m, q); return; }
      }
      clear(main);
      main.append(el('p', { class: 'error', text: 'no such page: ' + path }));
    } catch (e) {
      clear(main);
      if (e instanceof AuthError) { setToken(''); askToken('That token was refused. Enter another.'); return; }
      main.append(el('p', { class: 'error', text: 'error: ' + e.message }));
    }
  }
  window.addEventListener('hashchange', route);
  async function start() {
    if (demoMeta) {
      try {
        const m = await (await fetch(demoMeta.getAttribute('content'))).json();
        demo = { asOf: new Date(m.asOf).getTime() };
        document.getElementById('token').hidden = true;
        document.querySelector('header').append(el('span', { class: 'demo', text: 'demo: a snapshot of a real fleet as of ' + when(m.asOf) + ', names changed' }));
      } catch (e) {
        main.append(el('p', { class: 'error', text: 'demo data unreadable: ' + e.message }));
        return;
      }
    }
    route();
  }
  start();
})();
