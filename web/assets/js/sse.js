// Live store fed by GET /api/v1/events (Server-Sent Events).
//
// Keeps the latest Status per task, a short rate/lag history per task for
// sparklines, the latest overview with its throughput history, and a ring
// buffer of log entries. Components subscribe with useLive().
import { useEffect, useState } from '../vendor/htm-preact.js';
import { BASE, getToken } from './api.js';

const HIST = 180; // points kept per series (~3-6 min)
const LOGS = 300;

export const live = {
  conn: 'connecting', // connecting | live | down
  overview: null,
  overviewHist: [], // [{t, rate}]
  status: {}, // taskId -> Status
  hist: {}, // taskId -> [{t, rate, lagMs}]
  logs: [], // LogEntry, newest last
  logSeq: 0,
  taskSeq: 0, // bumps on "task" events so lists can refetch
  lastTaskEvent: null,
  active: {}, // "ruleId|taskId" -> firing AlertEvent
  destHealth: {}, // destination id -> latest Health from "destination.health"
  destSeq: 0,
  alertSeq: 0, // bumps on every "alert" event
};

const alertKey = (e) => `${e.ruleId}|${e.taskId || ''}`;
export const activeAlerts = () => Object.values(live.active).sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
/** Replace the firing set from GET /alerts/active. */
export function seedActiveAlerts(list) {
  live.active = {};
  for (const e of list || []) live.active[alertKey(e)] = e;
  live.alertSeq++;
  notify();
}

const subs = new Set();
let scheduled = false;
function notify() {
  if (scheduled) return;
  scheduled = true;
  // Coalesce bursts of events into one render per frame (timeout fallback when the tab is hidden).
  const run = () => { if (!scheduled) return; scheduled = false; subs.forEach((f) => f()); };
  requestAnimationFrame(run);
  setTimeout(run, 250);
}
export const subscribe = (f) => { subs.add(f); return () => subs.delete(f); };

function push(arr, v, max) { arr.push(v); if (arr.length > max) arr.splice(0, arr.length - max); }

export function recordStatus(st) {
  if (!st || !st.taskId) return;
  live.status[st.taskId] = st;
  const h = (live.hist[st.taskId] ||= []);
  const t = Date.now();
  // throttle history to 1 point / 900ms per task
  if (!h.length || t - h[h.length - 1].t > 900) push(h, { t, rate: st.rate || 0, lagMs: st.lagMs || 0 }, HIST);
}

/** Seed per-task history from GET /tasks/{id}/metrics so sparklines are not empty on load. */
export function seedHistory(taskId, points) {
  if (!points || !points.length) return;
  const cur = live.hist[taskId] || [];
  const first = cur.length ? cur[0].t : Infinity;
  const older = points.filter((p) => p.t < first).map((p) => ({ t: p.t, rate: p.rate, lagMs: p.lagMs }));
  live.hist[taskId] = [...older, ...cur].slice(-HIST);
  notify();
}
export function seedOverviewHist(points) {
  const first = live.overviewHist.length ? live.overviewHist[0].t : Infinity;
  live.overviewHist = [...points.filter((p) => p.t < first), ...live.overviewHist].slice(-HIST);
  notify();
}

let es = null;
let retry = 0;
let retryTimer = null;

export function connect() {
  if (es) es.close();
  clearTimeout(retryTimer);
  const token = getToken();
  live.conn = 'connecting';
  notify();
  es = new EventSource(`${BASE}/events${token ? `?token=${encodeURIComponent(token)}` : ''}`);
  es.onopen = () => { retry = 0; live.conn = 'live'; notify(); };
  es.onerror = () => {
    live.conn = 'down';
    notify();
    if (es && es.readyState === EventSource.CLOSED) {
      // The browser gave up (e.g. 401/500): reconnect with backoff.
      es = null;
      retryTimer = setTimeout(connect, Math.min(15000, 1000 * 2 ** retry++));
    }
  };
  const on = (name, fn) => es.addEventListener(name, (e) => {
    let data;
    try { data = JSON.parse(e.data); } catch { return; }
    if (live.conn !== 'live') live.conn = 'live';
    fn(data);
    notify();
  });
  on('status', recordStatus);
  on('overview', (o) => {
    live.overview = o;
    const t = Date.now();
    const h = live.overviewHist;
    if (!h.length || t - h[h.length - 1].t > 900) push(h, { t, rate: o.rate || 0 }, HIST);
  });
  on('log', (l) => { push(live.logs, l, LOGS); live.logSeq++; });
  on('task', (ev) => {
    live.taskSeq++;
    live.lastTaskEvent = ev;
    // Drop the live state of a deleted task so it does not stay in memory.
    if (ev && ev.action === 'deleted' && ev.id) { delete live.status[ev.id]; delete live.hist[ev.id]; }
  });
  on('destination.health', (ev) => {
    if (!ev || !ev.id) return;
    live.destHealth[ev.id] = ev.health;
    live.destSeq++;
    window.dispatchEvent(new CustomEvent('cdc:dest-health', { detail: ev }));
  });
  on('alert', (e) => {
    if (e.state === 'firing') live.active[alertKey(e)] = e; else delete live.active[alertKey(e)];
    live.alertSeq++;
    window.dispatchEvent(new CustomEvent('cdc:alert', { detail: e }));
  });
}

export function disconnect() {
  clearTimeout(retryTimer);
  if (es) { es.close(); es = null; }
  live.conn = 'down';
  live.active = {};
  notify();
}

window.addEventListener('cdc:token-changed', () => { if (es) connect(); });

/** Re-render on every live update; returns the live store. */
export function useLive() {
  const [, set] = useState(0);
  useEffect(() => subscribe(() => set((n) => n + 1)), []);
  return live;
}

/** Merge live status over a TaskView's embedded status. */
export const statusOf = (task) => (task && live.status[task.id]) || (task && task.status) || null;
