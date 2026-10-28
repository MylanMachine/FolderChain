'use strict';

// ---------- icons (inline SVG, Lucide-style strokes) ----------
const ICONS = {
  plus: '<path d="M12 5v14M5 12h14"/>',
  link: '<path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  'folder-big': '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M8 13h8"/>',
  pause: '<rect x="6" y="5" width="4" height="14" rx="1"/><rect x="14" y="5" width="4" height="14" rx="1"/>',
  play: '<path d="M7 5v14l11-7z"/>',
  gear: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  external: '<path d="M14 4h6v6M20 4l-9 9"/><path d="M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/>',
  refresh: '<path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/>',
  shield: '<path d="M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z"/><path d="M9 12l2 2 4-4"/>',
  down: '<path d="M12 4v14M6 12l6 6 6-6"/>',
  up: '<path d="M12 20V6M6 12l6-6 6 6"/>',
  trash: '<path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/>',
  alert: '<path d="M12 9v4M12 17h.01"/><path d="M10.3 3.9L2 18a2 2 0 0 0 1.7 3h16.6a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/>',
  split: '<path d="M16 3h5v5M8 3H3v5M21 3l-7 7M3 3l7 7M12 22v-8"/>',
  plug: '<path d="M9 2v6M15 2v6M6 8h12v4a6 6 0 0 1-12 0zM12 18v4"/>',
  check: '<path d="M5 12l5 5L20 7"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>',
  x: '<path d="M6 6l12 12M18 6L6 18"/>',
};
const svg = (name) => `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name] || ''}</svg>`;
function paintIcons(root = document) {
  for (const el of root.querySelectorAll('[data-icon]')) {
    if (!el.firstChild) el.innerHTML = svg(el.dataset.icon);
  }
}

// ---------- API ----------
const params = new URLSearchParams(location.search);
if (params.get('token')) {
  sessionStorage.setItem('fc-token', params.get('token'));
  history.replaceState(null, '', location.pathname);
}
const TOKEN = sessionStorage.getItem('fc-token') || '';

async function api(path, body) {
  const res = await fetch('/api/' + path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { 'X-FC-Token': TOKEN, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

// ---------- helpers ----------
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

function fmtBytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  return (n / 1024 ** i).toFixed(i && n / 1024 ** i < 10 ? 1 : 0) + ' ' + u[i];
}
function ago(t) {
  const d = new Date(t);
  if (!t || d.getFullYear() < 2000) return 'never';
  const s = Math.max(0, (Date.now() - d) / 1000);
  if (s < 10) return 'just now';
  if (s < 60) return Math.floor(s) + 's ago';
  if (s < 3600) return Math.floor(s / 60) + ' min ago';
  if (s < 86400) return Math.floor(s / 3600) + ' h ago';
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}
function clock(t) {
  const d = new Date(t);
  const today = new Date().toDateString() === d.toDateString();
  return today ? d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
               : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}
function initials(name) {
  const parts = String(name || '?').split(/[\s\-_.]+/).filter(Boolean);
  return ((parts[0]?.[0] || '?') + (parts[1]?.[0] || '')).toUpperCase();
}
function baseName(p) { return String(p || '').split(/[\\/]/).filter(Boolean).pop() || p; }

let toastTimer;
function toast(msg) {
  const t = $('toast');
  t.textContent = msg;
  try { t.showPopover(); } catch {}
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { try { t.hidePopover(); } catch {} }, 2200);
}

// ---------- rendering ----------
let state = null;

function render(s) {
  state = s;
  $('setup').hidden = s.configured;
  $('app').hidden = !s.configured;
  if (!s.configured) return;

  const pill = $('status-pill');
  pill.dataset.s = s.status;
  $('status-text').textContent = s.statusText;
  const pb = $('pause-btn');
  pb.classList.toggle('active', s.paused);
  pb.title = s.paused ? 'Resume syncing' : 'Pause syncing';
  pb.innerHTML = svg(s.paused ? 'play' : 'pause');

  const banner = $('banner');
  const msg = s.folderError || (s.problems.length ? s.problems.slice(0, 3).join(' · ') : '');
  banner.hidden = !msg;
  banner.className = 'banner' + (s.folderError ? ' bad' : '');
  banner.textContent = msg;

  $('folder-name').textContent = baseName(s.folder);
  $('folder-path').textContent = s.folder;
  $('stat-files').textContent = s.files.toLocaleString();
  $('stat-size').textContent = fmtBytes(s.bytes);
  $('stat-devices').textContent = s.devices.filter((d) => d.online).length;
  $('stat-last').textContent = ago(s.lastSync);

  // transfers
  $('transfers-card').hidden = !s.transfers.length;
  $('transfers').innerHTML = s.transfers.map((t) => {
    const pct = t.size ? Math.min(100, (t.done / t.size) * 100) : 100;
    return `<li><div class="transfer-row">${svg(t.upload ? 'up' : 'down')}
      <span class="name" title="${esc(t.path)}">${esc(t.path)}</span>
      <span class="meta">${t.upload ? 'to' : 'from'} ${esc(t.peer)} · ${fmtBytes(t.done)} of ${fmtBytes(t.size)}</span></div>
      <div class="bar"><i style="width:${pct.toFixed(1)}%"></i></div></li>`;
  }).join('');

  // devices
  const self = `<li class="device"><div class="avatar on self">${esc(initials(s.deviceName))}</div>
    <div class="device-info"><div class="device-name">${esc(s.deviceName)}<em>this computer</em></div>
    <div class="device-sub">${esc((s.localIPs || []).join(', ') || 'No network')}</div></div></li>`;
  const others = s.devices.map((d) => {
    let sub, cls = '';
    if (d.online && d.inSync) { sub = 'Up to date'; cls = 'ok'; }
    else if (d.online) { sub = d.pending ? `Syncing · ${d.pending} item${d.pending === 1 ? '' : 's'}` : 'Connecting…'; cls = 'busy'; }
    else sub = 'Offline · seen ' + ago(d.lastSeen);
    const addr = d.online && d.addr ? ` · ${esc(d.addr)}` : '';
    const forget = d.online ? '' : `<button type="button" class="icon-btn forget" data-forget="${esc(d.id)}" title="Remove from list">${svg('x')}</button>`;
    return `<li class="device"><div class="avatar ${d.online ? 'on' : ''}">${esc(initials(d.name))}</div>
      <div class="device-info"><div class="device-name">${esc(d.name)}</div>
      <div class="device-sub ${cls}">${esc(sub)}${addr}</div></div>${forget}</li>`;
  }).join('');
  $('devices').innerHTML = self + (others || '<li class="empty">No other devices yet. Click <b>Add a device</b> to connect another computer.</li>');

  // activity
  const evIcon = { in: 'down', out: 'up', del: 'trash', conflict: 'split', peer: 'plug', warn: 'alert', info: 'info' };
  $('activity').innerHTML = (s.events || []).map((e) => `<li class="ev-${esc(e.kind)}">
    <span class="ev-icon">${svg(evIcon[e.kind] || 'info')}</span>
    <span class="ev-text">${esc(e.text)}</span><span class="ev-time">${esc(clock(e.time))}</span></li>`).join('')
    || '<li class="empty">Nothing yet — changes will show up here.</li>';
}

let polling = false;
async function refresh() {
  if (polling) return;
  polling = true;
  try { render(await api('state')); } catch (e) { $('status-text') && ($('status-text').textContent = 'Disconnected'); }
  finally { polling = false; }
}
setInterval(() => { if (!window.__fcHidden) refresh(); }, 1000);

// ---------- onboarding ----------
let setupMode = 'create';
for (const btn of document.querySelectorAll('.choice')) {
  btn.addEventListener('click', async () => {
    setupMode = btn.dataset.mode;
    $('key-field').hidden = setupMode !== 'join';
    document.querySelector('[data-step=choose]').hidden = true;
    $('setup-form').hidden = false;
    $('setup-error').textContent = '';
    const d = await api('defaults').catch(() => ({}));
    if (!$('setup-name').value) $('setup-name').value = d.deviceName || '';
    if (!$('setup-folder').value) $('setup-folder').value = d.suggestedFolder || '';
    (setupMode === 'join' ? $('setup-key') : $('setup-folder')).focus();
  });
}
$('setup-back').addEventListener('click', () => {
  document.querySelector('[data-step=choose]').hidden = false;
  $('setup-form').hidden = true;
});
async function pickFolder(input) {
  try {
    const r = await api('pick-folder', { start: input.value });
    if (r.path) input.value = r.path;
  } catch (e) { toast(e.message); }
}
$('setup-browse').addEventListener('click', () => pickFolder($('setup-folder')));
$('setup-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const go = $('setup-go');
  go.disabled = true;
  $('setup-error').textContent = '';
  try {
    await api('setup', {
      mode: setupMode, key: $('setup-key').value, folder: $('setup-folder').value,
      name: $('setup-name').value, autostart: $('setup-autostart').checked,
    });
    await refresh();
    if (setupMode === 'create') openAddDevice();
  } catch (e) {
    $('setup-error').textContent = e.message;
  } finally {
    go.disabled = false;
  }
});

// ---------- main actions ----------
$('open-folder').addEventListener('click', () => api('open-folder', {}).catch((e) => toast(e.message)));
$('pause-btn').addEventListener('click', async () => {
  await api('pause', { paused: !state?.paused }).catch((e) => toast(e.message));
  refresh();
});
$('rescan-btn').addEventListener('click', async () => { await api('rescan', {}); toast('Checking for changes…'); });
$('devices').addEventListener('click', async (ev) => {
  const b = ev.target.closest('[data-forget]');
  if (b) { await api('forget', { id: b.dataset.forget }); refresh(); }
});

async function openAddDevice() {
  const r = await api('share-key').catch(() => ({}));
  $('share-key').textContent = r.key || '';
  $('add-dialog').showModal();
}
$('add-device-btn').addEventListener('click', openAddDevice);
$('copy-key').addEventListener('click', async () => {
  try { await navigator.clipboard.writeText($('share-key').textContent); toast('Share key copied'); }
  catch {
    const r = document.createRange(); r.selectNodeContents($('share-key'));
    getSelection().removeAllRanges(); getSelection().addRange(r);
    toast('Press Ctrl+C to copy');
  }
});

// ---------- settings ----------
$('settings-btn').addEventListener('click', () => {
  $('set-name').value = state.deviceName;
  $('set-folder').value = state.folder;
  $('set-autostart').checked = !!state.autostart;
  $('set-peers').value = (state.extraPeers || []).join(', ');
  $('about').textContent = `FolderChain ${state.version} · device ${state.deviceId} · port ${state.listenPort || '—'}`;
  $('settings-dialog').showModal();
});
$('settings-cancel').addEventListener('click', () => $('settings-dialog').close());
$('settings-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  try {
    if ($('set-folder').value !== state.folder) await api('change-folder', { folder: $('set-folder').value });
    await api('settings', {
      deviceName: $('set-name').value,
      autostart: $('set-autostart').checked,
      extraPeers: $('set-peers').value.split(/[,;\s]+/).filter(Boolean),
    });
    $('settings-dialog').close();
    toast('Settings saved');
    refresh();
  } catch (e) { toast(e.message); }
});
$('set-browse').addEventListener('click', () => pickFolder($('set-folder')));
$('firewall-btn').addEventListener('click', async () => {
  try { await api('firewall', {}); toast('Firewall rule added'); } catch (e) { toast(e.message); }
});
$('archive-btn').addEventListener('click', () => api('open-folder', { archive: true }).catch((e) => toast(e.message)));
$('leave-btn').addEventListener('click', () => $('confirm-dialog').showModal());
$('confirm-dialog').addEventListener('close', async () => {
  const d = $('confirm-dialog');
  const v = d.returnValue;
  d.returnValue = '';
  if (v !== 'leave') return;
  await api('leave', {});
  $('settings-dialog').close();
  document.querySelector('[data-step=choose]').hidden = false;
  $('setup-form').hidden = true;
  refresh();
});

// close-on-backdrop where `closedby` isn't supported
if (!('closedBy' in HTMLDialogElement.prototype)) {
  const outside = (e, d) => { const r = d.getBoundingClientRect(); return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom; };
  for (const d of document.querySelectorAll('dialog[closedby="any"]')) {
    let down = false;
    d.addEventListener('pointerdown', (e) => { down = outside(e, d); });
    d.addEventListener('pointerup', (e) => { if (down && outside(e, d)) d.close(); });
  }
}

paintIcons();
refresh();
