'use strict';

const LAYERS = ['jellyfin', 'rclone', 'infinidysk', 'usenet'];
const SVGNS = 'http://www.w3.org/2000/svg';
const POLL_MS = { running: 3000, stale: 15000, empty: 5000, error: 5000 };

const state = {
  runs: null,
  runId: null,
  report: null,
  sel: null,
  error: null,
  notice: null,
  unreadable: false,
};

const main = document.getElementById('main');
const picker = document.getElementById('run');
const statusEl = document.getElementById('status');
const tipEl = document.getElementById('tip');

let charts = [];
let pollTimer = 0;
let loadSeq = 0;

function el(ns, tag, attrs, kids) {
  const e = ns ? document.createElementNS(ns, tag) : document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'text') e.textContent = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat(Infinity)) {
    if (k != null && k !== false) e.append(k instanceof Node ? k : String(k));
  }
  return e;
}
const h = (tag, attrs, ...kids) => el(null, tag, attrs, kids);
const s = (tag, attrs, ...kids) => el(SVGNS, tag, attrs, kids);

function chart(cls, draw) {
  const box = h('div', { class: 'chart ' + cls });
  charts.push({ box, draw });
  return box;
}
function drawCharts() {
  for (const c of charts) {
    if (!c.box.isConnected) continue;
    const w = Math.floor(c.box.clientWidth);
    if (w > 0 && w !== c.w) {
      c.w = w;
      c.box.replaceChildren(c.draw(w));
    }
  }
}

function icon(kind) {
  const svg = s('svg', { class: 'icon ' + kind, viewBox: '0 0 16 16', 'aria-hidden': 'true' });
  if (kind === 'critical') {
    svg.append(s('circle', { cx: 8, cy: 8, r: 7, fill: 'currentColor' }),
      s('path', { d: 'M5.5 5.5l5 5M10.5 5.5l-5 5', stroke: '#fff', 'stroke-width': 1.8, 'stroke-linecap': 'round' }));
  } else if (kind === 'warning') {
    svg.append(s('path', { d: 'M8 1.5l7 13H1z', fill: 'currentColor', 'stroke-linejoin': 'round' }),
      s('path', { d: 'M8 6v4M8 12.2v.1', stroke: '#0b0b0b', 'stroke-width': 1.8, 'stroke-linecap': 'round' }));
  } else {
    svg.append(s('circle', { cx: 8, cy: 8, r: 7, fill: 'currentColor' }),
      s('path', { d: 'M4.8 8.3l2.1 2.1 4.3-4.6', stroke: '#fff', 'stroke-width': 1.8, fill: 'none', 'stroke-linecap': 'round', 'stroke-linejoin': 'round' }));
  }
  return svg;
}
const errLine = (text) => h('div', { class: 'err' }, icon('critical'), h('span', { text }));
const mini = (w, ...kids) => s('svg', { width: w, height: 14, 'aria-hidden': 'true' }, ...kids);
const swatch = (layer) => mini(10, s('circle', { cx: 5, cy: 7, r: 4.5, class: 'f' + slotOf(layer) }));
const failCross = (cx, cy, r) => s('path', { d: `M${cx - r} ${cy - r}L${cx + r} ${cy + r}M${cx + r} ${cy - r}L${cx - r} ${cy + r}`, class: 'failx' });

function slotOf(layer) {
  const i = LAYERS.indexOf(layer);
  return i < 0 ? 'u' : String(i);
}
function stackRank(layer) {
  const i = LAYERS.indexOf(layer);
  return i < 0 ? LAYERS.length : i;
}
const phaseLabel = (t, k) => (k === 0 ? 'startup' : 'seek ' + t.plan.seeks[k - 1].point);

function fmtDur(ns) {
  const ms = ns / 1e6;
  if (ms < 10) return ms.toFixed(1) + '\u00a0ms';
  if (ms < 999.5) return Math.round(ms) + '\u00a0ms';
  if (ms < 59950) return (ms / 1000).toFixed(ms < 9995 ? 2 : 1) + '\u00a0s';
  const m = Math.floor(ms / 60000);
  return `${m}m\u00a0${String(Math.round((ms - m * 60000) / 1000)).padStart(2, '0')}s`;
}
function fmtBytes(n) {
  if (n < 1024) return n + '\u00a0B';
  if (n < 1024 ** 2) return Math.round(n / 1024) + '\u00a0KiB';
  if (n < 1024 ** 3) return (n / 1024 ** 2).toFixed(1) + '\u00a0MiB';
  return (n / 1024 ** 3).toFixed(2) + '\u00a0GiB';
}
function fmtClock(ns) {
  const t = Math.round(ns / 1e9);
  const hh = Math.floor(t / 3600);
  const mm = Math.floor((t % 3600) / 60);
  const ss = String(t % 60).padStart(2, '0');
  return hh ? `${hh}:${String(mm).padStart(2, '0')}:${ss}` : `${mm}:${ss}`;
}
const fmtWhen = (iso) => new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
const fmtDay = (ms) => new Date(ms).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
const fmtReqs = (n) => `${Number.isInteger(n) ? n : n.toFixed(1)}\u00a0req${n === 1 ? '' : 's'}`;

function ticks(max, n) {
  if (!(max > 0)) max = 1;
  const raw = max / Math.max(1, n);
  const p = 10 ** Math.floor(Math.log10(raw));
  const f = raw / p;
  const step = (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * p;
  const out = [];
  for (let i = 0; i * step < max + step * 0.999; i++) out.push(i * step);
  if (out.length < 2) out.push(step);
  return out;
}
function tickLabel(v, tk, unit) {
  const top = tk[tk.length - 1];
  const step = tk[1] - tk[0];
  const last = v === top && unit;
  if (v === 0) return '0';
  if (top >= 1e9) return (v / 1e9).toFixed(step >= 1e9 ? 0 : 1) + (last ? ' s' : '');
  return Math.round(v / 1e6) + (last ? ' ms' : '');
}

const tips = new WeakMap();
function withTip(node, fn) {
  tips.set(node, fn);
  return node;
}
function tipOwner(t) {
  for (let n = t; n && n.nodeType === 1; n = n.parentNode) if (tips.has(n)) return n;
  return null;
}
function showTip(node, cx, cy) {
  const content = tips.get(node)();
  if (!content) return hideTip();
  tipEl.replaceChildren(...[content].flat(Infinity).filter(Boolean));
  tipEl.hidden = false;
  const r = tipEl.getBoundingClientRect();
  let left = cx + 14;
  let top = cy + 14;
  if (left + r.width > innerWidth - 8) left = Math.max(8, cx - r.width - 14);
  if (top + r.height > innerHeight - 8) top = Math.max(8, cy - r.height - 14);
  tipEl.style.left = left + 'px';
  tipEl.style.top = top + 'px';
}
function hideTip() {
  tipEl.hidden = true;
}
document.addEventListener('pointermove', (e) => {
  const n = tipOwner(e.target);
  if (n) showTip(n, e.clientX, e.clientY);
  else if (!tipEl.hidden) hideTip();
});
document.addEventListener('focusin', (e) => {
  const n = tipOwner(e.target);
  if (n && n.matches(':focus-visible')) {
    const r = n.getBoundingClientRect();
    showTip(n, r.left + r.width / 2, r.bottom);
  } else hideTip();
});
document.addEventListener('focusout', hideTip);
window.addEventListener('scroll', hideTip, { passive: true });

async function getJSON(url) {
  let res;
  try {
    res = await fetch(url, { cache: 'no-store' });
  } catch (e) {
    throw new Error(`can't reach nzbff serve (${e.message})`);
  }
  let body = null;
  try {
    body = await res.json();
  } catch {
  }
  if (!res.ok) throw new Error(body && body.error ? body.error : `${res.status} ${res.statusText}`);
  if (body === null) throw new Error(`unreadable response from ${url}`);
  return body;
}

function schedule(ms) {
  clearTimeout(pollTimer);
  pollTimer = setTimeout(() => (document.hidden ? schedule(ms) : refresh()), ms);
}

async function refresh() {
  clearTimeout(pollTimer);
  const seq = ++loadSeq;
  try {
    const runs = await getJSON('api/runs');
    if (seq !== loadSeq) return;
    state.runs = runs;
    if (runs.length === 0) {
      Object.assign(state, { report: null, error: null, sel: null });
      render();
      schedule(POLL_MS.empty);
      return;
    }
    let entry = runs.find((r) => r.id === state.runId);
    if (!entry) {
      entry = runs.find((r) => !r.err) || runs[0];
      if (state.runId) {
        state.notice = `Run ${state.runId} is not in the results directory; showing the newest run.`;
        state.sel = null;
      } else if (entry !== runs[0]) {
        state.notice = `The newest run file, ${runs[0].id}, can't be read (${runs[0].err}); showing the newest readable run.`;
      }
    }
    state.runId = entry.id;
    if (entry.err) {
      Object.assign(state, { report: null, sel: null, error: `Run ${entry.id} can't be read: ${entry.err}`, unreadable: true });
      render();
      return;
    }
    const rep = await getJSON('api/runs/' + encodeURIComponent(entry.id));
    if (seq !== loadSeq) return;
    Object.assign(state, { report: rep, error: null, unreadable: false });
    if (state.sel && !findSel(rep, state.sel)) state.sel = null;
    render();
    if (rep.run.status === 'running') schedule(isStale(rep) ? POLL_MS.stale : POLL_MS.running);
  } catch (e) {
    if (seq !== loadSeq) return;
    state.error = e.message;
    state.unreadable = false;
    render();
    schedule(POLL_MS.error);
  }
}

function openRun(id) {
  state.runId = id;
  state.sel = null;
  state.notice = null;
  writeHash();
  main.classList.add('busy');
  window.scrollTo({ top: 0 });
  refresh();
}

function select(sel, scroll) {
  state.sel = sel;
  writeHash();
  render();
  const head = document.getElementById('sample-h');
  if (scroll && head) {
    head.focus({ preventScroll: true });
    document.getElementById('sample').scrollIntoView({ block: 'start' });
  }
}

function writeHash() {
  const p = new URLSearchParams();
  if (state.runId) p.set('run', state.runId);
  if (state.sel) {
    p.set('t', state.sel.target);
    p.set('l', state.sel.layer);
    p.set('i', state.sel.idx);
    p.set('p', state.sel.phase);
  }
  history.replaceState(null, '', '#' + p);
}

function readHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  state.runId = p.get('run');
  if (p.has('t') && p.has('l')) {
    state.sel = { target: p.get('t'), layer: p.get('l'), idx: Number(p.get('i')) || 0, phase: Number(p.get('p')) || 0 };
  }
}

function lastActivity(rep) {
  let t = Date.parse(rep.run.started);
  for (const c of rep.cells || []) for (const sm of c.samples || []) t = Math.max(t, Date.parse(sm.at));
  return t;
}

function isStale(rep) {
  const st = rep.run.settings || {};
  const limitMs = (2 * (st.timeout_ns || 60e9) + (st.cooldown_ns || 0)) / 1e6 + 60000;
  return rep.run.status === 'running' && Date.now() - lastActivity(rep) > limitMs;
}

function findSel(rep, sel) {
  const target = (rep.targets || []).find((t) => t.name === sel.target);
  const cell = (rep.cells || []).find((c) => c.target === sel.target && c.layer === sel.layer);
  const sample = cell && (cell.samples || [])[sel.idx];
  return target && sample ? { target, cell, sample } : null;
}

function render() {
  hideTip();
  const focused = document.activeElement && document.activeElement.id;
  charts = [];
  renderPicker();
  renderStatus();
  const parts = [];
  if (state.notice) parts.push(h('div', { class: 'notice' }, icon('warning'), h('span', { text: state.notice })));
  if (state.runs && state.runs.length === 0) {
    parts.push(emptyView());
  } else if (!state.report) {
    parts.push(state.error ? errorView(state.error, !state.unreadable) : h('p', { class: 'sub', text: 'Loading runs…' }));
    if (state.unreadable) parts.push(trendsView());
  } else {
    if (state.error) {
      parts.push(h('div', { class: 'notice' }, icon('warning'),
        h('span', { text: `Lost contact with nzbff serve: ${state.error}. Showing the last data; retrying every few seconds.` })));
    }
    parts.push(runView(state.report), sampleView(state.report), trendsView());
  }
  main.classList.remove('busy');
  main.replaceChildren(...parts);
  drawCharts();
  if (focused) {
    const again = document.getElementById(focused);
    if (again && again !== document.activeElement) again.focus({ preventScroll: true });
  }
}

function renderPicker() {
  const runs = state.runs || [];
  picker.replaceChildren(...runs.map((r, i) => h('option', {
    value: r.id,
    selected: r.id === state.runId,
    text: r.err ? `${r.id} · unreadable` : `${fmtWhen(r.started)} · ${r.status}${i === 0 ? ' · latest' : ''}`,
  })));
  picker.disabled = runs.length === 0;
  picker.hidden = runs.length === 0;
}

function renderStatus() {
  statusEl.replaceChildren();
  const rep = state.report;
  if (!rep) return;
  const st = rep.run.status;
  if (st === 'running' && isStale(rep)) {
    const mins = Math.round((Date.now() - lastActivity(rep)) / 60000);
    statusEl.append(icon('warning'), `running, but no new sample for ${mins} min; the process may have died`);
  } else if (st === 'running') {
    statusEl.append(h('span', { class: 'pulse', 'aria-hidden': 'true' }), `running · refreshing every ${POLL_MS.running / 1000} s`);
  } else if (st === 'interrupted') {
    statusEl.append(icon('warning'), 'interrupted');
  } else {
    statusEl.append(icon('good'), st);
  }
}

function emptyView() {
  const cmd = (text) => h('code', { class: 'cmd', text });
  const step = (n, text, command) => h('li', {}, h('span', { class: 'n', text: n }), h('div', {}, text, cmd(command)));
  return h('section', { class: 'card empty', id: 'empty' },
    h('h2', { text: 'No runs yet' }),
    h('p', { text: 'nzbff stores each run in its results directory. This page checks for the first one every few seconds.' }),
    h('ol', { class: 'steps' },
      step('1', 'Write a config, then fill in your stack and at least one target:', 'nzbff config init'),
      step('2', 'Measure every layer and store the run:', 'nzbff run')),
    h('h3', { text: 'Just trying it out?' }),
    h('p', { text: 'Run against a fake stack with known latency:' }),
    h('ol', { class: 'steps' },
      step('1', 'Start it and leave it running:', 'nzbff dev-stack > fake.yaml'),
      step('2', 'In a second terminal, measure it:', 'nzbff run -c fake.yaml'),
      step('3', 'Open the dashboard on its results:', 'nzbff serve -c fake.yaml')));
}

function errorView(msg, retrying) {
  return h('section', { class: 'card', id: 'error' },
    h('div', { class: 'err' }, icon('critical'), h('h2', { text: retrying ? "Can't load results" : "Can't read this run" })),
    h('p', { class: 'sub', text: msg }),
    retrying && h('p', { class: 'muted', text: 'Retrying every few seconds.' }),
    h('p', {}, h('button', { type: 'button', onclick: () => refresh(), text: retrying ? 'Retry now' : 'Read it again' })));
}

function runView(rep) {
  const r = rep.run;
  const st = r.settings || {};
  const newest = state.runs[0] && state.runs[0].id === r.id;
  const facts = [
    `started ${fmtWhen(r.started)}`,
    r.finished && `took ${fmtDur((Date.parse(r.finished) - Date.parse(r.started)) * 1e6)}`,
    r.host, r.version && `nzbff ${r.version}`, `mode ${r.mode}`,
    `${st.iterations} iterations + ${st.warmup} warmup`,
    st.cold_cache && 'rclone dir cache dropped before each sample',
    (st.seek_points || []).length && `seeks ${st.seek_points.join(', ')}`,
    st.timeout_ns && `timeout ${fmtDur(st.timeout_ns)}`,
  ].filter(Boolean).join(' · ');
  const targets = rep.targets || [];
  return h('section', { id: 'latest', 'aria-labelledby': 'latest-h' },
    h('div', { class: 'section-head' }, h('h2', { id: 'latest-h', text: newest ? 'Latest run' : 'Run' }), h('p', { text: facts })),
    h('div', { class: 'legend' },
      h('span', {}, mini(12, s('circle', { cx: 6, cy: 7, r: 4.5, class: 'fu' })), 'measured sample'),
      h('span', {}, mini(12, s('circle', { cx: 6, cy: 7, r: 4, class: 'su', fill: 'none', 'stroke-width': 2 })), 'warmup (not in stats)'),
      h('span', {}, mini(12, s('path', { d: 'M4 2.5L9 7L4 11.5', class: 'chev su' })), 'off the scale (cold warmup)'),
      h('span', {}, mini(12, failCross(6, 7, 3.5)), 'failed before this phase'),
      h('span', {}, mini(8, s('line', { x1: 4, x2: 4, y1: 1, y2: 13, class: 'p50' })), 'p50'),
      h('span', { text: 'Rows are layers, top of the stack first. Click a dot to see why it took that long.' })),
    targets.length ? targets.map((t) => targetCard(rep, t)) : h('p', { class: 'card sub', text: 'No targets planned yet.' }));
}

function targetCard(rep, t) {
  const cells = (rep.cells || []).filter((c) => c.target === t.name).sort((a, b) => stackRank(a.layer) - stackRank(b.layer));
  let measured = 0;
  let all = 0;
  let anyFail = false;
  for (const c of cells) {
    for (const sm of c.samples || []) {
      for (const p of sm.analysis.phases) {
        if (!p.ok) anyFail = true;
        else {
          all = Math.max(all, p.latency_ns);
          if (!sm.warmup) measured = Math.max(measured, p.latency_ns);
        }
      }
    }
  }
  const scale = { max: measured || all || 1e9, gutter: anyFail ? 34 : 0 };
  const phases = [];
  for (let k = 0; k < 1 + (t.plan.seeks || []).length; k++) phases.push(phaseBlock(t, k, cells, scale));
  return h('article', { class: 'card' },
    h('div', { class: 'card-head' }, h('h3', { text: t.name }),
      h('span', { class: 'sub', text: [t.path, t.duration_ns ? fmtClock(t.duration_ns) : ''].filter(Boolean).join(' · ') })),
    t.err && errLine(`Could not plan this target: ${t.err}`),
    phases,
    statsTable(t, cells));
}

function stripGeom(w, scale) {
  const pl = 6;
  const pr = w - scale.gutter - 8;
  const tk = ticks(scale.max, Math.max(2, Math.floor((pr - pl) / 80)));
  const top = tk[tk.length - 1];
  return { pl, pr, tk, x: (v) => pl + ((pr - pl) * Math.min(v, top)) / top, off: (v) => v > top };
}

function phaseBlock(t, k, cells, scale) {
  const seek = k > 0 ? t.plan.seeks[k - 1] : null;
  let sub = 'open to first frame';
  if (seek) sub = seek.offset_ns ? `to ${fmtClock(seek.offset_ns)}, previous frame to first frame` : 'previous frame to first frame';
  return h('div', { class: 'phase' },
    h('div', { class: 'phase-head' }, h('h4', { text: phaseLabel(t, k) }), h('span', { class: 'sub', text: sub })),
    cells.map((c) => stripRow(t, k, c, scale)),
    h('div', { class: 'row axis', 'aria-hidden': 'true' }, h('div', { class: 'row-label' }),
      chart('axis', (w) => drawStripAxis(w, scale)), h('div', { class: 'row-stats' })));
}

function stripRow(t, k, cell, scale) {
  const st = (cell.phases || [])[k];
  const n = (cell.samples || []).length;
  const stats = h('div', { class: 'row-stats' });
  if (st && st.n > 0) {
    stats.append(
      h('span', {}, h('span', { class: 'k', text: 'p50 ' }), h('b', { class: 'num', text: fmtDur(st.p50_ns) })),
      h('span', { class: 'num' }, h('span', { class: 'k', text: 'p90 ' }), fmtDur(st.p90_ns)),
      h('span', { text: fmtReqs(st.requests_p50) }));
  } else {
    stats.append(h('span', { class: 'none', text: n ? 'no successful sample' : 'no samples yet' }));
  }
  if (st && st.failed) stats.append(h('span', { class: 'fail' }, mini(10, failCross(5, 7, 3.5)), `${st.failed} failed`));
  return h('div', { class: 'row' },
    h('div', { class: 'row-label' }, swatch(cell.layer), h('span', { text: cell.layer })),
    chart('strip', (w) => drawStrip(w, t, k, cell, st, scale)),
    stats);
}

const JITTER = [0, -6, 6, -3, 3, -8, 8];

function drawStrip(w, t, k, cell, st, scale) {
  const H = 30;
  const mid = 15;
  const g = stripGeom(w, scale);
  const sl = slotOf(cell.layer);
  const label = phaseLabel(t, k);
  const svg = s('svg', { width: w, height: H, role: 'group', 'aria-label': `${cell.layer}, ${label}` });
  for (const v of g.tk) svg.append(s('line', { x1: g.x(v), x2: g.x(v), y1: 3, y2: H - 3, class: 'grid' }));
  if (scale.gutter) svg.append(s('line', { x1: g.pr + 8, x2: g.pr + 8, y1: 3, y2: H - 3, class: 'axis-line' }));
  const sel = state.sel;
  (cell.samples || []).forEach((sm, idx) => {
    const ph = sm.analysis.phases[k];
    const cy = mid + JITTER[idx % JITTER.length];
    const cx = ph.ok ? g.x(ph.latency_ns) : g.pr + 8 + scale.gutter / 2;
    const kind = sm.warmup ? 'warmup' : 'measured';
    const pick = () => select({ target: t.name, layer: cell.layer, idx, phase: k }, true);
    const dot = s('g', {
      class: 'dot' + (sm.warmup ? ' warm' : ''), tabindex: 0, role: 'button',
      'aria-label': `${kind} sample ${sm.iteration + 1}: ${ph.ok ? fmtDur(ph.latency_ns) : 'failed'}. Open waterfall.`,
      onclick: pick,
      onkeydown: (e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          pick();
        }
      },
    }, s('circle', { cx, cy, r: 12, class: 'hit' }));
    if (!ph.ok) dot.append(failCross(cx, cy, 3.5));
    else if (g.off(ph.latency_ns)) dot.append(s('path', { d: `M${cx - 5} ${cy - 4.5}L${cx} ${cy}L${cx - 5} ${cy + 4.5}`, class: 'chev s' + sl }));
    else if (sm.warmup) dot.append(s('circle', { cx, cy, r: 4, class: 'hollow s' + sl }));
    else dot.append(s('circle', { cx, cy, r: 5, class: 'solid f' + sl }));
    if (sel && sel.target === t.name && sel.layer === cell.layer && sel.idx === idx) {
      dot.append(s('circle', { cx, cy, r: 8.5, class: 'selring' }));
    }
    withTip(dot, () => [
      h('strong', { text: ph.ok ? fmtDur(ph.latency_ns) + (g.off(ph.latency_ns) ? ' (off the scale)' : '') : 'phase not reached' }),
      h('div', { text: `${cell.layer} · ${label}` }),
      h('div', { text: `${kind} #${sm.iteration + 1}` + (ph.ok ? ` · ${fmtReqs(ph.requests)} · ${fmtBytes(ph.bytes)}` : '') }),
      ph.ok && h('div', { text: `wait ${fmtDur(ph.wait_ns)} · transfer ${fmtDur(ph.transfer_ns)} · client ${fmtDur(ph.client_ns)}` }),
      sm.err && h('div', { class: 'tip-err', text: sm.err }),
      h('div', { class: 'tip-hint', text: 'Click for the waterfall' }),
    ]);
    svg.append(dot);
  });
  if (st && st.n > 0) {
    const px = g.x(st.p50_ns);
    svg.append(s('line', { x1: px, x2: px, y1: 3, y2: H - 3, class: 'p50-halo' }), s('line', { x1: px, x2: px, y1: 3, y2: H - 3, class: 'p50' }));
  }
  return svg;
}

function drawStripAxis(w, scale) {
  const g = stripGeom(w, scale);
  const svg = s('svg', { width: w, height: 16 });
  g.tk.forEach((v, i) => {
    const anchor = i === 0 ? 'start' : i === g.tk.length - 1 ? 'end' : 'middle';
    svg.append(s('text', { x: g.x(v) + (i === 0 ? -2 : 0), y: 12, 'text-anchor': anchor, class: 'tick' }, tickLabel(v, g.tk, true)));
  });
  if (scale.gutter) svg.append(s('text', { x: g.pr + 8 + scale.gutter / 2, y: 12, 'text-anchor': 'middle', class: 'tick' }, 'failed'));
  return svg;
}

function statsTable(t, cells) {
  const rows = [];
  for (const c of cells) {
    for (const p of c.phases || []) {
      const ok = p.n > 0;
      rows.push(h('tr', {},
        h('td', { text: c.layer }), h('td', { class: 't-left', text: p.label }), h('td', { text: p.n }), h('td', { text: p.failed }),
        ...[p.p50_ns, p.p90_ns, p.min_ns, p.max_ns].map((v) => h('td', { text: ok ? fmtDur(v) : '—' })),
        h('td', { text: ok ? p.requests_p50 : '—' }), h('td', { text: ok ? fmtBytes(p.bytes_p50) : '—' }),
        h('td', { text: ok && p.first_ttfb_p50_ns ? fmtDur(p.first_ttfb_p50_ns) : '—' })));
    }
  }
  return h('details', {}, h('summary', { text: `Table for ${t.name}` }),
    h('div', { class: 'table-wrap' }, h('table', {},
      h('thead', {}, h('tr', {}, ['layer', 'phase', 'n', 'failed', 'p50', 'p90', 'min', 'max', 'reqs', 'bytes', 'first TTFB']
        .map((x, i) => h('th', { class: i === 1 ? 't-left' : null, text: x })))),
      h('tbody', {}, rows))));
}

function sampleView(rep) {
  const found = state.sel && findSel(rep, state.sel);
  if (!found) {
    return h('section', { class: 'card', id: 'sample' },
      h('h2', { id: 'sample-h', tabindex: -1, text: 'Sample' }),
      h('p', { class: 'sub', text: 'Click any dot above to open its waterfall: every upstream request of that session, when it waited, when bytes flowed, what the client abandoned, and which phase it served.' }));
  }
  const { target, cell, sample } = found;
  const sel = state.sel;
  const tl = sample.timeline || {};
  const an = sample.analysis;
  const samples = cell.samples || [];
  const move = (d) => select({ ...sel, idx: sel.idx + d }, false);
  const facts = [
    `started ${new Date(sample.at).toLocaleTimeString()}`,
    `session ${fmtDur(tl.end_ns || 0)}`,
    `${(tl.ops || []).length} upstream ops`,
    `${fmtBytes(an.bytes)} read`,
    an.tail_bytes ? `${fmtBytes(an.tail_bytes)} after the last frame` : null,
  ].filter(Boolean).join(' · ');
  return h('section', { class: 'card', id: 'sample', 'aria-labelledby': 'sample-h' },
    h('div', { class: 'sample-head' },
      h('div', { class: 'sample-title' },
        h('h2', { id: 'sample-h', tabindex: -1, text: 'Sample' }),
        h('span', { class: 'who' }, `${target.name} ·`, swatch(cell.layer), cell.layer),
        h('span', { class: 'sub', text: `${sample.warmup ? 'warmup' : 'measured'} #${sample.iteration + 1}` })),
      h('div', { class: 'sample-nav' },
        h('button', { type: 'button', id: 'prev', disabled: sel.idx === 0, onclick: () => move(-1), text: '‹ Prev' }),
        h('button', { type: 'button', id: 'next', disabled: sel.idx >= samples.length - 1, onclick: () => move(1), text: 'Next ›' }),
        h('button', { type: 'button', 'aria-label': 'Close sample', onclick: () => select(null, false), text: '×' }))),
    h('p', { class: 'facts', text: facts }),
    sample.err && errLine(sample.err),
    breakdown(target, cell, sample),
    waterfallLegend(slotOf(cell.layer)),
    chart('wf', (w) => drawWaterfall(w, target, cell, sample)),
    opsTable(target, sample));
}

function breakdown(target, cell, sample) {
  const sl = slotOf(cell.layer);
  const phases = sample.analysis.phases;
  const max = Math.max(1, ...phases.map((p) => p.latency_ns));
  return h('div', { class: 'bd' }, phases.map((p, k) => {
    const hi = state.sel.phase === k;
    return [
      h('div', { class: 'bd-label' + (hi ? ' bd-row hi' : '') }, hi && h('span', { class: 'hi-mark', 'aria-hidden': 'true' }), phaseLabel(target, k)),
      h('div', { class: 'bd-lat', text: p.ok ? fmtDur(p.latency_ns) : '—' }),
      p.ok ? chart('bd-bar', (w) => drawBreakdown(w, p, max, sl)) : h('div', { class: 'err' }, icon('critical'), 'not reached'),
      p.ok && h('div', { class: 'bd-why num', text: `${fmtReqs(p.requests)} · ${fmtBytes(p.bytes)} · wait ${fmtDur(p.wait_ns)} · transfer ${fmtDur(p.transfer_ns)} · client ${fmtDur(p.client_ns)}` }),
    ];
  }));
}

function drawBreakdown(w, p, max, sl) {
  const svg = s('svg', { width: w, height: 12, 'aria-hidden': 'true' });
  const parts = [[p.wait_ns, 'f' + sl + ' seg-wait'], [p.transfer_ns, 'f' + sl], [Math.max(0, p.client_ns), 'seg-client']];
  let x = 0;
  for (const [v, cls] of parts) {
    const width = ((w - 4) * v) / max;
    if (width < 0.5) continue;
    svg.append(s('rect', { x, y: 2, width: Math.max(1, width), height: 8, rx: 1.5, class: cls }));
    x += width + 2;
  }
  return svg;
}

function waterfallLegend(sl) {
  const box = (cls, fill) => mini(18, s('rect', { x: 1, y: 2, width: 16, height: 10, rx: 1.5, class: cls, fill }));
  return h('div', { class: 'legend' },
    h('span', {}, box('dial'), 'dial (new connection)'),
    h('span', {}, box('wait f' + sl), 'waiting for the first byte'),
    h('span', {}, box('f' + sl), 'transfer'),
    h('span', {}, box('', `url(#hatch-${sl})`), 'abandoned read-ahead'),
    h('span', {}, mini(8, s('line', { x1: 4, x2: 4, y1: 0, y2: 14, class: 'mark' })), 'phase done (first frame / article end)'),
    h('span', {}, box('band'), 'phase'));
}

function opPhases(sample) {
  const m = new Map();
  for (const p of sample.analysis.phases) for (const i of p.ops || []) m.set(i, p.phase);
  return m;
}

function unattributed(target, sample) {
  const marks = (sample.timeline.marks || []).length;
  const planned = 1 + (target.plan.seeks || []).length;
  if (sample.err && marks < planned) return `after the last mark (${phaseLabel(target, marks)} never finished)`;
  return 'tail read-ahead: no phase needed it';
}

function drawWaterfall(w, target, cell, sample) {
  const tl = sample.timeline || {};
  const ops = tl.ops || [];
  const marks = tl.marks || [];
  const sl = slotOf(cell.layer);
  const phaseOf = opPhases(sample);
  const wide = w >= 640;
  const ROW = 22;
  const HEAD = 24;
  const cols = wide ? [
    ['offset', 76, (op) => (op.offset < 0 ? '—' : fmtBytes(op.offset))],
    ['bytes', 70, (op) => (op.bytes ? fmtBytes(op.bytes) : '—')],
    ['TTFB', 66, (op) => (op.first_byte_ns ? fmtDur(op.first_byte_ns - op.start_ns) : '—')],
    ['dial', 58, (op) => (op.dial_ns ? fmtDur(op.dial_ns) : '—')],
  ] : [];
  const pl = wide ? 92 : 64;
  const pr = w - cols.reduce((a, c) => a + c[1], 0) - (wide ? 14 : 4);
  const tEnd = Math.max(tl.end_ns || 0, ...ops.map((o) => o.end_ns), ...marks.map((m) => m.at_ns), 1);
  const tk = ticks(tEnd, Math.max(2, Math.floor((pr - pl) / 90)));
  const top = tk[tk.length - 1];
  const x = (t) => pl + ((pr - pl) * t) / top;
  const bodyTop = HEAD - 4;
  const bodyBot = HEAD + ops.length * ROW;
  const svg = s('svg', { width: w, height: bodyBot + 22, role: 'group', 'aria-label': `Waterfall of ${ops.length} upstream operations` });

  let lo = ops.length ? ops[0].start_ns : 0;
  marks.forEach((m, k) => {
    const x0 = x(Math.min(lo, m.at_ns));
    const x1 = x(m.at_ns);
    const cls = 'band' + (k === state.sel.phase ? ' hi' : k % 2 ? ' alt' : '');
    svg.append(s('rect', { x: x0, y: bodyTop, width: Math.max(0, x1 - x0), height: bodyBot - bodyTop, class: cls }));
    const full = phaseLabel(target, k);
    const short = k === 0 ? 'start' : target.plan.seeks[k - 1].point;
    const fit = [full, short].find((txt) => txt.length * 6.2 + 6 <= x1 - x0);
    if (fit) svg.append(s('text', { x: (x0 + x1) / 2, y: 13, 'text-anchor': 'middle', class: 'band-label' }, fit));
    lo = m.at_ns;
  });
  for (const v of tk) svg.append(s('line', { x1: x(v), x2: x(v), y1: bodyBot, y2: bodyBot + 4, class: 'axis-line' }));
  svg.append(s('line', { x1: pl, x2: pr, y1: bodyBot, y2: bodyBot, class: 'axis-line' }));
  tk.forEach((v, i) => {
    const anchor = i === tk.length - 1 ? 'end' : i === 0 ? 'start' : 'middle';
    svg.append(s('text', { x: x(v), y: bodyBot + 16, 'text-anchor': anchor, class: 'tick' }, tickLabel(v, tk, true)));
  });
  let cx = pr + 14;
  for (const [head, cw] of cols) {
    cx += cw;
    svg.append(s('text', { x: cx - 4, y: 13, 'text-anchor': 'end', class: 'col-head' }, head));
  }

  ops.forEach((op, i) => {
    const y0 = HEAD + i * ROW;
    const yc = y0 + ROW / 2;
    const fb = op.first_byte_ns || 0;
    const dialEnd = op.start_ns + (op.dial_ns || 0);
    const where = phaseOf.has(i) ? `served ${phaseLabel(target, phaseOf.get(i))}` : unattributed(target, sample);
    const g = s('g', { class: 'op', tabindex: 0, 'aria-label': `${i + 1} ${op.kind}, ${where}` },
      s('rect', { x: 0, y: y0, width: w, height: ROW, class: 'op-hit' }),
      s('text', { x: 2, y: yc + 4, class: 'op-label' }, `${i + 1} ${op.kind}`));
    const bar = (a, b, cls, fill) => s('rect', { x: a, y: yc - 5, width: Math.max(1, b - a), height: 10, rx: 1.5, class: cls, fill });
    if (op.dial_ns) g.append(bar(x(op.start_ns), x(dialEnd), 'dial'));
    g.append(bar(x(dialEnd), x(fb || op.end_ns), 'wait f' + sl));
    if (fb) {
      const next = op.abandoned && ops[i + 1] ? ops[i + 1].start_ns : op.end_ns;
      const useful = Math.max(fb, Math.min(op.end_ns, next));
      if (useful > fb) g.append(bar(x(fb), x(useful), 'f' + sl));
      if (op.end_ns > useful) g.append(bar(x(useful), x(op.end_ns), '', `url(#hatch-${sl})`));
    }
    if (op.err && !fb) g.append(failCross(x(op.end_ns) + 7, yc, 3.5));
    if (wide) {
      let colX = pr + 14;
      for (const [, cw, val] of cols) {
        colX += cw;
        g.append(s('text', { x: colX - 4, y: yc + 4, 'text-anchor': 'end', class: 'op-num' }, val(op)));
      }
    } else {
      const txt = [op.bytes ? fmtBytes(op.bytes) : '', fb ? fmtDur(fb - op.start_ns) : ''].filter(Boolean).join(' · ');
      const est = txt.length * 5.6;
      const after = x(op.end_ns) + (op.err && !fb ? 14 : 4);
      if (after + est <= w) g.append(s('text', { x: after, y: yc + 4, class: 'op-num' }, txt));
      else if (x(op.start_ns) - 4 - est >= pl) g.append(s('text', { x: x(op.start_ns) - 4, y: yc + 4, 'text-anchor': 'end', class: 'op-num' }, txt));
    }
    withTip(g, () => [
      h('strong', { text: fb ? `TTFB ${fmtDur(fb - op.start_ns)}` : op.err ? 'no response' : 'no payload' }),
      h('div', { text: op.kind + (op.ref ? ' ' + op.ref : '') }),
      h('div', { class: 'num', text: `start ${fmtDur(op.start_ns)} → ${fb ? `first byte ${fmtDur(fb)} → ` : ''}end ${fmtDur(op.end_ns)}` }),
      h('div', { text: [op.bytes ? fmtBytes(op.bytes) : '', op.offset >= 0 ? `at ${fmtBytes(op.offset)}` : '', op.dial_ns ? `dial ${fmtDur(op.dial_ns)}` : ''].filter(Boolean).join(' · ') }),
      op.abandoned && h('div', { text: 'abandoned: the client closed it before the range was delivered' }),
      h('div', { text: where }),
      op.err && h('div', { class: 'tip-err', text: op.err }),
    ]);
    svg.append(g);
  });

  marks.forEach((m, k) => {
    const mx = x(m.at_ns);
    svg.append(s('line', { x1: mx, x2: mx, y1: bodyTop, y2: bodyBot, class: 'mark' }));
    const hit = s('rect', { x: mx - 5, y: 0, width: 10, height: bodyTop, class: 'mark-hit' });
    withTip(hit, () => [
      h('strong', { text: fmtDur(m.at_ns) }),
      h('div', { text: `${phaseLabel(target, k)} done` + (m.pts >= 0 ? ` · frame at ${m.pts.toFixed(2)} s` : '') }),
    ]);
    svg.append(hit);
  });
  return svg;
}

function opsTable(target, sample) {
  const ops = sample.timeline.ops || [];
  const phaseOf = opPhases(sample);
  const tail = phaseOf.size < ops.length ? unattributed(target, sample).split(':')[0] : '';
  const rows = ops.map((op, i) => h('tr', {},
    h('td', { text: i + 1 }),
    h('td', { class: 't-left', text: phaseOf.has(i) ? phaseLabel(target, phaseOf.get(i)) : tail }),
    h('td', { class: 't-left', text: op.kind }),
    h('td', { class: 't-left', text: op.ref || '' }),
    h('td', { text: fmtDur(op.start_ns) }),
    h('td', { text: op.first_byte_ns ? fmtDur(op.first_byte_ns - op.start_ns) : '—' }),
    h('td', { text: fmtDur(op.end_ns) }),
    h('td', { text: fmtBytes(op.bytes) }),
    h('td', { text: op.offset < 0 ? '—' : op.offset }),
    h('td', { text: op.dial_ns ? fmtDur(op.dial_ns) : '—' }),
    h('td', { class: 't-left', text: [op.abandoned && 'abandoned', op.err].filter(Boolean).join('; ') })));
  const head = ['#', 'phase', 'kind', 'ref', 'start', 'TTFB', 'end', 'bytes', 'offset', 'dial', 'note']
    .map((x, i) => h('th', { class: (i >= 1 && i <= 3) || i === 10 ? 't-left' : null, text: x }));
  return h('details', {}, h('summary', { text: 'Ops as a table' }),
    h('div', { class: 'table-wrap' },
      h('table', {}, h('thead', {}, h('tr', {}, head)), h('tbody', {}, rows))));
}

function trendsView() {
  const runs = (state.runs || []).filter((r) => !r.err);
  const sec = h('section', { id: 'trends', 'aria-labelledby': 'trends-h' },
    h('div', { class: 'section-head' }, h('h2', { id: 'trends-h', text: 'Trends' }),
      h('p', { text: 'p50 per layer in each run, oldest to newest. Click a run to open it.' })));
  if (runs.length < 2) {
    sec.append(h('p', { class: 'card sub', text: 'One run so far. Trends appear after the next run.' }));
    return sec;
  }
  const asc = runs.slice().reverse();
  const groups = new Map();
  const layers = new Set();
  runs.forEach((r) => {
    for (const p of r.points || []) {
      if (!groups.has(p.target)) groups.set(p.target, new Map());
      const byLabel = groups.get(p.target);
      if (!byLabel.has(p.label)) byLabel.set(p.label, new Map());
      const byLayer = byLabel.get(p.label);
      if (!byLayer.has(p.layer)) byLayer.set(p.layer, new Map());
      if (p.n > 0) byLayer.get(p.layer).set(r.id, p.p50_ns);
      layers.add(p.layer);
    }
  });
  const order = [...layers].sort((a, b) => stackRank(a) - stackRank(b));
  sec.append(h('div', { class: 'legend' },
    order.map((l) => h('span', {}, mini(16, s('line', { x1: 1, x2: 15, y1: 7, y2: 7, class: 'line s' + slotOf(l) })), l))));
  for (const [target, byLabel] of groups) {
    const facets = [...byLabel].map(([label, byLayer]) => {
      const series = order.filter((l) => byLayer.has(l))
        .map((l) => ({ layer: l, vals: asc.map((r) => (byLayer.get(l).has(r.id) ? byLayer.get(l).get(r.id) : null)) }));
      return h('div', { class: 'facet' }, h('h4', { text: label }),
        chart('trend', (w) => drawTrend(w, { target, label, runs: asc, series })));
    });
    sec.append(h('h3', { text: target }), h('div', { class: 'trend-grid' }, facets));
  }
  sec.append(trendsTable(runs, groups, order));
  return sec;
}

function drawTrend(w, f) {
  const H = 150;
  const M = { l: 44, r: 82, t: 8, b: 20 };
  const pw = Math.max(10, w - M.l - M.r);
  const ph = H - M.t - M.b;
  const ts = f.runs.map((r) => Date.parse(r.started));
  let t0 = ts[0];
  let t1 = ts[ts.length - 1];
  if (t1 === t0) {
    t0 -= 864e5;
    t1 += 864e5;
  }
  const vals = f.series.flatMap((sr) => sr.vals.filter((v) => v != null));
  const tk = ticks(Math.max(1e6, ...vals), 3);
  const top = tk[tk.length - 1];
  const x = (t) => M.l + (pw * (t - t0)) / (t1 - t0);
  const y = (v) => M.t + ph * (1 - v / top);
  const svg = s('svg', { width: w, height: H, role: 'group', 'aria-label': `${f.target} ${f.label}: p50 per layer over ${f.runs.length} runs` });
  tk.forEach((v, i) => {
    svg.append(s('line', { x1: M.l, x2: M.l + pw, y1: y(v), y2: y(v), class: i === 0 ? 'axis-line' : 'grid' }),
      s('text', { x: M.l - 6, y: y(v) + 4, 'text-anchor': 'end', class: 'tick' }, tickLabel(v, tk, true)));
  });
  svg.append(s('text', { x: M.l, y: H - 4, 'text-anchor': 'start', class: 'tick' }, fmtDay(ts[0])),
    s('text', { x: M.l + pw, y: H - 4, 'text-anchor': 'end', class: 'tick' }, fmtDay(ts[ts.length - 1])));
  const cur = f.runs.findIndex((r) => r.id === state.runId);
  if (cur >= 0) svg.append(s('line', { x1: x(ts[cur]), x2: x(ts[cur]), y1: M.t, y2: M.t + ph, class: 'sel-run' }));

  const ends = [];
  for (const sr of f.series) {
    const sl = slotOf(sr.layer);
    let d = '';
    let run = [];
    const flush = () => {
      if (run.length === 1) svg.append(s('circle', { cx: run[0][0], cy: run[0][1], r: 2.5, class: 'f' + sl }));
      else if (run.length > 1) d += 'M' + run.map((p) => p.join(' ')).join('L');
      run = [];
    };
    sr.vals.forEach((v, i) => (v == null ? flush() : run.push([x(ts[i]).toFixed(1), y(v).toFixed(1)])));
    flush();
    if (d) svg.append(s('path', { d, class: 'line s' + sl }));
    const li = sr.vals.findLastIndex((v) => v != null);
    if (li >= 0) ends.push({ layer: sr.layer, x: x(ts[li]), y: y(sr.vals[li]), ly: y(sr.vals[li]) });
    if (cur >= 0 && sr.vals[cur] != null) svg.append(s('circle', { cx: x(ts[cur]), cy: y(sr.vals[cur]), r: 4, class: 'pt f' + sl }));
  }
  ends.sort((a, b) => a.ly - b.ly);
  for (let i = 1; i < ends.length; i++) ends[i].ly = Math.max(ends[i].ly, ends[i - 1].ly + 12);
  for (let i = ends.length - 1; i >= 0; i--) ends[i].ly = Math.min(ends[i].ly, i === ends.length - 1 ? M.t + ph : ends[i + 1].ly - 12);
  const lx = M.l + pw + 12;
  for (const e of ends) {
    svg.append(s('line', { x1: e.x + 3, y1: e.y, x2: lx - 3, y2: e.ly, class: 'leader' }),
      s('text', { x: lx, y: e.ly + 4, class: 'end-label' }, e.layer));
  }

  const xh = s('line', { y1: M.t, y2: M.t + ph, class: 'xhair', visibility: 'hidden' });
  const dots = f.series.map((sr) => s('circle', { r: 4, class: 'pt f' + slotOf(sr.layer), visibility: 'hidden' }));
  svg.append(xh, ...dots);
  let at = -1;
  const show = (i) => {
    at = i;
    const px = x(ts[i]);
    xh.setAttribute('x1', px);
    xh.setAttribute('x2', px);
    xh.setAttribute('visibility', 'visible');
    f.series.forEach((sr, j) => {
      const v = sr.vals[i];
      dots[j].setAttribute('visibility', v == null ? 'hidden' : 'visible');
      if (v != null) {
        dots[j].setAttribute('cx', px);
        dots[j].setAttribute('cy', y(v));
      }
    });
  };
  const hide = () => {
    at = -1;
    xh.setAttribute('visibility', 'hidden');
    dots.forEach((d) => d.setAttribute('visibility', 'hidden'));
  };
  const overlay = s('rect', {
    x: M.l - 6, y: 0, width: pw + 12, height: H, class: 'overlay', tabindex: 0, role: 'button',
    'aria-label': `${f.target} ${f.label}: arrow keys pick a run, Enter opens it`,
  });
  overlay.addEventListener('pointermove', (e) => {
    const px = e.clientX - svg.getBoundingClientRect().left;
    let best = 0;
    ts.forEach((t, i) => {
      if (Math.abs(x(t) - px) < Math.abs(x(ts[best]) - px)) best = i;
    });
    show(best);
  });
  overlay.addEventListener('pointerleave', hide);
  overlay.addEventListener('blur', hide);
  overlay.addEventListener('click', () => at >= 0 && openRun(f.runs[at].id));
  overlay.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      e.preventDefault();
      const start = at >= 0 ? at : cur >= 0 ? cur : ts.length - 1;
      show(Math.min(ts.length - 1, Math.max(0, start + (e.key === 'ArrowRight' ? 1 : -1))));
      const r = svg.getBoundingClientRect();
      showTip(overlay, r.left + x(ts[at]), r.top + M.t);
    } else if (e.key === 'Enter' && at >= 0) {
      openRun(f.runs[at].id);
    }
  });
  withTip(overlay, () => {
    if (at < 0) return null;
    const r = f.runs[at];
    return [
      h('strong', { text: fmtWhen(r.started) }),
      f.series.map((sr) => h('div', { class: 'tip-row' },
        mini(14, s('line', { x1: 1, x2: 13, y1: 7, y2: 7, class: 'line s' + slotOf(sr.layer) })),
        h('b', { text: sr.vals[at] == null ? '—' : fmtDur(sr.vals[at]) }), sr.layer)),
      r.status !== 'done' && h('div', { text: r.status }),
      r.id !== state.runId && h('div', { class: 'tip-hint', text: 'Click to open this run' }),
    ];
  });
  svg.append(overlay);
  return svg;
}

function trendsTable(runs, groups, layers) {
  const rows = [];
  for (const r of runs) {
    for (const [target, byLabel] of groups) {
      for (const [label, byLayer] of byLabel) {
        rows.push(h('tr', {},
          h('td', { text: fmtWhen(r.started) }), h('td', { class: 't-left', text: target }), h('td', { class: 't-left', text: label }),
          layers.map((l) => {
            const v = byLayer.has(l) && byLayer.get(l).get(r.id);
            return h('td', { text: v ? fmtDur(v) : '—' });
          })));
      }
    }
  }
  return h('details', {}, h('summary', { text: 'Trends as a table' }),
    h('div', { class: 'table-wrap' }, h('table', {},
      h('thead', {}, h('tr', {}, ['run', 'target', 'phase', ...layers].map((x, i) => h('th', { class: i === 1 || i === 2 ? 't-left' : null, text: x })))),
      h('tbody', {}, rows))));
}

picker.addEventListener('change', () => openRun(picker.value));
new ResizeObserver(drawCharts).observe(main);
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && state.report && state.report.run.status === 'running') refresh();
});
readHash();
const landOnSample = Boolean(state.sel);
refresh().then(() => {
  const sec = landOnSample && state.sel && document.getElementById('sample');
  if (sec) sec.scrollIntoView({ block: 'start' });
});
