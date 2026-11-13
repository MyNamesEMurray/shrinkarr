'use strict';

// ---------- helpers ----------
// Let append/replaceChildren take arrays and skip null/false, like h() does.
for (const m of ['append', 'replaceChildren']) {
  const orig = Element.prototype[m];
  Element.prototype[m] = function (...kids) { return orig.apply(this, kids.flat(Infinity).filter(c => c != null && c !== false)); };
}
const $ = (s, el = document) => el.querySelector(s);

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'style') el.style.cssText = v;
    else if (k === 'value') el.value = v;
    else if (k === 'checked') el.checked = !!v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of kids.flat(Infinity)) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : String(c));
  }
  return el;
}

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

async function call(method, path, body) {
  const r = await fetch('/api' + path, {
    method,
    headers: method === 'GET' ? {} : { 'Content-Type': 'application/json' },
    body: method === 'GET' ? undefined : JSON.stringify(body || {}),
  });
  if (r.status === 401 && path !== '/login') { showLogin(); throw new Error('Please log in'); }
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}
const get = p => call('GET', p);
const post = (p, b) => call('POST', p, b || {});

let toastTimer;
function toast(msg, err) {
  const t = $('#toast');
  t.textContent = msg;
  t.className = 'show' + (err ? ' err' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.className = ''), err ? 6000 : 2500);
}
// Wraps an async click handler: disables the button and shows errors.
function act(fn) {
  return async e => {
    const b = e && e.currentTarget;
    if (b && b.tagName === 'BUTTON') b.disabled = true;
    try { await fn(e); } catch (err) { toast(err.message, true); } finally { if (b && b.tagName === 'BUTTON') b.disabled = false; }
  };
}

function bytes(n) {
  const neg = n < 0; n = Math.abs(n || 0);
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return (neg ? '−' : '') + (i === 0 ? n : n.toFixed(n < 10 ? 2 : 1)) + ' ' + u[i];
}
const base = p => String(p).split('/').pop();
const dirOf = p => String(p).split('/').slice(0, -1).join('/') || '/';
function when(t) {
  const d = new Date(t);
  if (isNaN(d) || d.getFullYear() < 2000) return '';
  const s = (Date.now() - d) / 1000;
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + ' min ago';
  if (s < 86400) return Math.floor(s / 3600) + ' h ago';
  return d.toLocaleDateString();
}
const pill = s => h('span', { class: 'pill ' + s }, s);

function openModal(content, opts = {}) {
  const box = h('div', { class: 'modal-box' + (opts.big ? ' big' : ''), role: 'dialog', 'aria-modal': 'true' }, content);
  const wrap = h('div', { class: 'modal' }, box);
  const close = () => { wrap.remove(); document.removeEventListener('keydown', onKey); opts.onClose && opts.onClose(); };
  const onKey = e => { if (e.key === 'Escape' && !opts.locked) close(); };
  if (!opts.locked) wrap.addEventListener('click', e => { if (e.target === wrap) close(); });
  document.addEventListener('keydown', onKey);
  document.body.append(wrap);
  const f = box.querySelector('input, select, button');
  f && f.focus();
  return close;
}

// ---------- shared data ----------
let nodeDefs = null;
async function defs() {
  if (!nodeDefs) {
    nodeDefs = {};
    for (const d of await get('/nodes')) nodeDefs[d.type] = d;
  }
  return nodeDefs;
}
const catLabel = { check: 'Checks', video: 'Video', audio: 'Audio', subtitles: 'Subtitles', run: 'Run', finish: 'Finish' };

// ---------- login ----------
let loginOpen = false;
function showLogin() {
  if (loginOpen) return;
  loginOpen = true;
  const pw = h('input', { type: 'password', autocomplete: 'current-password', 'aria-label': 'Password' });
  let close;
  const go = act(async () => {
    await post('/login', { password: pw.value });
    location.reload();
  });
  close = openModal(h('form', { onsubmit: e => { e.preventDefault(); go(); } },
    h('h1', {}, 'Shrinkarr'),
    h('label', { class: 'f' }, h('span', {}, 'Password'), pw),
    h('button', { class: 'btn primary', type: 'submit' }, 'Log in')), { locked: true });
}

// ---------- folder / file browser ----------
function browser(mode, start, onPick) {
  let cur = start || '/';
  let selected = null;
  const list = h('div', { class: 'browser' });
  const crumb = h('div', { class: 'mono', style: 'margin-bottom:8px;word-break:break-all' });
  const useBtn = h('button', { class: 'btn primary', type: 'button', onclick: () => onPick(mode === 'dir' ? cur : selected), disabled: mode === 'file' },
    mode === 'dir' ? 'Use this folder' : 'Use this file');
  async function load(p) {
    try {
      const r = await get('/browse?path=' + encodeURIComponent(p));
      cur = r.path; selected = null;
      if (mode === 'file') useBtn.disabled = true;
      crumb.textContent = cur;
      list.replaceChildren(
        cur !== '/' ? h('div', { onclick: () => load(dirOf(cur)) }, '⬆︎', h('span', {}, 'Up one folder')) : null,
        ...(r.entries || []).filter(e => e.dir || mode === 'file').map(e => {
          const row = h('div', {
            onclick: () => {
              if (e.dir) return load((cur === '/' ? '' : cur) + '/' + e.name);
              selected = (cur === '/' ? '' : cur) + '/' + e.name;
              list.querySelectorAll('.sel').forEach(x => x.classList.remove('sel'));
              row.classList.add('sel');
              useBtn.disabled = false;
            },
          }, e.dir ? '📁' : '🎞️', h('span', { style: 'flex:1;word-break:break-all' }, e.name), e.dir ? null : h('span', { class: 'muted small' }, bytes(e.size)));
          return row;
        }),
        (r.entries || []).length === 0 ? h('div', { class: 'muted' }, 'Empty folder') : null);
    } catch (e) { toast(e.message, true); if (p !== '/') load('/'); }
  }
  load(cur);
  return h('div', {}, crumb, list, h('div', { class: 'row', style: 'margin-top:12px' }, h('span', { class: 'spacer' }), useBtn));
}

function pickPath(mode, start) {
  return new Promise(resolve => {
    let done = false;
    const close = openModal(h('div', {},
      h('h2', { style: 'margin-top:0' }, mode === 'dir' ? 'Choose a folder' : 'Choose a video file'),
      browser(mode, start, p => { done = true; close(); resolve(p); })), { onClose: () => { if (!done) resolve(null); } });
  });
}

// ---------- banners / status ----------
let lastStatus = null;
async function refreshBanners() {
  try { lastStatus = await get('/status'); } catch { return; }
  const s = lastStatus;
  const b = [];
  if (s.testMode) b.push(h('div', { class: 'banner warn' }, h('span', {}, h('b', {}, 'Test mode is on. '), 'Shrinkarr shows what it would do, but changes no files.'),
    h('button', { class: 'btn sm', onclick: act(async () => { await setSetting({ testMode: false }); toast('Test mode off. Files will now be converted.'); }) }, 'Turn off test mode')));
  if (s.paused) b.push(h('div', { class: 'banner info' }, h('span', {}, h('b', {}, 'Paused. '), 'No new files start.'),
    h('button', { class: 'btn sm', onclick: act(async () => { await post('/pause', { paused: false }); refreshBanners(); }) }, 'Resume')));
  else if (!s.inSchedule) b.push(h('div', { class: 'banner info' }, `Waiting for active hours (${s.activeFrom}–${s.activeTo}). Change this in Settings.`));
  $('#banners').replaceChildren(...b);
}

async function setSetting(patch) {
  const r = await get('/settings');
  await post('/settings', { ...r.settings, ...patch });
  refreshBanners();
  if (location.hash.startsWith('#/settings')) route(); // reload the form so it isn't stale
}

// ---------- record details ----------
async function showRecord(id) {
  const r = await get('/history/' + id);
  const saved = r.origSize - r.newSize;
  openModal(h('div', {},
    h('h2', { style: 'margin-top:0;word-break:break-word' }, base(r.final || r.path)),
    h('div', { class: 'row' }, pill(r.status), h('span', {}, r.detail)),
    h('p', { class: 'path mono' }, r.path),
    r.status === 'done' ? h('div', { class: 'grid tiles', style: 'margin:12px 0' },
      tile('Before', bytes(r.origSize), r.origCodec), tile('After', bytes(r.newSize), r.newCodec),
      tile('Saved', bytes(saved), r.origSize ? Math.round(100 * saved / r.origSize) + '% smaller' : '')) : null,
    h('p', { class: 'small muted' }, `Flow: ${r.flow} · took ${duration((new Date(r.finished) - new Date(r.started)) / 1000)}`),
    h('h3', {}, 'Path through the flow'),
    h('ol', { class: 'small' }, (r.steps || []).map(s => h('li', {}, s))),
    r.commands && r.commands.length ? [h('h3', {}, 'FFmpeg commands'), h('pre', {}, r.commands.join('\n\n'))] : null,
    r.log ? [h('h3', {}, 'Log'), h('pre', {}, r.log)] : null,
    h('div', { class: 'row', style: 'margin-top:12px' },
      r.status !== 'done' ? h('button', { class: 'btn', onclick: act(async () => { await post('/files/action', { paths: [r.path], action: 'now' }); toast('Queued first'); }) }, 'Run again') : null)), { big: true });
}

function duration(s) {
  if (!isFinite(s) || s < 0) return '?';
  if (s < 60) return Math.round(s) + ' s';
  if (s < 3600) return Math.round(s / 60) + ' min';
  return (s / 3600).toFixed(1) + ' h';
}
const tile = (k, v, s) => h('div', { class: 'card tile' }, h('div', { class: 'k' }, k), h('div', { class: 'v' }, v), s ? h('div', { class: 's' }, s) : null);

// ---------- pages ----------
let timers = [];
let leaveGuard = null;
function every(ms, fn) { fn(); timers.push(setInterval(fn, ms)); }

const pages = {
  '': pageDashboard, flows: pageFlows, libraries: pageLibraries, stats: pageStats,
  history: pageHistory, settings: pageSettings,
};

let lastHash = location.hash;
async function route() {
  if (leaveGuard && !leaveGuard()) { history.replaceState(null, '', lastHash); return; }
  leaveGuard = null;
  lastHash = location.hash;
  timers.forEach(clearInterval); timers = [];
  const [name, arg] = location.hash.replace(/^#\/?/, '').split('/');
  document.querySelectorAll('#nav a').forEach(a => a.classList.toggle('on', a.getAttribute('href').replace(/^#\/?/, '') === name));
  const app = $('#app');
  app.className = '';
  app.replaceChildren();
  try {
    if (name === 'flows' && arg) await pageEditor(app, decodeURIComponent(arg));
    else await (pages[name] || pageDashboard)(app);
  } catch (e) { app.append(h('p', { class: 'muted' }, e.message)); }
}

// ---------- dashboard ----------
async function pageDashboard(app) {
  let tab = 'queued', query = '', lastKey = '';
  const tiles = h('div', { class: 'grid tiles' });
  const runBox = h('div', { class: 'grid', style: 'margin-top:14px' });
  const tabsEl = h('div', { class: 'tabs', role: 'tablist' });
  const tools = h('div', { class: 'row', style: 'margin-bottom:10px' });
  const listBox = h('div', { class: 'table-wrap' });
  const pauseBtn = h('button', { class: 'btn' });
  app.append(
    h('div', { class: 'row' }, h('h1', { style: 'margin:0' }, 'Dashboard'), h('span', { class: 'spacer' }), pauseBtn,
      h('button', { class: 'btn', onclick: act(async () => { await post('/scan', {}); toast('Scanning libraries…'); }) }, 'Scan now')),
    h('div', { style: 'height:16px' }), tiles, runBox, tabsEl, tools, listBox);

  const tabNames = [['queued', 'Queue'], ['failed', 'Failed'], ['done', 'Done'], ['skipped', 'Skipped'], ['ignored', 'Ignored']];
  let stats = null;

  async function refresh() {
    const s = lastStatus = await get('/status');
    const c = s.counts || {};
    const key = JSON.stringify([c, s.queue]);
    if (key !== lastKey || !stats) stats = await get('/stats?days=1');
    pauseBtn.textContent = s.paused ? 'Resume' : 'Pause';
    pauseBtn.onclick = act(async () => { await post('/pause', { paused: !s.paused }); refreshBanners(); refresh(); });
    tiles.replaceChildren(
      tile('Space saved', bytes(stats.savedBytes), stats.origBytes ? Math.round(100 * stats.savedBytes / stats.origBytes) + '% smaller overall' : 'nothing yet'),
      tile('Files shrunk', stats.files, ''),
      tile('In queue', s.queue, (s.scanning || []).length ? 'scanning…' : ''),
      tile('Failed', c.failed || 0, c.failed ? 'see the Failed tab' : ''));
    runBox.replaceChildren(...(s.running || []).map(j => h('div', { class: 'card job' },
      h('div', { class: 'row' }, h('div', { style: 'flex:1;min-width:0' }, h('div', { class: 'name', style: 'word-break:break-word;font-weight:600' }, base(j.path)), h('div', { class: 'path small muted' }, j.flow)),
        h('button', { class: 'btn sm danger', onclick: act(async () => { await post(`/jobs/${j.id}/cancel`); toast('Cancelling…'); }) }, 'Cancel')),
      h('div', { class: 'small' }, j.progress.phase || 'Starting…', j.progress.percent ? ` · ${j.progress.percent.toFixed(1)}%` : '',
        j.progress.fps && j.progress.fps !== '0.00' ? ` · ${j.progress.fps} fps` : '', j.progress.speed && j.progress.speed !== 'N/A' ? ` · ${j.progress.speed}` : ''),
      h('div', { class: 'bar', role: 'progressbar', 'aria-valuenow': Math.round(j.progress.percent || 0), 'aria-valuemin': 0, 'aria-valuemax': 100 }, h('div', { style: `width:${j.progress.percent || 0}%` })))));
    tabsEl.replaceChildren(...tabNames.map(([k, label]) => h('button', {
      role: 'tab', class: tab === k ? 'on' : '', 'aria-selected': tab === k,
      onclick: () => { tab = k; lastKey = ''; refresh(); },
    }, label, h('span', { class: 'count' }, k === 'queued' ? s.queue : (c[k] || 0)))));
    if (key !== lastKey) { lastKey = key; loadList(); }
  }

  async function loadList() {
    const r = await get(`/files?status=${tab}&limit=300&q=${encodeURIComponent(query)}`);
    const bulk = {
      queued: [['Clear queue', 'dequeue']], failed: [['Retry all', 'retry'], ['Ignore all', 'ignore']], skipped: [['Retry all', 'retry']],
    }[tab] || [];
    tools.replaceChildren(
      h('input', { type: 'search', placeholder: 'Search files…', value: query, style: 'max-width:280px', 'aria-label': 'Search files',
        oninput: e => { query = e.target.value; clearTimeout(tools._t); tools._t = setTimeout(loadList, 250); } }),
      h('span', { class: 'spacer' }),
      ...bulk.map(([label, action]) => h('button', {
        class: 'btn sm', onclick: act(async () => {
          if (!confirm(`${label}: ${r.total} files?`)) return;
          await post('/files/action', { status: tab, action }); lastKey = ''; refresh();
        }),
      }, label)));
    if (!r.files || !r.files.length) {
      listBox.replaceChildren(h('p', { class: 'muted' }, {
        queued: 'Nothing waiting. New files are picked up on each scan.', failed: 'No failures.', done: 'Nothing converted yet.',
        skipped: 'Files that needed no work show here.', ignored: 'Files you ignore are never processed.',
      }[tab]));
      return;
    }
    const actions = f => ({
      queued: [['Run next', 'now'], ['Ignore', 'ignore']], failed: [['Retry', 'retry'], ['Ignore', 'ignore']],
      skipped: [['Retry', 'retry'], ['Ignore', 'ignore']], done: [['Redo', 'retry']], ignored: [['Un-ignore', 'unignore']],
    }[f.status] || []);
    listBox.replaceChildren(h('table', {},
      h('thead', {}, h('tr', {}, h('th', {}, 'File'), h('th', { class: 'num' }, 'Size'), h('th', { class: 'num hide-sm' }, 'Updated'), h('th', {}, ''))),
      h('tbody', {}, r.files.map(f => h('tr', { class: f.jobId ? 'click' : '', onclick: f.jobId ? act(() => showRecord(f.jobId)) : null },
        h('td', {}, h('div', { class: 'name' }, base(f.path)), h('div', { class: 'path' }, dirOf(f.path)), f.detail ? h('div', { class: 'small', style: 'margin-top:2px' }, f.detail) : null),
        h('td', { class: 'num' }, bytes(f.size)),
        h('td', { class: 'num hide-sm small muted' }, when(f.updated)),
        h('td', { class: 'num' }, actions(f).map(([label, action]) => h('button', {
          class: 'btn sm', style: 'margin-left:4px',
          onclick: act(async e => { e.stopPropagation(); await post('/files/action', { paths: [f.path], action }); lastKey = ''; refresh(); }),
        }, label))))))),
      r.total > r.files.length ? h('p', { class: 'muted small' }, `Showing ${r.files.length} of ${r.total}. Use search to narrow down.`) : null);
  }

  every(2000, () => refresh().catch(() => {}));
}

// ---------- flows list ----------
let pendingDraft = null;
async function pageFlows(app) {
  const [flows, st] = await Promise.all([get('/flows'), get('/settings')]);
  const used = id => st.settings.libraries.filter(l => l.flowId === id).map(l => l.name);
  const card = f => h('div', { class: 'card' },
    h('div', { class: 'row' }, h('h3', { style: 'margin:0;flex:1' }, f.name), f.builtIn ? h('span', { class: 'pill tpl' }, 'Template') : null),
    h('p', { class: 'small', style: 'color:var(--ink-2)' }, f.description || 'No description.'),
    used(f.id).length ? h('p', { class: 'small muted' }, 'Used by: ' + used(f.id).join(', ')) : null,
    h('div', { class: 'row' },
      h('a', { class: 'btn sm', href: '#/flows/' + encodeURIComponent(f.id) }, f.builtIn ? 'View' : 'Edit'),
      h('button', { class: 'btn sm', onclick: () => { pendingDraft = { ...structuredClone(f), id: '', builtIn: false, name: f.name + ' (my copy)' }; location.hash = '#/flows/new'; } }, 'Duplicate'),
      f.builtIn ? null : h('button', {
        class: 'btn sm danger', onclick: act(async () => {
          if (!confirm(`Delete "${f.name}"?`)) return;
          await call('DELETE', '/flows/' + encodeURIComponent(f.id)); route();
        }),
      }, 'Delete')));
  app.append(
    h('div', { class: 'row' }, h('h1', { style: 'margin:0' }, 'Flows'), h('span', { class: 'spacer' }),
      h('button', { class: 'btn primary', onclick: () => { pendingDraft = { id: '', name: 'My flow', description: '', nodes: [{ id: '1', type: 'start' }], edges: [] }; location.hash = '#/flows/new'; } }, 'New blank flow')),
    h('p', { class: 'muted' }, 'A flow decides what happens to each file. Templates are ready to use; duplicate one to change it.'),
    h('h2', {}, 'Templates'), h('div', { class: 'grid cards' }, flows.filter(f => f.builtIn).map(card)),
    h('h2', {}, 'Your flows'),
    flows.some(f => !f.builtIn) ? h('div', { class: 'grid cards' }, flows.filter(f => !f.builtIn).map(card)) : h('p', { class: 'muted' }, 'None yet.'));
}

// ---------- flow editor ----------
function summary(def, params) {
  return (def.params || []).map(p => {
    const v = params[p.key] ?? p.default;
    if (p.key === 'vmaf' && params.mode === 'fixed') return null;
    if (p.key === 'quality' && (params.mode ?? 'auto') === 'auto') return null;
    if (p.key === 'folder' && (params.original ?? 'delete') !== 'move') return null;
    if (p.key === 'hardware' && v === 'settings') return null;
    if (v === '' || v == null) return p.kind === 'list' ? p.label + ': (none)' : null;
    return p.kind === 'number' && p.key === 'vmaf' ? 'VMAF ' + v : String(v);
  }).filter(Boolean).join(' · ');
}
function nodeHTML(def, params) {
  return `<div class="nd-t">${esc(def.label)}</div><div class="nd-s">${esc(summary(def, params))}</div>`;
}

async function pageEditor(app, id) {
  const D = await defs();
  let flow;
  if (id === 'new') {
    if (!pendingDraft) { location.hash = '#/flows'; return; }
    flow = pendingDraft; pendingDraft = null;
  } else flow = await get('/flows/' + encodeURIComponent(id));
  const ro = !!flow.builtIn;
  let dirty = id === 'new';
  leaveGuard = () => !dirty || confirm('Leave without saving your changes?');
  app.className = 'wide';

  const dfEl = h('div', { id: 'drawflow' });
  const props = h('div', { class: 'props' });
  const nameIn = h('input', { value: flow.name, 'aria-label': 'Flow name', disabled: ro, oninput: () => { dirty = true; } });
  const palette = h('div', { class: 'palette' }, h('div', { class: 'small muted' }, 'Tap a block to add it, or drag it onto the canvas.'));
  const wrap = h('div', { class: 'canvas-wrap' },
    h('div', { class: 'ed-bar' },
      h('a', { class: 'btn sm', href: '#/flows' }, '← Flows'), nameIn,
      ro ? h('span', { class: 'pill tpl' }, 'Template · read only') : null,
      h('span', { class: 'spacer' }),
      h('button', { class: 'btn sm', onclick: act(testFlow) }, 'Test on a file'),
      ro ? h('button', { class: 'btn sm primary', onclick: () => { pendingDraft = { ...structuredClone(flow), id: '', builtIn: false, name: flow.name + ' (my copy)' }; dirty = false; location.hash = '#/flows/new'; } }, 'Duplicate to edit')
        : h('button', { class: 'btn sm primary', onclick: act(save) }, 'Save')),
    dfEl,
    h('div', { class: 'zoom' },
      h('button', { class: 'btn sm', onclick: () => fit() }, 'Fit'),
      h('button', { class: 'btn sm', 'aria-label': 'Zoom out', onclick: () => ed.zoom_out() }, '−'),
      h('button', { class: 'btn sm', 'aria-label': 'Reset zoom', onclick: () => ed.zoom_reset() }, '100%'),
      h('button', { class: 'btn sm', 'aria-label': 'Zoom in', onclick: () => ed.zoom_in() }, '+')));
  app.append(h('div', { class: 'editor' + (ro ? ' ro' : '') }, palette, wrap, props));

  const ed = new Drawflow(dfEl);
  ed.reroute = false;
  ed.editor_mode = ro ? 'view' : 'edit';
  ed.start();

  function addNode(type, x, y, params) {
    const def = D[type];
    const nid = ed.addNode(type, type === 'start' ? 0 : 1, def.outputs.length, x, y, 'cat-' + def.category,
      { type, params: params || {} }, nodeHTML(def, params || {}));
    const el = document.getElementById('node-' + nid);
    el.style.setProperty('--c', `var(--c-${def.category})`);
    el.querySelectorAll('.output').forEach((o, i) => { o.dataset.label = def.outputs[i]; o.title = def.outputs[i]; });
    return nid;
  }

  // Lay out nodes that have no saved position: columns by distance from Start.
  const hasPos = flow.nodes.every(n => n.x || n.y);
  const pos = {};
  if (!hasPos) {
    const depth = {}, start = flow.nodes.find(n => n.type === 'start');
    const q = [[start.id, 0]];
    while (q.length) {
      const [nid, d] = q.shift();
      if (depth[nid] !== undefined && depth[nid] >= d) continue;
      if (d > flow.nodes.length) break;
      depth[nid] = d;
      flow.edges.filter(e => e.from === nid).sort((a, b) => a.out - b.out).forEach(e => q.push([e.to, d + 1]));
    }
    const rows = {};
    flow.nodes.forEach(n => {
      const d = depth[n.id] ?? 0;
      rows[d] = (rows[d] || 0) + 1;
      pos[n.id] = [40 + d * 250, 70 + (rows[d] - 1) * 120];
    });
  }
  const map = {};
  flow.nodes.forEach(n => { map[n.id] = addNode(n.type, hasPos ? n.x : pos[n.id][0], hasPos ? n.y : pos[n.id][1], n.params); });
  flow.edges.forEach(e => { if (map[e.from] && map[e.to]) ed.addConnection(map[e.from], map[e.to], 'output_' + e.out, 'input_1'); });

  // Zoom and pan so every block is on screen. Drawflow scales around the canvas centre.
  function fit() {
    const ns = Object.values(ed.export().drawflow.Home.data);
    if (!ns.length) return;
    const minX = Math.min(...ns.map(n => n.pos_x)), maxX = Math.max(...ns.map(n => n.pos_x)) + 250;
    const minY = Math.min(...ns.map(n => n.pos_y)), maxY = Math.max(...ns.map(n => n.pos_y)) + 90;
    const W = wrap.clientWidth, H = wrap.clientHeight, top = wrap.querySelector('.ed-bar').offsetHeight + 24;
    const z = Math.max(0.5, Math.min(1, (W - 40) / (maxX - minX), (H - top - 30) / (maxY - minY)));
    const pc = ed.precanvas, cx = pc.clientWidth / 2, cy = pc.clientHeight / 2;
    ed.zoom = z;
    ed.canvas_x = 20 - (cx + (minX - cx) * z);
    ed.canvas_y = top - (cy + (minY - cy) * z);
    pc.style.transform = `translate(${ed.canvas_x}px, ${ed.canvas_y}px) scale(${z})`;
  }
  requestAnimationFrame(fit);

  ed.on('connectionCreated', c => {
    if (c.output_id === c.input_id) { ed.removeSingleConnection(c.output_id, c.input_id, c.output_class, c.input_class); return; }
    // Each output leads to one block: drop the older connection.
    const conns = ed.getNodeFromId(c.output_id).outputs[c.output_class].connections;
    conns.filter(x => x.node !== String(c.input_id)).forEach(x => ed.removeSingleConnection(c.output_id, x.node, c.output_class, x.output));
    dirty = true;
  });
  dirty = id === 'new';
  ['nodeMoved', 'connectionRemoved'].forEach(ev => ed.on(ev, () => { dirty = true; }));
  ed.on('nodeRemoved', () => { dirty = true; showProps(null); });

  function exportFlow() {
    const data = ed.export().drawflow.Home.data;
    const nodes = [], edges = [];
    for (const [nid, n] of Object.entries(data)) {
      nodes.push({ id: nid, type: n.data.type, x: Math.round(n.pos_x), y: Math.round(n.pos_y), params: n.data.params });
      for (const [oc, o] of Object.entries(n.outputs)) for (const c of o.connections) edges.push({ from: nid, out: +oc.split('_')[1], to: c.node });
    }
    return { id: flow.id, name: nameIn.value.trim(), description: flow.description || '', nodes, edges };
  }

  async function save() {
    const f = exportFlow();
    const saved = await post('/flows', f);
    flow.id = saved.id;
    dirty = false;
    toast('Saved');
    if (id === 'new') { leaveGuard = null; history.replaceState(null, '', '#/flows/' + encodeURIComponent(saved.id)); lastHash = location.hash; }
  }

  async function testFlow() {
    const st = await get('/settings');
    const lib = st.settings.libraries[0];
    const path = await pickPath('file', lib ? lib.path : '/');
    if (!path) return;
    toast('Testing… nothing will be changed');
    const r = await post('/flows/test', { flow: exportFlow(), path });
    dfEl.querySelectorAll('.drawflow-node.visited').forEach(n => n.classList.remove('visited'));
    (r.nodes || []).forEach(nid => { const n = document.getElementById('node-' + nid); n && n.classList.add('visited'); });
    openModal(h('div', {},
      h('h2', { style: 'margin-top:0;word-break:break-word' }, 'Test: ' + base(path)),
      h('div', { class: 'row' }, pill(r.status), h('span', {}, r.detail)),
      h('p', { class: 'small muted' }, 'No files were changed. Blocks this file passed through are outlined in green.'),
      h('h3', {}, 'Path through the flow'), h('ol', { class: 'small' }, (r.steps || []).map(s => h('li', {}, s))),
      r.commands && r.commands.length ? [h('h3', {}, 'FFmpeg would run'), h('pre', {}, r.commands.join('\n\n'))] : null,
      r.log ? [h('h3', {}, 'Log'), h('pre', {}, r.log)] : null), { big: true });
  }

  function showProps(nid) {
    if (!nid) {
      props.replaceChildren(h('h3', {}, 'How flows work'),
        h('p', { class: 'small' }, 'Every file starts at Start and follows the arrows. Checks send it down one of their outputs. Video, audio and subtitle blocks only collect settings. Run FFmpeg does the work into a temporary file, and Replace original swaps it in.'),
        h('p', { class: 'small' }, 'Connect blocks by dragging from an output dot (right side) to an input dot (left side). Click a block to change its settings.'),
        h('p', { class: 'small muted' }, 'Tip: use "Test on a file" to see the path a file would take, without changing anything.'));
      return;
    }
    const node = ed.getNodeFromId(nid);
    const def = D[node.data.type];
    const params = node.data.params || {};
    const update = () => {
      ed.updateNodeDataFromId(nid, { type: def.type, params });
      const c = document.querySelector(`#node-${nid} .drawflow_content_node`);
      if (c) c.innerHTML = nodeHTML(def, params);
      dirty = true;
    };
    const field = p => {
      const v = params[p.key] ?? p.default;
      const set = val => { params[p.key] = val; update(); };
      let input;
      if (p.kind === 'select') {
        input = h('select', { disabled: ro, onchange: e => set(e.target.value) }, p.options.map(o => h('option', { value: o, selected: String(v) === o }, o)));
      } else if (p.kind === 'number') {
        input = h('input', { type: 'number', step: 'any', value: v, disabled: ro, oninput: e => set(e.target.value === '' ? '' : Number(e.target.value)) });
      } else {
        input = h('input', { value: v, disabled: ro, placeholder: p.kind === 'list' ? 'comma separated' : '', oninput: e => set(e.target.value) });
      }
      return h('label', { class: 'f' }, h('span', {}, p.label), input, p.help ? h('span', { class: 'help' }, p.help) : null);
    };
    props.replaceChildren(
      h('div', { class: 'small muted' }, catLabel[def.category] || 'Start'),
      h('h3', {}, def.label),
      def.help ? h('p', { class: 'small', style: 'color:var(--ink-2)' }, def.help) : null,
      def.outputs.length > 1 ? h('p', { class: 'small muted' }, 'Outputs, top to bottom: ' + def.outputs.join(', ')) : null,
      ...(def.params || []).map(field),
      !ro && def.type !== 'start' ? h('button', { class: 'btn sm danger', onclick: () => { ed.removeNodeId('node-' + nid); } }, 'Delete block') : null);
  }
  dfEl.addEventListener('click', e => {
    const n = e.target.closest('.drawflow-node');
    if (n && !e.target.closest('.output, .input')) showProps(n.id.replace('node-', ''));
  });
  showProps(null);

  // palette
  if (!ro) {
    const cats = {};
    Object.values(D).filter(d => d.type !== 'start').forEach(d => (cats[d.category] ||= []).push(d));
    for (const [cat, list] of Object.entries(cats)) {
      palette.append(h('h4', {}, catLabel[cat] || cat));
      list.forEach(d => palette.append(h('button', {
        class: 'pal-item', style: `--c:var(--c-${cat})`, draggable: 'true', title: d.help || '',
        ondragstart: e => e.dataTransfer.setData('text/plain', d.type),
        onclick: () => {
          const x = (wrap.clientWidth / 2 - ed.canvas_x) / ed.zoom - 95 + Math.random() * 40;
          const y = (wrap.clientHeight / 2 - ed.canvas_y) / ed.zoom - 30 + Math.random() * 40;
          const nid = addNode(d.type, x, y, {});
          dirty = true;
          showProps(String(nid));
        },
      }, d.label)));
    }
    dfEl.addEventListener('dragover', e => e.preventDefault());
    dfEl.addEventListener('drop', e => {
      e.preventDefault();
      const type = e.dataTransfer.getData('text/plain');
      if (!D[type]) return;
      const pc = ed.precanvas, r = pc.getBoundingClientRect();
      const k = pc.clientWidth / (pc.clientWidth * ed.zoom);
      const nid = addNode(type, e.clientX * k - r.x * k - 95, e.clientY * k - r.y * k - 20, {});
      dirty = true;
      showProps(String(nid));
    });
  }
}

// ---------- libraries ----------
async function pageLibraries(app) {
  const [st, flows] = await Promise.all([get('/settings'), get('/flows')]);
  const s = st.settings;
  const flowName = fid => (flows.find(f => f.id === fid) || {}).name || '(missing flow)';
  const saveAll = async () => { await post('/settings', s); route(); };

  function edit(lib, isNew) {
    const l = { ...lib };
    const pathIn = h('input', { value: l.path, placeholder: '/media/movies', oninput: e => { l.path = e.target.value; } });
    let close;
    close = openModal(h('form', { onsubmit: act(async e => {
        e.preventDefault();
        l.scanMinutes = +l.scanMinutes || 30;
        if (!l.name) l.name = base(l.path) || 'Library';
        if (isNew) s.libraries.push(l); else s.libraries[s.libraries.indexOf(lib)] = l;
        try { await post('/settings', s); } catch (err) { if (isNew) s.libraries.pop(); else s.libraries[s.libraries.indexOf(l)] = lib; throw err; }
        close(); route();
        if (isNew) { await post('/scan', { library: '' }); toast('Library added. Scanning…'); }
      }) },
      h('h2', { style: 'margin-top:0' }, isNew ? 'Add a library' : 'Edit library'),
      h('label', { class: 'f' }, h('span', {}, 'Name'), h('input', { value: l.name, placeholder: 'Movies', oninput: e => { l.name = e.target.value; } })),
      h('label', { class: 'f' }, h('span', {}, 'Folder'), h('div', { class: 'row', style: 'flex-wrap:nowrap' }, pathIn,
        h('button', { class: 'btn', type: 'button', onclick: async () => { const p = await pickPath('dir', l.path || '/'); if (p) { l.path = p; pathIn.value = p; } } }, 'Browse')),
        h('span', { class: 'help' }, 'The folder as Shrinkarr sees it inside its container.')),
      h('label', { class: 'f' }, h('span', {}, 'Flow'), h('select', { onchange: e => { l.flowId = e.target.value; } },
        flows.map(f => h('option', { value: f.id, selected: f.id === l.flowId }, f.name + (f.builtIn ? '' : ' (yours)'))))),
      h('div', { class: 'form-grid' },
        h('label', { class: 'f' }, h('span', {}, 'Look for new files every (minutes)'), h('input', { type: 'number', min: 1, value: l.scanMinutes || 30, oninput: e => { l.scanMinutes = e.target.value; } }))),
      h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: l.enabled, onchange: e => { l.enabled = e.target.checked; } }), 'Enabled'),
      h('div', { class: 'row' }, h('span', { class: 'spacer' }), h('button', { class: 'btn', type: 'button', onclick: () => close() }, 'Cancel'), h('button', { class: 'btn primary', type: 'submit' }, 'Save'))));
  }

  app.append(
    h('div', { class: 'row' }, h('h1', { style: 'margin:0' }, 'Libraries'), h('span', { class: 'spacer' }),
      h('button', { class: 'btn primary', onclick: () => edit({ name: '', path: '', flowId: flows[0].id, enabled: true, scanMinutes: 30 }, true) }, 'Add library')),
    h('p', { class: 'muted' }, 'A library is a folder of videos and the flow used on it.'),
    s.libraries.length ? h('div', { class: 'grid cards' }, s.libraries.map(l => h('div', { class: 'card' },
      h('div', { class: 'row' }, h('h3', { style: 'margin:0;flex:1' }, l.name), l.enabled ? null : h('span', { class: 'pill' }, 'disabled')),
      h('p', { class: 'mono', style: 'word-break:break-all' }, l.path),
      h('p', { class: 'small' }, 'Flow: ', h('a', { href: '#/flows/' + encodeURIComponent(l.flowId) }, flowName(l.flowId))),
      h('p', { class: 'small muted' }, `Scans every ${l.scanMinutes} min`),
      h('div', { class: 'row' },
        h('button', { class: 'btn sm', onclick: act(async () => { await post('/scan', { library: l.id }); toast('Scanning ' + l.name); }) }, 'Scan now'),
        h('button', { class: 'btn sm', onclick: () => edit(l, false) }, 'Edit'),
        h('button', { class: 'btn sm danger', onclick: act(async () => { if (!confirm(`Remove library "${l.name}"? Files are not touched.`)) return; s.libraries = s.libraries.filter(x => x !== l); await saveAll(); }) }, 'Remove')))))
      : h('div', { class: 'card' }, h('p', {}, 'No libraries yet.'), h('button', { class: 'btn primary', onclick: () => edit({ name: '', path: '', flowId: flows[0].id, enabled: true, scanMinutes: 30 }, true) }, 'Add your first library')));
}

// ---------- stats ----------
function barChart(days) {
  const W = 720, H = 220, L = 52, B = 26, T = 10;
  const max = Math.max(1, ...days.map(d => d.saved));
  const step = niceStep(max / 4);
  const top = Math.ceil(max / step) * step;
  const bw = (W - L) / days.length;
  const y = v => T + (H - T - B) * (1 - v / top);
  const ns = 'http://www.w3.org/2000/svg';
  const s = (tag, attrs, ...kids) => { const el = document.createElementNS(ns, tag); for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v); kids.forEach(k => el.append(k)); return el; };
  const tip = h('div', { class: 'tip' });
  const svg = s('svg', { viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': 'Space saved per day' });
  for (let v = 0; v <= top + 1; v += step) {
    svg.append(s('line', { class: 'grid-line', x1: L, x2: W, y1: y(v), y2: y(v) }));
    svg.append(s('text', { class: 'axis', x: L - 6, y: y(v) + 4, 'text-anchor': 'end' }, bytes(v)));
  }
  const every = Math.ceil(days.length / 8);
  days.forEach((d, i) => {
    const x = L + i * bw;
    const g = s('g', {});
    const w = Math.max(1, bw - 2), bh = Math.max(0, y(0) - y(d.saved)), r = Math.min(4, w / 2, bh);
    if (d.saved > 0) {
      g.append(s('path', { class: 'b', fill: 'var(--bar)', d: `M${x + 1},${y(0)} v${-(bh - r)} q0,${-r} ${r},${-r} h${w - 2 * r} q${r},0 ${r},${r} v${bh - r} z` }));
    }
    const hit = s('rect', { class: 'hit', x, y: T, width: bw, height: H - T - B });
    hit.addEventListener('mouseenter', () => { tip.textContent = `${d.day}: ${bytes(d.saved)} saved, ${d.files} file${d.files === 1 ? '' : 's'}`; tip.style.display = 'block'; tip.style.left = (100 * (x + bw / 2) / W) + '%'; tip.style.top = (100 * y(d.saved) / H) + '%'; });
    hit.addEventListener('mouseleave', () => { tip.style.display = 'none'; });
    g.append(hit);
    svg.append(g);
    if (i % every === 0) svg.append(s('text', { class: 'axis', x: x + bw / 2, y: H - 8, 'text-anchor': 'middle' }, d.day.slice(5)));
  });
  return h('div', { class: 'chart' }, svg, tip);
}
function niceStep(raw) {
  const p = Math.pow(10, Math.floor(Math.log10(raw || 1)));
  return [1, 2, 2.5, 5, 10].map(m => m * p).find(v => v >= raw) || p * 10;
}

async function pageStats(app) {
  let days = 30;
  const body = h('div');
  const sel = h('select', { style: 'width:auto', 'aria-label': 'Time range', onchange: e => { days = +e.target.value; load(); } },
    [[7, 'Last 7 days'], [30, 'Last 30 days'], [90, 'Last 90 days'], [365, 'Last year']].map(([v, l]) => h('option', { value: v, selected: v === days }, l)));
  app.append(h('div', { class: 'row' }, h('h1', { style: 'margin:0' }, 'Stats'), h('span', { class: 'spacer' }), sel), h('div', { style: 'height:16px' }), body);
  async function load() {
    const s = await get('/stats?days=' + days);
    const pct = s.origBytes ? Math.round(100 * s.savedBytes / s.origBytes) : 0;
    const libs = Object.entries(s.libraries).sort((a, b) => b[1] - a[1]);
    const libMax = Math.max(1, ...libs.map(l => l[1]));
    const codecRows = [...new Set([...Object.keys(s.codecsIn), ...Object.keys(s.codecsOut)])];
    const showTable = h('details', {}, h('summary', { class: 'small muted' }, 'Show as table'),
      h('table', { class: 'small' }, h('tr', {}, h('th', {}, 'Day'), h('th', { class: 'num' }, 'Saved'), h('th', { class: 'num' }, 'Files')),
        s.daily.filter(d => d.files).map(d => h('tr', {}, h('td', {}, d.day), h('td', { class: 'num' }, bytes(d.saved)), h('td', { class: 'num' }, d.files)))));
    body.replaceChildren(
      h('p', { class: 'small muted', style: 'margin-top:0' }, 'Totals cover every file Shrinkarr has converted; the chart shows the selected range.'),
      h('div', { class: 'grid tiles' },
        tile('Space saved', bytes(s.savedBytes), pct + '% smaller'),
        tile('Files shrunk', s.files, s.failed ? `${s.failed} failed` : ''),
        tile('Before → after', bytes(s.origBytes), '→ ' + bytes(s.newBytes)),
        tile('Encoding time', duration(s.encodeHours * 3600), s.files ? 'avg ' + duration(s.encodeHours * 3600 / s.files) + ' per file' : '')),
      h('div', { class: 'card', style: 'margin-top:14px' }, h('h3', {}, 'Space saved per day'),
        s.daily.some(d => d.saved) ? [barChart(s.daily), showTable] : h('p', { class: 'muted' }, 'Nothing converted in this range yet.')),
      h('div', { class: 'grid', style: 'grid-template-columns:repeat(auto-fit,minmax(320px,1fr));margin-top:14px' },
        h('div', { class: 'card' }, h('h3', {}, 'Saved by library'),
          libs.length ? libs.map(([n, v]) => h('div', { class: 'hbar' }, h('span', {}, n), h('div', { class: 't' }, h('div', { style: `width:${Math.max(0, 100 * v / libMax)}%` })), h('span', { class: 'num' }, bytes(v)))) : h('p', { class: 'muted' }, 'No data yet.')),
        h('div', { class: 'card' }, h('h3', {}, 'Video codecs'),
          codecRows.length ? h('table', { class: 'small' }, h('tr', {}, h('th', {}, 'Codec'), h('th', { class: 'num' }, 'Before'), h('th', { class: 'num' }, 'After')),
            codecRows.map(c => h('tr', {}, h('td', {}, c || '?'), h('td', { class: 'num' }, s.codecsIn[c] || 0), h('td', { class: 'num' }, s.codecsOut[c] || 0)))) : h('p', { class: 'muted' }, 'No data yet.'))),
      h('div', { class: 'card', style: 'margin-top:14px' }, h('h3', {}, 'Biggest savings'),
        (s.top || []).length ? h('div', { class: 'table-wrap' }, h('table', { class: 'small' },
          h('tr', {}, h('th', {}, 'File'), h('th', { class: 'num' }, 'Before'), h('th', { class: 'num' }, 'After'), h('th', { class: 'num' }, 'Saved')),
          s.top.map(r => h('tr', { class: 'click', onclick: act(() => showRecord(r.id)) }, h('td', {}, h('span', { class: 'name' }, base(r.final || r.path))),
            h('td', { class: 'num' }, bytes(r.origSize)), h('td', { class: 'num' }, bytes(r.newSize)), h('td', { class: 'num' }, bytes(r.origSize - r.newSize)))))) : h('p', { class: 'muted' }, 'No data yet.')));
  }
  await load();
}

// ---------- history ----------
async function pageHistory(app) {
  let status = '';
  const box = h('div', { class: 'table-wrap' });
  app.append(h('div', { class: 'row' }, h('h1', { style: 'margin:0' }, 'History'), h('span', { class: 'spacer' }),
    h('select', { style: 'width:auto', 'aria-label': 'Filter', onchange: e => { status = e.target.value; load(); } },
      [['', 'All'], ['done', 'Done'], ['failed', 'Failed'], ['skipped', 'Skipped']].map(([v, l]) => h('option', { value: v }, l)))),
    h('div', { style: 'height:12px' }), box);
  async function load() {
    const list = await get('/history?limit=300&status=' + status);
    box.replaceChildren(list.length ? h('table', {},
      h('tr', {}, h('th', {}, 'File'), h('th', {}, 'Result'), h('th', { class: 'num' }, 'Saved'), h('th', { class: 'num hide-sm' }, 'When')),
      list.map(r => h('tr', { class: 'click', onclick: act(() => showRecord(r.id)) },
        h('td', {}, h('div', { class: 'name' }, base(r.final || r.path)), h('div', { class: 'path' }, r.flow)),
        h('td', { class: 'small' }, pill(r.status), ' ', r.detail),
        h('td', { class: 'num' }, r.status === 'done' ? bytes(r.origSize - r.newSize) : ''),
        h('td', { class: 'num hide-sm small muted' }, when(r.finished))))) : h('p', { class: 'muted' }, 'No history yet.'));
  }
  await load();
}

// ---------- settings ----------
async function pageSettings(app) {
  const [st, hw] = await Promise.all([get('/settings'), get('/hardware')]);
  const s = st.settings;
  const bind = (key, type = 'text', attrs = {}) => h('input', {
    type, value: s[key] ?? '', ...attrs,
    oninput: e => { s[key] = type === 'number' ? +e.target.value : e.target.value; },
  });
  const field = (label, input, help) => h('label', { class: 'f' }, h('span', {}, label), input, help ? h('span', { class: 'help' }, help) : null);
  const checkbox = (key, label) => h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: s[key], onchange: e => { s[key] = e.target.checked; } }), label);
  const works = id => (hw.encoders.find(e => e.id === id) || {}).works || [];
  hw.encoders.forEach(e => { e.works ||= []; });

  const arrBox = h('div');
  function renderArrs() {
    arrBox.replaceChildren(...s.arrs.map((a, i) => h('div', { class: 'card', style: 'margin-bottom:10px' },
      h('div', { class: 'form-grid' },
        field('Type', h('select', { onchange: e => { a.kind = e.target.value; } }, ['radarr', 'sonarr'].map(k => h('option', { value: k, selected: a.kind === k }, k === 'radarr' ? 'Radarr' : 'Sonarr')))),
        field('URL', h('input', { value: a.url, placeholder: 'http://192.168.1.10:7878', oninput: e => { a.url = e.target.value; } })),
        field('API key', h('input', { value: a.key, type: 'password', autocomplete: 'off', oninput: e => { a.key = e.target.value; } }), 'Settings → General in Radarr/Sonarr')),
      h('div', { class: 'row' },
        h('button', { class: 'btn sm', type: 'button', onclick: act(async () => { await post('/arr/test', a); toast('Connected ✓'); }) }, 'Test'),
        h('button', { class: 'btn sm danger', type: 'button', onclick: () => { s.arrs.splice(i, 1); renderArrs(); } }, 'Remove')))),
      h('button', { class: 'btn sm', type: 'button', onclick: () => { s.arrs.push({ kind: 'radarr', url: '', key: '' }); renderArrs(); } }, 'Add Radarr or Sonarr'));
  }
  s.arrs ||= [];
  renderArrs();

  const pw = h('input', { type: 'password', autocomplete: 'new-password', placeholder: st.passwordSet ? 'New password' : 'Choose a password' });
  app.append(
    h('h1', {}, 'Settings'),
    h('form', { onsubmit: act(async e => { e.preventDefault(); await post('/settings', s); toast('Settings saved'); refreshBanners(); }) },
      h('div', { class: 'card' }, h('h3', {}, 'Processing'),
        checkbox('testMode', 'Test mode: show what would happen, change no files'),
        h('div', { class: 'form-grid' },
          field('Files at the same time', bindNumber('workers', 1, 8), 'Keep at 1 unless your GPU can handle more.'),
          field('Hardware', h('select', { onchange: e => { s.hardware = e.target.value; } },
            [['auto', 'Automatic (best available)'], ...hw.encoders.map(e => [e.id, e.label + (e.works.length ? '' : ' (not found)')])].map(([v, l]) => h('option', { value: v, selected: s.hardware === v }, l))),
            'Used by Video: Encode blocks set to "settings".'),
          field('Only run from', bind('activeFrom', 'time'), 'Leave both empty to run any time.'),
          field('Only run until', bind('activeTo', 'time')),
          field('Wait before touching new files (minutes)', bindNumber('minAgeMinutes', 0, 1440), 'Avoids grabbing files that are still copying.'),
          field('Temporary folder', bind('tempDir'), 'Used for quality test samples.'),
          field('VMAF CPU threads', bindNumber('vmafThreads', 1, 64), 'Quality measuring runs on the CPU.'))),
      h('div', { class: 'card', style: 'margin-top:14px' }, h('h3', {}, 'Radarr / Sonarr'),
        h('p', { class: 'small muted' }, 'Lets the "Tell Radarr/Sonarr" block trigger a rescan after a file changes.'),
        arrBox,
        h('div', { class: 'form-grid', style: 'margin-top:12px' },
          field('Path here', bind('pathMapFrom', 'text', { placeholder: '/media' }), 'Only if Radarr/Sonarr see your media at a different path.'),
          field('Same path in Radarr/Sonarr', bind('pathMapTo', 'text', { placeholder: '/data/media' })))),
      h('div', { class: 'card', style: 'margin-top:14px' }, h('h3', {}, 'Notifications'),
        field('Discord webhook URL', bind('discordUrl', 'url', { placeholder: 'https://discord.com/api/webhooks/…' }), 'Failures are always sent. Add a "Send Discord message" block to a flow for successes.'),
        h('button', { class: 'btn sm', type: 'button', onclick: act(async () => { await post('/discord/test', { url: s.discordUrl }); toast('Sent'); }) }, 'Send test message')),
      h('div', { class: 'row', style: 'margin-top:14px' }, h('button', { class: 'btn primary', type: 'submit' }, 'Save settings'))),

    h('div', { class: 'card', style: 'margin-top:24px' }, h('h3', {}, 'Hardware found'),
      h('table', { class: 'small' }, h('tr', {}, h('th', {}, 'Encoder'), h('th', {}, 'HEVC'), h('th', {}, 'AV1'), h('th', {}, 'H.264')),
        hw.encoders.map(e => h('tr', {}, h('td', {}, e.label), ['hevc', 'av1', 'h264'].map(c => h('td', {}, works(e.id).includes(c) ? '✓ yes' : '— no'))))),
      h('p', { class: 'small' }, 'VMAF quality search: ', hw.vmaf ? '✓ available' : '— missing (Auto quality falls back to fixed)', ' · Dolby Vision removal: ', hw.dovi ? '✓ available' : '— missing'),
      h('p', { class: 'small muted mono' }, hw.ffmpeg),
      h('button', { class: 'btn sm', onclick: act(async () => { toast('Checking hardware…'); await post('/hardware/detect'); route(); }) }, 'Check again')),

    h('div', { class: 'card', style: 'margin-top:14px' }, h('h3', {}, 'Password'),
      h('p', { class: 'small muted' }, st.passwordSet ? 'A password is required to open Shrinkarr.' : 'Off. Anyone who can reach this page can use it. Turn it on if Shrinkarr is reachable outside your home network.'),
      h('div', { class: 'row' }, h('div', { style: 'flex:1;min-width:200px' }, pw),
        h('button', { class: 'btn', onclick: act(async () => { await post('/password', { password: pw.value }); toast('Password set. Log in again.'); route(); }) }, st.passwordSet ? 'Change' : 'Turn on'),
        st.passwordSet ? h('button', { class: 'btn danger', onclick: act(async () => { if (!confirm('Remove the password?')) return; await post('/password', { password: '' }); toast('Password removed'); route(); }) }, 'Turn off') : null,
        st.passwordSet ? h('button', { class: 'btn', onclick: act(async () => { await post('/logout'); location.reload(); }) }, 'Log out') : null)),

    h('div', { class: 'card', style: 'margin-top:14px' }, h('h3', {}, 'Setup'),
      h('button', { class: 'btn sm', onclick: () => wizard() }, 'Run the setup wizard again')));

  function bindNumber(key, min, max) { return bind(key, 'number', { min, max }); }
}

// ---------- first-run wizard ----------
async function wizard() {
  const flows = (await get('/flows')).filter(f => f.builtIn);
  const st = await get('/settings');
  const pick = { path: '', name: '', flowId: 'tpl-space', test: true };
  let step = 0;
  const body = h('div');
  const close = openModal(body, { locked: true, big: true });

  async function hwLine() {
    let hw = await get('/hardware');
    for (let i = 0; hw.detecting && i < 60; i++) { await new Promise(r => setTimeout(r, 1000)); hw = await get('/hardware'); }
    const found = hw.encoders.filter(e => (e.works || []).length && e.id !== 'cpu').map(e => e.label);
    return found.length ? `Graphics hardware found: ${found.join(', ')}. Shrinkarr will use it.` : 'No graphics card encoder found, so Shrinkarr will use the CPU. That works, just slower. If you have a GPU, check that it is passed through to the container.';
  }

  async function render() {
    const nav = (canNext, nextLabel = 'Next') => h('div', { class: 'row', style: 'margin-top:18px' },
      step > 0 ? h('button', { class: 'btn', onclick: () => { step--; render(); } }, 'Back') : h('button', { class: 'btn', onclick: act(async () => { await post('/settings', { ...st.settings, setupDone: true }); close(); }) }, 'Skip setup'),
      h('span', { class: 'spacer' }),
      h('button', { class: 'btn primary', disabled: !canNext, onclick: act(async () => { if (step === 3) return finish(); step++; render(); }) }, nextLabel));
    if (step === 0) {
      const line = h('p', { class: 'muted' }, 'Checking your hardware…');
      body.replaceChildren(h('h1', {}, 'Welcome to Shrinkarr'),
        h('p', {}, 'Shrinkarr goes through your movies and shows and re-saves them in a modern format, so they take less space and play on more devices.'),
        h('p', {}, 'This takes three quick steps: pick your media folder, pick what to do with it, and start.'),
        line, nav(true));
      hwLine().then(t => { line.textContent = t; }).catch(() => {});
    } else if (step === 1) {
      const chosen = h('p', { class: 'small' }, pick.path ? 'Selected: ' + pick.path : 'Open the folder that holds your videos, then press "Use this folder".');
      body.replaceChildren(h('h2', { style: 'margin-top:0' }, '1. Where are your videos?'),
        h('p', { class: 'small muted' }, 'Usually under /media. You can add more folders later on the Libraries page.'),
        browser('dir', pick.path || (st.settings.libraries[0] || {}).path || '/', p => { pick.path = p; pick.name = base(p) || 'Media'; step++; render(); }),
        chosen, nav(!!pick.path));
    } else if (step === 2) {
      body.replaceChildren(h('h2', { style: 'margin-top:0' }, '2. What should Shrinkarr do?'),
        h('div', { class: 'grid' }, flows.map(f => h('div', {
          class: 'card choice' + (pick.flowId === f.id ? ' on' : ''), tabindex: 0, role: 'radio', 'aria-checked': pick.flowId === f.id,
          onclick: () => { pick.flowId = f.id; render(); }, onkeydown: e => { if (e.key === 'Enter' || e.key === ' ') { pick.flowId = f.id; render(); } },
        }, h('div', { class: 'row' }, h('h3', { style: 'margin:0' }, f.name), f.id === 'tpl-space' ? h('span', { class: 'pill done' }, 'Recommended') : null), h('p', {}, f.description)))),
        h('p', { class: 'small muted' }, 'You can change this later, or build your own flow.'), nav(true));
    } else {
      body.replaceChildren(h('h2', { style: 'margin-top:0' }, '3. Ready'),
        h('p', {}, 'Folder: ', h('b', {}, pick.path)),
        h('p', {}, 'Flow: ', h('b', {}, flows.find(f => f.id === pick.flowId).name)),
        h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: pick.test, onchange: e => { pick.test = e.target.checked; } }),
          h('span', {}, h('b', {}, 'Start in test mode (recommended). '), 'Shrinkarr lists what it would do to each file, but changes nothing. Check the Dashboard, then turn test mode off.')),
        nav(true, 'Start'));
    }
  }

  async function finish() {
    const s = st.settings;
    s.libraries = [...s.libraries.filter(l => l.path !== pick.path), { name: pick.name, path: pick.path, flowId: pick.flowId, enabled: true, scanMinutes: 30 }];
    s.testMode = pick.test;
    s.setupDone = true;
    await post('/settings', s);
    await post('/scan', {});
    close();
    toast('Scanning your folder…');
    location.hash = '#/';
    route();
    refreshBanners();
  }
  render();
}

// ---------- boot ----------
window.addEventListener('hashchange', route);
(async () => {
  const a = await get('/auth').catch(() => ({}));
  if (a.required && !a.ok) { showLogin(); return; }
  await route();
  await refreshBanners();
  setInterval(refreshBanners, 10000);
  if (lastStatus && !lastStatus.setupDone) wizard();
})();
