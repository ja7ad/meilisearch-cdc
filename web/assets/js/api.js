// REST client for /api/v1 (see docs/api.md).
import { lsGet, lsSet } from './util.js';

export const BASE = '/api/v1';
const TOKEN_KEY = 'cdc.adminToken';

export const getToken = () => lsGet(TOKEN_KEY, '') || '';
export function setToken(t) {
  lsSet(TOKEN_KEY, t ? t : null);
  window.dispatchEvent(new CustomEvent('cdc:token-changed'));
}

// ---------------------------------------------------------------- destination scope
const DEST_KEY = 'cdc.destination';
/** The Meilisearch destination the Indexes / Meilisearch pages work on ('' = the server default). */
export const getDestination = () => lsGet(DEST_KEY, '') || '';
/**
 * Selects the destination the index and Meilisearch pages use. `silent`
 * is for deep links applied before the page renders: listeners update, but
 * the page is not remounted (it already loads with the new destination).
 */
export function setDestination(id, { silent = false } = {}) {
  if (id === getDestination()) return;
  lsSet(DEST_KEY, id || null);
  window.dispatchEvent(new CustomEvent('cdc:destination', { detail: { id, silent } }));
}
const SCOPED = /^\/(indexes|meili|health)(\/|\?|$)/;
/** Adds ?destination=<selected> to index and Meilisearch endpoints unless the caller set one. */
function scoped(path) {
  const d = getDestination();
  if (!d || !SCOPED.test(path) || /[?&]destination=/.test(path)) return path;
  return `${path}${path.includes('?') ? '&' : '?'}destination=${encodeURIComponent(d)}`;
}

export class ApiError extends Error {
  constructor(status, message, details, retryAfter, data) {
    super(message);
    this.data = data || null;
    this.status = status;
    this.details = details || [];
    this.retryAfter = retryAfter || 0;
  }
}

// Short-lived cache for lists that change rarely (and only through this
// dashboard): repeated page visits and components asking at once share one
// request. Any write, sign-in change or 401 empties it.
const CACHE_TTL = 15000;
const CACHEABLE = /^\/(connectors(\/[^/?]+)?|destinations|alerts\/channels|auth\/config|users|invitations|settings\/(smtp|telegram|mcp))$/;
const cache = new Map();
export const clearApiCache = () => cache.clear();
window.addEventListener('cdc:token-changed', clearApiCache);
window.addEventListener('cdc:auth-required', clearApiCache);

/**
 * Fires "cdc:auth-required" on 401 so the shell returns to the login page
 * (except for the auth endpoints themselves, which pass {auth: false}).
 */
export function request(method, path, body, opts = {}) {
  if (method !== 'GET') {
    cache.clear();
    return send(method, path, body, opts);
  }
  if (!CACHEABLE.test(path) || opts.signal || opts.fresh) return send(method, path, body, opts);
  const key = `${getToken()}|${scoped(path)}`;
  const hit = cache.get(key);
  if (hit && Date.now() - hit.at < CACHE_TTL) return hit.p.then((d) => (d == null ? d : structuredClone(d)));
  const p = send(method, path, body, opts);
  const entry = { at: Date.now(), p };
  cache.set(key, entry);
  p.catch(() => { if (cache.get(key) === entry) cache.delete(key); });
  return p.then((d) => (d == null ? d : structuredClone(d)));
}

async function send(method, path, body, { signal, auth = true } = {}) {
  const headers = { Accept: 'application/json' };
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  let res;
  try {
    res = await fetch(BASE + scoped(path), { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal });
  } catch (e) {
    if (e.name === 'AbortError') throw e;
    throw new ApiError(0, 'Network error: the server is unreachable');
  }
  if (res.status === 401 && auth) {
    window.dispatchEvent(new CustomEvent('cdc:auth-required'));
    throw new ApiError(401, 'Your session has ended. Please sign in again.');
  }
  if (res.status === 204) return null;
  const text = await res.text();
  let data = null;
  if (text) { try { data = JSON.parse(text); } catch { data = text; } }
  if (!res.ok) {
    let msg = (data && data.error) || (typeof data === 'string' && data) || `${res.status} ${res.statusText}`;
    if (res.status === 403) msg = `Not allowed: ${msg.replace(/^requires role/, 'requires the')}${/role/.test(msg) ? ' role' : ''}`;
    throw new ApiError(res.status, msg, data && data.details, Number(res.headers.get('Retry-After')) || 0, data);
  }
  return data;
}

const enc = encodeURIComponent;
const qs = (o) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== null && v !== '') p.set(k, v);
  const s = p.toString();
  return s ? `?${s}` : '';
};

const pub = { auth: false };

export const api = {
  // accounts (public endpoints never trigger the global 401 handler)
  authConfig: () => request('GET', '/auth/config', undefined, pub),
  me: () => request('GET', '/auth/me', undefined, pub),
  setup: (b) => request('POST', '/auth/setup', b, pub),
  login: (email, password) => request('POST', '/auth/login', { email, password }, pub),
  logout: () => request('POST', '/auth/logout', undefined, pub),
  forgot: (email) => request('POST', '/auth/forgot', { email }, pub),
  reset: (token, password) => request('POST', '/auth/reset', { token, password }, pub),
  invitation: (token) => request('GET', `/auth/invite/${enc(token)}`, undefined, pub),
  acceptInvite: (token, name, password) => request('POST', '/auth/invite/accept', { token, name, password }, pub),
  updateMe: (name) => request('PATCH', '/auth/me', { name }),
  changePassword: (currentPassword, newPassword) => request('POST', '/auth/me/password', { currentPassword, newPassword }, pub),

  users: () => request('GET', '/users'),
  createUser: (u) => request('POST', '/users', u),
  updateUser: (id, patch) => request('PATCH', `/users/${enc(id)}`, patch),
  setUserPassword: (id, password) => request('POST', `/users/${enc(id)}/password`, { password }),
  deleteUser: (id) => request('DELETE', `/users/${enc(id)}`),
  invitations: () => request('GET', '/invitations'),
  invite: (email, role) => request('POST', '/invitations', { email, role }),
  revokeInvitation: (id) => request('DELETE', `/invitations/${enc(id)}`),

  smtp: () => request('GET', '/settings/smtp'),
  saveSmtp: (b) => request('PUT', '/settings/smtp', b),
  testSmtp: (to) => request('POST', '/settings/smtp/test', { to }),
  telegram: () => request('GET', '/settings/telegram'),
  saveTelegram: (b) => request('PUT', '/settings/telegram', b),
  testTelegram: (chatId) => request('POST', '/settings/telegram/test', { chatId }),
  mcp: () => request('GET', '/settings/mcp'),
  saveMcp: (b) => request('PUT', '/settings/mcp', b),
  resetMcpPolicy: () => request('POST', '/settings/mcp/reset-policy'),
  mcpTokens: () => request('GET', '/mcp/tokens'),
  createMcpToken: (b) => request('POST', '/mcp/tokens', b),
  rotateMcpToken: (id) => request('POST', `/mcp/tokens/${enc(id)}/rotate`),
  revokeMcpToken: (id) => request('DELETE', `/mcp/tokens/${enc(id)}`),
  audit: (limit = 200) => request('GET', `/audit?limit=${limit}`),

  alertRules: () => request('GET', '/alerts/rules'),
  alertChannels: () => request('GET', '/alerts/channels'),
  createRule: (r) => request('POST', '/alerts/rules', r),
  updateRule: (id, r) => request('PUT', `/alerts/rules/${enc(id)}`, r),
  deleteRule: (id) => request('DELETE', `/alerts/rules/${enc(id)}`),
  testRule: (id) => request('POST', `/alerts/rules/${enc(id)}/test`),
  alertEvents: (f = {}) => request('GET', `/alerts/events${qs({ limit: 100, ...f })}`),
  activeAlerts: () => request('GET', '/alerts/active'),

  connectors: () => request('GET', '/connectors'),
  connector: (id) => request('GET', `/connectors/${enc(id)}`),
  createConnector: (c) => request('POST', '/connectors', c),
  updateConnector: (id, c) => request('PUT', `/connectors/${enc(id)}`, c),
  deleteConnector: (id) => request('DELETE', `/connectors/${enc(id)}`),
  testConnectorDraft: (c) => request('POST', '/connectors/test', c),
  testConnector: (id) => request('POST', `/connectors/${enc(id)}/test`),
  connectorTables: (id, q = '', opts) => request('GET', `/connectors/${enc(id)}/tables${qs({ q })}`, undefined, opts),

  destinations: () => request('GET', '/destinations'),
  destination: (id) => request('GET', `/destinations/${enc(id)}`),
  createDestination: (d) => request('POST', '/destinations', d),
  updateDestination: (id, d) => request('PUT', `/destinations/${enc(id)}`, d),
  deleteDestination: (id) => request('DELETE', `/destinations/${enc(id)}`),
  makeDefaultDestination: (id) => request('POST', `/destinations/${enc(id)}/default`),
  testDestinationDraft: (d) => request('POST', '/destinations/test', d),
  testDestination: (id) => request('POST', `/destinations/${enc(id)}/test`),
  checkDestination: (id) => request('POST', `/destinations/${enc(id)}/health/check`),
  /** Index list of an explicit destination (ignores the global switcher). */
  indexesOf: (destination) => request('GET', `/indexes${qs({ destination })}`),
  taskSettings: (id) => request('GET', `/tasks/${enc(id)}/settings`),
  applyTaskSettings: (id) => request('POST', `/tasks/${enc(id)}/settings/apply`),

  health: () => request('GET', '/health'),
  overview: () => request('GET', '/overview'),

  tasks: (f = {}) => request('GET', `/tasks${qs(f)}`),
  task: (id) => request('GET', `/tasks/${enc(id)}`),
  createTask: (t, start = false) => request('POST', `/tasks${start ? '?start=true' : ''}`, t),
  updateTask: (id, t) => request('PUT', `/tasks/${enc(id)}`, t),
  deleteTask: (id, { dropSource, deleteIndex } = {}) =>
    request('DELETE', `/tasks/${enc(id)}${qs({ dropSource: dropSource ? 'true' : '', deleteIndex })}`),
  taskAction: (id, action) => request('POST', `/tasks/${enc(id)}/${action}`),
  batch: (ids, action) => request('POST', '/tasks/batch', { ids, action }),
  testTask: (t) => request('POST', '/tasks/test', t),
  exportTasks: () => request('GET', '/tasks/export'),
  importTasks: (list) => request('POST', '/tasks/import', list),
  logs: (id, limit = 200) => request('GET', `/tasks/${enc(id)}/logs?limit=${limit}`),
  metrics: (id) => request('GET', `/tasks/${enc(id)}/metrics`),
  schemaCheck: (id) => request('GET', `/tasks/${enc(id)}/schema-check`),
  runDiff: (id, opts) => request('POST', `/tasks/${enc(id)}/diff`, opts),
  diff: (id) => request('GET', `/tasks/${enc(id)}/diff`),
  repair: (id, opts) => request('POST', `/tasks/${enc(id)}/diff/repair`, opts),
  dlq: (id, limit = 100) => request('GET', `/tasks/${enc(id)}/dlq?limit=${limit}`),
  dlqReplay: (id, ids) => request('POST', `/tasks/${enc(id)}/dlq/replay`, { ids }),
  dlqDelete: (id, ids) => request('DELETE', `/tasks/${enc(id)}/dlq`, { ids }),

  indexes: () => request('GET', '/indexes'),
  index: (uid) => request('GET', `/indexes/${enc(uid)}`),
  /** Index groups of every Meilisearch instance: [{destinationId, destinationName, isDefault, healthy, error, indexes}] */
  indexesAll: () => request('GET', '/indexes?destination=*'),
  createIndex: (uid, primaryKey, destination) => request('POST', `/indexes${qs({ destination })}`, { uid, primaryKey: primaryKey || undefined }),
  deleteIndex: (uid, destination) => request('DELETE', `/indexes/${enc(uid)}${qs({ confirm: uid, destination })}`),
  settings: (uid) => request('GET', `/indexes/${enc(uid)}/settings`),
  previewSettings: (uid, s) => request('POST', `/indexes/${enc(uid)}/settings/preview`, s),
  applySettings: (uid, s) => request('PATCH', `/indexes/${enc(uid)}/settings`, s),
  resetSettings: (uid) => request('POST', `/indexes/${enc(uid)}/settings/reset`),
  setPrimaryKey: (uid, primaryKey) => request('PUT', `/indexes/${enc(uid)}/primary-key`, { primaryKey }),
  search: (uid, body, opts) => request('POST', `/indexes/${enc(uid)}/search`, body, opts),
  documents: (uid, limit, offset) => request('GET', `/indexes/${enc(uid)}/documents?limit=${limit}&offset=${offset}`),
  swap: (a, b, rename = false, destination) => request('POST', `/indexes/swap${qs({ destination })}`, { a, b, rename }),
  compact: (uid) => request('POST', `/indexes/${enc(uid)}/compact`),

  meiliTasks: (f = {}) => request('GET', `/meili/tasks${qs(f)}`),
  // instance endpoints: `destination` overrides the switcher (always passed on destination pages)
  cancelMeiliTasks: (uids, destination) => request('POST', `/meili/tasks/cancel${qs({ destination })}`, { uids }),
  meiliStats: (destination) => request('GET', `/meili/stats${qs({ destination })}`),
  dump: (destination) => request('POST', `/meili/dumps${qs({ destination })}`),
  snapshot: (destination) => request('POST', `/meili/snapshots${qs({ destination })}`),
  experimental: (destination) => request('GET', `/meili/experimental-features${qs({ destination })}`),
  setExperimental: (f, destination) => request('PATCH', `/meili/experimental-features${qs({ destination })}`, f),
};
