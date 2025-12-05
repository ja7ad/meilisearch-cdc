// Formatting and small helpers shared by every page.

export const cls = (...xs) => xs.filter(Boolean).join(' ');

export const reducedMotion = () =>
  typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;

const nf0 = new Intl.NumberFormat('en', { maximumFractionDigits: 0 });
const nfc = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 });

/** 1234567 -> "1,234,567" */
export function fmtInt(n) {
  if (n == null || Number.isNaN(n)) return '—';
  return nf0.format(Math.round(n));
}
/** 1234567 -> "1.2M" (exact below 10k) */
export function fmtCompact(n) {
  if (n == null || Number.isNaN(n)) return '—';
  return Math.abs(n) < 10000 ? nf0.format(Math.round(n)) : nfc.format(n);
}
/** documents per second */
export function fmtRate(r) {
  if (r == null || Number.isNaN(r)) return '—';
  if (r >= 10000) return nfc.format(r);
  if (r >= 100) return nf0.format(r);
  return (Math.round(r * 10) / 10).toString();
}
export function fmtBytes(b) {
  if (b == null || Number.isNaN(b)) return '—';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return `${b >= 100 || i === 0 ? Math.round(b) : b.toFixed(1)} ${u[i]}`;
}
export function fmtLag(ms) {
  if (ms == null || Number.isNaN(ms)) return '—';
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)} s`;
  if (ms < 3600000) return `${Math.floor(ms / 60000)}m ${Math.round((ms % 60000) / 1000)}s`;
  return `${(ms / 3600000).toFixed(1)} h`;
}
export const fmtPct = (x, digits = 0) => (x == null ? '—' : `${(x * 100).toFixed(digits)}%`);

/** Go encodes zero time.Time as 0001-01-01T00:00:00Z even with omitempty. */
export const isZeroTime = (t) => !t || String(t).startsWith('0001-');

export function timeAgo(t, now = Date.now()) {
  if (isZeroTime(t)) return '—';
  const d = Math.max(0, now - Date.parse(t)) / 1000;
  if (d < 5) return 'just now';
  if (d < 60) return `${Math.floor(d)}s ago`;
  if (d < 3600) return `${Math.floor(d / 60)}m ago`;
  if (d < 86400) return `${Math.floor(d / 3600)}h ago`;
  return `${Math.floor(d / 86400)}d ago`;
}
export function fmtTime(t, withDate = false) {
  if (isZeroTime(t)) return '—';
  const d = new Date(t);
  return withDate
    ? d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })
    : d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}
/** ISO-8601 duration from Meilisearch ("PT0.412S") -> "412 ms" */
export function fmtIsoDuration(d) {
  if (!d) return '—';
  const m = /^P(?:(\d+)D)?T?(?:(\d+)H)?(?:(\d+)M)?(?:([\d.]+)S)?$/.exec(d);
  if (!m) return d;
  const s = (+m[1] || 0) * 86400 + (+m[2] || 0) * 3600 + (+m[3] || 0) * 60 + (+m[4] || 0);
  return fmtLag(s * 1000);
}

/** Go time.ParseDuration syntax, e.g. "1s", "250ms", "1m30s". */
export const DURATION_RE = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;
export const isDuration = (s) => !s || DURATION_RE.test(String(s).trim());

export function debounce(fn, ms) {
  let t;
  const d = (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
  d.cancel = () => clearTimeout(t);
  return d;
}

export function downloadJSON(name, data) {
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

export async function copyText(text) {
  try { await navigator.clipboard.writeText(text); return true; } catch { return false; }
}

export const pretty = (v) => JSON.stringify(v, null, 2);

export function lsGet(k, def = null) {
  try { const v = localStorage.getItem(k); return v == null ? def : v; } catch { return def; }
}
export function lsSet(k, v) {
  try { v == null ? localStorage.removeItem(k) : localStorage.setItem(k, v); } catch { /* storage unavailable */ }
}

// ---------------------------------------------------------------- domain

/** tone drives the color of badges, dots and progress bars */
export const STATES = {
  streaming: { label: 'Streaming', tone: 'ok', live: true },
  snapshotting: { label: 'Snapshotting', tone: 'sync', live: true },
  resyncing: { label: 'Resyncing', tone: 'sync', live: true },
  starting: { label: 'Starting', tone: 'sync', live: true },
  retrying: { label: 'Retrying', tone: 'warn', live: true },
  failed: { label: 'Failed', tone: 'bad' },
  held: { label: 'Held', tone: 'bad' },
  paused: { label: 'Paused', tone: 'muted' },
  stopped: { label: 'Stopped', tone: 'muted' },
  completed: { label: 'Completed', tone: 'done' },
  running: { label: 'Running', tone: 'sync' },
};
export const stateInfo = (s) => STATES[s] || { label: s || 'unknown', tone: 'muted' };
export const isActive = (s) => ['starting', 'snapshotting', 'streaming', 'resyncing', 'retrying'].includes(s);

/** Actions offered for a state, in display order. */
/** Actions for a task state; resync needs a table to copy (not stream only, not pushed). */
export function actionsFor(state, task) {
  const noResync = task && ((task.sync && task.sync.mode === 'stream') || (task.source && task.source.type === 'webhook'));
  const list = state === 'paused' ? ['resume', 'stop', 'restart']
    : isActive(state) ? ['pause', 'stop', 'restart', 'resync'] : ['start', 'resync', 'reset-checkpoint'];
  return noResync ? list.filter((a) => a !== 'resync') : list;
}
export const ACTION_LABELS = {
  start: 'Start', stop: 'Stop', pause: 'Pause', resume: 'Resume', restart: 'Restart',
  resync: 'Resync', 'reset-checkpoint': 'Reset checkpoint', delete: 'Delete',
};

export const SOURCES = [
  { type: 'postgres', label: 'PostgreSQL', icon: 'elephant', desc: 'Logical replication with pgoutput. Snapshot then stream WAL changes.', hue: '#336791' },
  { type: 'mysql', label: 'MySQL', icon: 'dolphin', desc: 'Row-based binlog replication as a replica server.', hue: '#00758F' },
  { type: 'mariadb', label: 'MariaDB', icon: 'seal', desc: 'Binlog replication with MariaDB GTIDs.', hue: '#A8724B' },
  { type: 'mongodb', label: 'MongoDB', icon: 'leaf', desc: 'Change streams on a replica set or sharded cluster.', hue: '#13AA52' },
  { type: 'dynamodb', label: 'DynamoDB', icon: 'bolt', desc: 'DynamoDB Streams with a full table scan for the snapshot.', hue: '#4053D6' },
  { type: 'kafka', label: 'Kafka · Debezium', icon: 'stream', desc: 'Consume Debezium change events: SQL Server, Oracle, Db2 and more.', hue: '#5B5B5B' },
  { type: 'couchdb', label: 'CouchDB', icon: 'couch', desc: 'Follow the _changes feed of CouchDB or Cloudant.', hue: '#E42528' },
  { type: 'sqlserver', label: 'SQL Server', icon: 'server', desc: 'Native SQL Server CDC change tables.', hue: '#A91D22' },
  { type: 'cockroachdb', label: 'CockroachDB', icon: 'roach', desc: 'Changefeeds with resolved timestamps.', hue: '#6933FF' },
  { type: 'redis', label: 'Redis Streams', icon: 'cube', desc: 'Consume change entries from a Redis stream.', hue: '#DC382D' },
  { type: 'webhook', label: 'Webhook', icon: 'webhook', desc: 'Your apps push changes over HTTP; replies once indexed.', hue: '#FF5CAA' },
  { type: 'sqlpoll', label: 'SQL polling', icon: 'poll', desc: 'Poll any SQL database with an incrementing cursor column.', hue: '#8E5CC4' },
];
export const sourceInfo = (t) => SOURCES.find((s) => s.type === t) || { type: t, label: t, icon: 'database', hue: '#888' };
