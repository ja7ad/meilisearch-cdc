// Mock of the "Accounts, roles and notifications" and "MCP server" parts of
// docs/api.md: users, sessions (cookie), invitations, password reset, SMTP,
// Telegram, alert rules/events, audit log, MCP settings and tokens.
import crypto from 'node:crypto';

export const ROLE_RANK = { viewer: 0, moderator: 1, admin: 2 };
const MASK = '••••••';
const ZERO_TIME = '0001-01-01T00:00:00Z';
const iso = (ms = Date.now()) => new Date(ms).toISOString();
const rand = (n = 18) => crypto.randomBytes(n).toString('base64url');
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const DURATION_RE = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;

const MCP_TOOLS = [
  ['overview', 'Overview', 'Task counts, throughput and Meilisearch health.', 'viewer', false],
  ['list_tasks', 'List tasks', 'Every task with its live status.', 'viewer', false],
  ['get_task', 'Get task', 'One task definition and status.', 'viewer', false],
  ['get_task_logs', 'Task logs', 'Recent log lines of a task.', 'viewer', false],
  ['schema_check', 'Schema check', 'Compare source columns with index fields.', 'viewer', false],
  ['get_diff', 'Get diff', 'Last source/index diff report.', 'viewer', false],
  ['list_dlq', 'List dead letters', 'Documents Meilisearch rejected.', 'viewer', false],
  ['list_indexes', 'List indexes', 'Meilisearch indexes and their size.', 'viewer', false],
  ['get_index', 'Get index', 'Stats of one index.', 'viewer', false],
  ['search_index', 'Search index', 'Run a search query.', 'viewer', false],
  ['get_index_settings', 'Index settings', 'Current settings of an index.', 'viewer', false],
  ['preview_index_settings', 'Preview settings', 'Diff desired settings against the index.', 'viewer', false],
  ['list_meili_tasks', 'Meilisearch tasks', 'The Meilisearch task queue.', 'viewer', false],
  ['list_alert_rules', 'List alert rules', 'Configured alert rules.', 'viewer', false],
  ['list_alert_events', 'Alert events', 'Recent firing/resolved events.', 'viewer', false],
  ['list_destinations', 'List destinations', 'Meilisearch instances and their health.', 'viewer', false],
  ['task_action', 'Task action', 'Start, stop, pause, resume, restart, resync or reset a task.', 'moderator', true],
  ['batch_task_action', 'Batch task action', 'Run an action on several tasks.', 'moderator', true],
  ['test_connection', 'Test connection', 'Connect to a source and sample rows.', 'moderator', false],
  ['run_diff', 'Run diff', 'Start a source/index comparison.', 'moderator', true],
  ['replay_dlq', 'Replay dead letters', 'Send rejected documents again.', 'moderator', true],
  ['create_task', 'Create task', 'Define a new CDC task.', 'admin', true],
  ['update_task', 'Update task', 'Change a task definition.', 'admin', true],
  ['delete_task', 'Delete task', 'Remove a task.', 'admin', true],
  ['update_index_settings', 'Update index settings', 'Apply settings to an index.', 'admin', true],
  ['create_alert_rule', 'Create alert rule', 'Add an alert rule.', 'admin', true],
  ['delete_alert_rule', 'Delete alert rule', 'Remove an alert rule.', 'admin', true],
].map(([name, title, description, minRole, mutating]) => ({ name, title, description, minRole, mutating }));
const MCP_RESOURCES = [
  ['cdc://overview', 'Overview', 'Live overview counters.', 'viewer'],
  ['cdc://tasks', 'Tasks', 'All task definitions and statuses.', 'viewer'],
  ['cdc://tasks/{id}', 'Task', 'One task by id.', 'viewer'],
  ['cdc://tasks/{id}/logs', 'Task logs', 'Recent logs of a task.', 'viewer'],
  ['cdc://indexes', 'Indexes', 'Meilisearch indexes.', 'viewer'],
  ['cdc://alerts/active', 'Active alerts', 'Alerts currently firing.', 'viewer'],
  ['cdc://destinations', 'Destinations', 'Meilisearch instances.', 'viewer'],
  ['cdc://docs/api', 'API reference', 'The REST API documentation.', 'viewer'],
].map(([uri, name, description, minRole]) => ({ uri, name, description, minRole }));

function defaultPolicy() {
  const p = {};
  for (const role of Object.keys(ROLE_RANK)) {
    p[role] = {
      tools: MCP_TOOLS.filter((t) => ROLE_RANK[t.minRole] <= ROLE_RANK[role]).map((t) => t.name),
      resources: MCP_RESOURCES.filter((r) => ROLE_RANK[r.minRole] <= ROLE_RANK[role]).map((r) => r.uri),
    };
  }
  return p;
}

export function createAccounts({ send, err, readBody, broadcast, tasks, adminToken, extraConfig = () => ({}) }) {
  let A;

  function init({ empty = false } = {}) {
    const now = Date.now();
    const mk = (id, email, name, role, lastAgo) => ({
      id, email, name, role, password: 'password123', createdAt: iso(now - 40 * 86400e3), updatedAt: iso(now - 2 * 86400e3),
      lastLoginAt: lastAgo == null ? ZERO_TIME : iso(now - lastAgo),
    });
    const users = empty ? [] : [
      { ...mk('u_admin', 'admin@example.com', 'Ada Admin', 'admin', 3600e3), owner: true },
      mk('u_mod', 'mod@example.com', 'Max Moderator', 'moderator', 26 * 3600e3),
      mk('u_view', 'viewer@example.com', 'Vera Viewer', 'viewer', null),
    ];
    const rules = [
      {
        id: 'r_failed', name: 'Task failed or held', enabled: true,
        condition: { type: 'task_state', states: ['failed', 'held'], for: '1m' }, taskIds: [], severity: 'critical',
        channels: { emails: ['ops@example.com', 'oncall@example.com'], telegramChatIds: ['-1001234567890'], webhookUrl: '' },
        cooldown: '30m', notifyResolved: true, createdAt: iso(now - 20 * 86400e3), updatedAt: iso(now - 5 * 86400e3),
      },
      {
        id: 'r_lag', name: 'Catalog lag over 1 minute', enabled: true,
        condition: { type: 'lag', thresholdMs: 60000, for: '2m' }, taskIds: ['products-pg'], severity: 'warning',
        channels: { emails: ['catalog@example.com'], telegramChatIds: [], webhookUrl: '' },
        cooldown: '15m', notifyResolved: true, createdAt: iso(now - 15 * 86400e3), updatedAt: iso(now - 15 * 86400e3),
      },
      {
        id: 'r_dlq', name: 'New dead letters', enabled: false,
        condition: { type: 'dlq', count: 1, window: '5m', for: '0s' }, taskIds: [], severity: 'info',
        channels: { emails: [], telegramChatIds: [], webhookUrl: 'https://hooks.example.com/cdc' },
        cooldown: '1h', notifyResolved: false, createdAt: iso(now - 9 * 86400e3), updatedAt: iso(now - 9 * 86400e3),
      },
    ];
    rules.push({
      id: 'r_meili', name: 'Meilisearch unreachable', enabled: true, condition: { type: 'meili_down', for: '1m' }, taskIds: [], destinationIds: ['default', 'eu-replica'], severity: 'critical',
      channels: { emails: ['ops@example.com'], telegramChatIds: [], webhookUrl: '' }, cooldown: '10m', notifyResolved: true, createdAt: iso(now - 6 * 86400e3), updatedAt: iso(now - 6 * 86400e3),
    });
    const ev = (id, rule, taskId, state, agoMs, message, deliveries) => ({
      id, ruleId: rule.id, ruleName: rule.name, severity: rule.severity, taskId, taskName: (tasks().find((t) => t.id === taskId) || {}).name || '',
      state, message, at: iso(now - agoMs), deliveries: deliveries.map((d) => ({ ...d, at: iso(now - agoMs + 800) })),
    });
    const okDel = [{ channel: 'email', target: 'ops@example.com', ok: true, error: '' }, { channel: 'email', target: 'oncall@example.com', ok: true, error: '' }, { channel: 'telegram', target: '-1001234567890', ok: true, error: '' }];
    const events = [
      ev('e6', rules[0], 'articles-maria', 'firing', 2400e3, 'Blog articles is held: delete ratio 0.38 exceeds maxDeleteRatio 0.20', okDel),
      ev('e5', rules[0], 'stock-dynamo', 'firing', 7200e3, 'Warehouse stock is failed: ResourceNotFoundException', [okDel[0], okDel[1], { channel: 'telegram', target: '-1001234567890', ok: false, error: 'Forbidden: bot was kicked from the group chat' }]),
      ev('e4', rules[1], 'products-pg', 'resolved', 3 * 3600e3, 'Products catalog lag is back to 210 ms', [{ channel: 'email', target: 'catalog@example.com', ok: true, error: '' }]),
      ev('e3', rules[1], 'products-pg', 'firing', 3.2 * 3600e3, 'Products catalog lag is 74.2 s (threshold 1m0s) for 2m', [{ channel: 'email', target: 'catalog@example.com', ok: true, error: '' }]),
      ev('e2', rules[0], 'orders-mysql', 'resolved', 26 * 3600e3, 'Orders is streaming again', okDel),
      ev('e1', rules[0], 'orders-mysql', 'firing', 27 * 3600e3, 'Orders is failed: dial tcp 10.0.0.7:3306: i/o timeout', okDel),
      { ...ev('e0', rules[3], '', 'resolved', 40 * 3600e3, 'EU replica is reachable again', [{ channel: 'email', target: 'ops@example.com', ok: true, error: '' }]), destinationId: 'eu-replica', destinationName: 'EU replica' },
    ];
    const au = (agoMs, userEmail, action, target, details = '') => ({ at: iso(now - agoMs), userId: users.find((u) => u.email === userEmail)?.id || '', userEmail, action, target, details, ip: '10.0.4.21' });
    const audit = empty ? [] : [
      au(3600e3, 'admin@example.com', 'login', 'admin@example.com'),
      au(4000e3, 'mod@example.com', 'task.action', 'events-kafka', 'restart'),
      au(5 * 3600e3, 'admin@example.com', 'index.settings', 'products', 'filterableAttributes'),
      au(26 * 3600e3, 'admin@example.com', 'user.create', 'viewer@example.com', 'role viewer'),
      au(27 * 3600e3, '', 'login.failed', 'root@example.com', 'invalid email or password'),
      au(30 * 3600e3, 'admin@example.com', 'rule.update', 'Task failed or held'),
    ];
    const tok = (id, name, role, createdBy, ageD, lastAgo, expD) => ({
      id, name, role, prefix: `cdcmcp_${id.slice(-4)}`, createdAt: iso(now - ageD * 86400e3), createdBy,
      lastUsedAt: lastAgo == null ? ZERO_TIME : iso(now - lastAgo), expiresAt: expD == null ? ZERO_TIME : iso(now + expD * 86400e3),
      secret: `cdcmcp_${id.slice(-4)}${rand(12)}`,
    });
    A = {
      users, rules, events, audit,
      sessions: new Map(), invitations: [], resets: new Map(), failures: new Map(), outbox: [],
      smtp: { host: '', port: 587, username: '', password: '', from: '', tls: 'auto', publicUrl: '' },
      telegram: { botToken: '', apiUrl: 'https://api.telegram.org', botName: '' },
      mcp: {
        enabled: false, policy: defaultPolicy(),
        tokens: empty ? [] : [tok('t_9f2a', 'Claude Desktop (ops)', 'moderator', 'admin@example.com', 12, 5 * 60e3, null), tok('t_41bc', 'CI read-only', 'viewer', 'admin@example.com', 3, null, 27)],
      },
    };
  }
  init();

  const pub = (u) => u && { id: u.id, email: u.email, name: u.name, role: u.role, createdAt: u.createdAt, updatedAt: u.updatedAt, lastLoginAt: u.lastLoginAt, ...(u.owner ? { owner: true } : {}) };
  // Primary administrator rules (auth service): only the operator CLI changes the owner.
  const OWNER_HINT = 'transfer ownership first with `meilisearch-cdc admin users set-owner`';
  const API_USER = { id: 'api-token', email: '', name: 'API token', role: 'admin' };
  const cookies = (req) => Object.fromEntries((req.headers.cookie || '').split(';').map((c) => c.trim().split('=')).filter((x) => x[0]));
  const setCookie = (res, token) => res.setHeader('Set-Cookie', token ? `cdc_session=${token}; Path=/; HttpOnly; SameSite=Lax; Max-Age=604800` : 'cdc_session=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0');
  function newSession(res, user) {
    const t = rand();
    A.sessions.set(t, user.id);
    user.lastLoginAt = iso();
    setCookie(res, t);
  }
  const revokeSessions = (userId, keep) => { for (const [t, id] of A.sessions) if (id === userId && t !== keep) A.sessions.delete(t); };
  function audit(user, action, target, details = '', req) {
    A.audit.unshift({ at: iso(), userId: user ? user.id : '', userEmail: user ? (user.email || user.name) : '', action, target, details, ip: (req && req.socket.remoteAddress) || '' });
    if (A.audit.length > 500) A.audit.pop();
  }

  /** Who is calling: {user} or null. */
  function authenticate(req, url, isEvents) {
    const h = req.headers.authorization || '';
    if (adminToken && (h === `Bearer ${adminToken}` || (isEvents && url.searchParams.get('token') === adminToken))) return API_USER;
    const sid = cookies(req).cdc_session;
    const uid = sid && A.sessions.get(sid);
    return (uid && A.users.find((u) => u.id === uid)) || null;
  }

  const smtpConfigured = () => !!A.smtp.host;
  const telegramConfigured = () => !!A.telegram.botToken;
  const origin = (req) => `http://${req.headers.host || 'localhost'}`;
  function mail(to, subject, link) { A.outbox.push({ to, subject, link, at: iso() }); }
  const pwErr = (p) => (typeof p !== 'string' || p.length < 8 || p.length > 128 ? 'password must be 8-128 characters' : '');

  // ---------------------------------------------------------------- public auth endpoints
  async function publicAuth(req, res, parts, M) {
    const [, b, c] = parts;
    if (b === 'config' && M === 'GET') return send(res, 200, { setupRequired: A.users.length === 0, smtpConfigured: smtpConfigured(), telegramConfigured: telegramConfigured(), passwordLogin: true, version: "v1.0.0", ...extraConfig() }), true;
    if (b === 'setup' && M === 'POST') {
      if (A.users.length) return err(res, 409, 'setup is already complete'), true;
      const x = await readBody(req) || {};
      if (!EMAIL_RE.test(x.email || '')) return err(res, 400, 'a valid email is required'), true;
      if (pwErr(x.password)) return err(res, 400, pwErr(x.password)), true;
      const u = { id: `u_${rand(4)}`, email: x.email.toLowerCase(), name: x.name || '', role: 'admin', password: x.password, createdAt: iso(), updatedAt: iso(), lastLoginAt: ZERO_TIME, owner: true };
      A.users.push(u);
      newSession(res, u);
      audit(u, 'setup', u.email, '', req);
      return send(res, 200, { user: pub(u) }), true;
    }
    if (b === 'login' && M === 'POST') {
      const x = await readBody(req) || {};
      const key = String(x.email || '').toLowerCase();
      const f = A.failures.get(key);
      if (f && f.until > Date.now()) {
        res.setHeader('Retry-After', String(Math.ceil((f.until - Date.now()) / 1000)));
        return err(res, 429, 'too many failed attempts'), true;
      }
      const u = A.users.find((y) => y.email === key);
      if (!u || u.password !== x.password) {
        const n = (f && f.until <= Date.now() && f.count >= 5 ? 0 : (f ? f.count : 0)) + 1;
        A.failures.set(key, { count: n, until: n >= 5 ? Date.now() + 30000 : 0 });
        audit(null, 'login.failed', key, 'invalid email or password', req);
        return err(res, 401, 'invalid email or password'), true;
      }
      A.failures.delete(key);
      newSession(res, u);
      audit(u, 'login', u.email, '', req);
      return send(res, 200, { user: pub(u) }), true;
    }
    if (b === 'logout' && M === 'POST') {
      const sid = cookies(req).cdc_session;
      if (sid) A.sessions.delete(sid);
      setCookie(res, '');
      return send(res, 204), true;
    }
    if (b === 'forgot' && M === 'POST') {
      if (!smtpConfigured()) return err(res, 409, 'email is not configured'), true;
      const x = await readBody(req) || {};
      const u = A.users.find((y) => y.email === String(x.email || '').toLowerCase());
      if (u) { const t = rand(); A.resets.set(t, { userId: u.id, exp: Date.now() + 3600e3 }); mail(u.email, 'Reset your password', `${A.smtp.publicUrl || origin(req)}/#/reset/${t}`); }
      return send(res, 202, {}), true;
    }
    if (b === 'reset' && M === 'POST') {
      const x = await readBody(req) || {};
      const r = A.resets.get(x.token || '');
      if (!r || r.exp < Date.now()) return err(res, 400, 'the reset link is invalid or has expired'), true;
      if (pwErr(x.password)) return err(res, 400, pwErr(x.password)), true;
      const u = A.users.find((y) => y.id === r.userId);
      u.password = x.password; u.updatedAt = iso();
      A.resets.delete(x.token);
      revokeSessions(u.id);
      audit(u, 'password.reset', u.email, '', req);
      return send(res, 204), true;
    }
    if (b === 'invite' && c === 'accept' && M === 'POST') {
      const x = await readBody(req) || {};
      const inv = A.invitations.find((i) => i.token === x.token && Date.parse(i.expiresAt) > Date.now());
      if (!inv) return err(res, 404, 'the invitation is invalid, used or expired'), true;
      if (pwErr(x.password)) return err(res, 400, pwErr(x.password)), true;
      if (A.users.some((u) => u.email === inv.email)) return err(res, 409, 'an account with this email already exists'), true;
      const u = { id: `u_${rand(4)}`, email: inv.email, name: x.name || '', role: inv.role, password: x.password, createdAt: iso(), updatedAt: iso(), lastLoginAt: ZERO_TIME };
      A.users.push(u);
      A.invitations = A.invitations.filter((i) => i !== inv);
      newSession(res, u);
      audit(u, 'invitation.accept', u.email, `role ${u.role}`, req);
      return send(res, 200, { user: pub(u) }), true;
    }
    if (b === 'invite' && c && M === 'GET') {
      const inv = A.invitations.find((i) => i.token === c && Date.parse(i.expiresAt) > Date.now());
      if (!inv) return err(res, 404, 'the invitation is invalid, used or expired'), true;
      return send(res, 200, { email: inv.email, role: inv.role, expiresAt: inv.expiresAt }), true;
    }
    return false;
  }

  /** Minimum role for a request on the existing + new API. */
  function requiredRole(M, parts, body) {
    const [a, b, c, d] = parts;
    if (['users', 'invitations', 'audit', 'settings'].includes(a) || (a === 'mcp' && b === 'tokens')) return 'admin';
    if (a === 'auth') return 'viewer';
    if (a === 'alerts') return M === 'GET' ? 'viewer' : 'admin';
    if (M === 'GET') return 'viewer';
    if (a === 'indexes' && (c === 'search' || (c === 'settings' && d === 'preview'))) return 'viewer';
    if (a === 'tasks') {
      if (b === 'test' || b === 'batch') return 'moderator';
      if (['start', 'stop', 'pause', 'resume', 'restart', 'resync', 'reset-checkpoint', 'diff', 'dlq'].includes(c)) return 'moderator';
      return 'admin';
    }
    if (a === 'meili' && b === 'tasks' && c === 'cancel') return 'moderator';
    if (a === 'connectors' && (b === 'test' || c === 'test')) return 'moderator';
    if (a === 'destinations' && (b === 'test' || c === 'test' || c === 'health')) return 'moderator';
    return 'admin';
  }

  function activeAlerts() {
    const latest = new Map();
    for (const e of [...A.events].sort((x, y) => Date.parse(x.at) - Date.parse(y.at))) latest.set(`${e.ruleId}|${e.taskId}`, e);
    return [...latest.values()].filter((e) => e.state === 'firing' && A.rules.some((r) => r.id === e.ruleId)).sort((x, y) => Date.parse(y.at) - Date.parse(x.at));
  }

  function validateRule(r) {
    const e = [];
    if (!String(r.name || '').trim()) e.push('name is required');
    const c = r.condition || {};
    if (!['task_state', 'lag', 'dlq', 'errors', 'meili_down', 'stalled'].includes(c.type)) e.push('condition.type is invalid');
    if (c.type === 'task_state' && !(c.states || []).length) e.push('condition.states is required');
    if (c.type === 'lag' && !(c.thresholdMs > 0)) e.push('condition.thresholdMs must be positive');
    if ((c.type === 'dlq' || c.type === 'errors') && !(c.count > 0)) e.push('condition.count must be positive');
    for (const k of ['window', 'for']) if (c[k] && !DURATION_RE.test(c[k])) e.push(`condition.${k} is not a duration`);
    if (r.cooldown && !DURATION_RE.test(r.cooldown)) e.push('cooldown is not a duration');
    if (!['info', 'warning', 'critical'].includes(r.severity)) e.push('severity is invalid');
    for (const m of (r.channels && r.channels.emails) || []) if (!EMAIL_RE.test(m)) e.push(`invalid email ${m}`);
    const ch = r.channels || {};
    if (!(ch.emails || []).length && !(ch.telegramChatIds || []).length && !ch.webhookUrl) e.push('at least one channel is required');
    return e;
  }
  const normRule = (r) => ({
    name: r.name, enabled: r.enabled !== false, condition: r.condition, taskIds: r.taskIds || [], severity: r.severity || 'warning',
    channels: { emails: r.channels?.emails || [], telegramChatIds: r.channels?.telegramChatIds || [], webhookUrl: r.channels?.webhookUrl || '' },
    cooldown: r.cooldown || '30m', notifyResolved: !!r.notifyResolved, ...(r.destinationIds && r.destinationIds.length ? { destinationIds: r.destinationIds } : {}),
  });
  /** Server-side channel rules: alerts need SMTP or Telegram; recipients need their channel. */
  function channelErr(x, creating, res) {
    if (!smtpConfigured() && !telegramConfigured()) { err(res, 409, 'alerts need a notification channel: configure SMTP or a Telegram bot first'); return true; }
    const ch = x.channels || {};
    if ((ch.emails || []).length && !smtpConfigured()) { err(res, 422, 'email recipients need SMTP to be configured'); return true; }
    if ((ch.telegramChatIds || []).length && !telegramConfigured()) { err(res, 422, 'Telegram chat ids need a Telegram bot to be configured'); return true; }
    return false;
  }
  function deliver(rule) {
    const out = [];
    for (const m of rule.channels.emails) out.push({ channel: 'email', target: m, ok: smtpConfigured(), error: smtpConfigured() ? '' : 'SMTP is not configured' });
    for (const id of rule.channels.telegramChatIds) {
      const ok = telegramConfigured() && /^-?\d+$/.test(id);
      out.push({ channel: 'telegram', target: id, ok, error: ok ? '' : telegramConfigured() ? 'Bad Request: chat not found' : 'Telegram is not configured' });
    }
    if (rule.channels.webhookUrl) { const ok = !/fail/.test(rule.channels.webhookUrl); out.push({ channel: 'webhook', target: rule.channels.webhookUrl, ok, error: ok ? '' : 'POST returned 500 Internal Server Error' }); }
    return out;
  }
  /** Mock only: fire or resolve an alert and broadcast it. */
  function emitAlert({ ruleId, taskId = '', state = 'firing', message }) {
    const rule = A.rules.find((r) => r.id === ruleId) || A.rules[0];
    const task = tasks().find((t) => t.id === taskId);
    const e = {
      id: `e_${rand(5)}`, ruleId: rule.id, ruleName: rule.name, severity: rule.severity, taskId, taskName: task ? task.name : '', state,
      message: message || (state === 'firing' ? `${task ? task.name : 'Meilisearch'} triggered “${rule.name}”` : `${task ? task.name : 'Meilisearch'} recovered`), at: iso(),
      deliveries: deliver(rule).map((d) => ({ ...d, at: iso() })),
    };
    A.events.unshift(e);
    broadcast('alert', e);
    return e;
  }

  // ---------------------------------------------------------------- authenticated endpoints
  async function handle(req, res, parts, M, url, user) {
    const [a, b, c] = parts;
    const q = url.searchParams;
    if (a === 'auth' && b === 'me') {
      if (!c && M === 'GET') return send(res, 200, { user: user === API_USER ? API_USER : pub(user) }), true;
      if (user === API_USER) return err(res, 400, 'the API token has no profile'), true;
      if (!c && M === 'PATCH') { const x = await readBody(req) || {}; if (typeof x.name === 'string') user.name = x.name.trim(); user.updatedAt = iso(); return send(res, 200, { user: pub(user) }), true; }
      if (c === 'password' && M === 'POST') {
        const x = await readBody(req) || {};
        if (x.currentPassword !== user.password) return err(res, 400, 'the current password is wrong'), true;
        if (pwErr(x.newPassword)) return err(res, 400, pwErr(x.newPassword)), true;
        user.password = x.newPassword; user.updatedAt = iso();
        revokeSessions(user.id, cookies(req).cdc_session);
        audit(user, 'password.change', user.email, '', req);
        return send(res, 204), true;
      }
    }
    if (a === 'users') {
      if (!b && M === 'GET') return send(res, 200, A.users.map(pub)), true;
      if (!b && M === 'POST') {
        const x = await readBody(req) || {};
        if (!EMAIL_RE.test(x.email || '')) return err(res, 400, 'a valid email is required'), true;
        if (!ROLE_RANK.hasOwnProperty(x.role)) return err(res, 400, 'role is invalid'), true;
        if (pwErr(x.password)) return err(res, 400, pwErr(x.password)), true;
        if (A.users.some((u) => u.email === x.email.toLowerCase())) return err(res, 409, 'a user with this email already exists'), true;
        const u = { id: `u_${rand(4)}`, email: x.email.toLowerCase(), name: x.name || '', role: x.role, password: x.password, createdAt: iso(), updatedAt: iso(), lastLoginAt: ZERO_TIME };
        A.users.push(u);
        audit(user, 'user.create', u.email, `role ${u.role}`, req);
        return send(res, 201, pub(u)), true;
      }
      const u = A.users.find((y) => y.id === b);
      if (!u) return err(res, 404, 'user not found'), true;
      const admins = A.users.filter((y) => y.role === 'admin').length;
      if (!c && M === 'PATCH') {
        const x = await readBody(req) || {};
        if (x.role && !ROLE_RANK.hasOwnProperty(x.role)) return err(res, 400, 'role is invalid'), true;
        if (x.role && x.role !== u.role && u.id === user.id) return err(res, 403, 'forbidden: you cannot change your own role'), true;
        if (x.role && x.role !== u.role && u.owner) return err(res, 403, `forbidden: the primary administrator's role cannot be changed; ${OWNER_HINT}`), true;
        if (x.role && x.role !== 'admin' && u.role === 'admin' && admins <= 1) return err(res, 409, 'cannot demote the last admin'), true;
        if (typeof x.name === 'string') u.name = x.name;
        if (x.role) { audit(user, 'user.role', u.email, `${u.role} → ${x.role}`, req); u.role = x.role; }
        u.updatedAt = iso();
        return send(res, 200, pub(u)), true;
      }
      if (c === 'password' && M === 'POST') {
        const x = await readBody(req) || {};
        if (pwErr(x.password)) return err(res, 400, pwErr(x.password)), true;
        if (u.owner && u.id !== user.id) return err(res, 403, 'forbidden: only the primary administrator can change their password; an operator can reset it with `meilisearch-cdc admin users reset-password`'), true;
        u.password = x.password; u.updatedAt = iso(); revokeSessions(u.id);
        audit(user, 'user.password', u.email, '', req);
        return send(res, 204), true;
      }
      if (!c && M === 'DELETE') {
        if (u.id === user.id) return err(res, 409, 'you cannot delete yourself'), true;
        if (u.owner) return err(res, 403, `forbidden: the primary administrator cannot be deleted; ${OWNER_HINT}`), true;
        if (u.role === 'admin' && admins <= 1) return err(res, 409, 'cannot delete the last admin'), true;
        A.users = A.users.filter((y) => y !== u); revokeSessions(u.id);
        audit(user, 'user.delete', u.email, '', req);
        return send(res, 204), true;
      }
    }
    if (a === 'invitations') {
      const view = (i) => ({ id: i.id, email: i.email, role: i.role, invitedBy: i.invitedBy, createdAt: i.createdAt, expiresAt: i.expiresAt, emailed: i.emailed });
      if (!b && M === 'GET') return send(res, 200, A.invitations.filter((i) => Date.parse(i.expiresAt) > Date.now()).map(view)), true;
      if (!b && M === 'POST') {
        const x = await readBody(req) || {};
        if (!EMAIL_RE.test(x.email || '')) return err(res, 400, 'a valid email is required'), true;
        if (!ROLE_RANK.hasOwnProperty(x.role)) return err(res, 400, 'role is invalid'), true;
        if (A.users.some((u) => u.email === x.email.toLowerCase())) return err(res, 409, 'a user with this email already exists'), true;
        const token = rand();
        const link = `${A.smtp.publicUrl || origin(req)}/#/invite/${token}`;
        const smtpFails = /fail/.test(A.smtp.host);
        const inv = { id: `i_${rand(4)}`, token, email: x.email.toLowerCase(), role: x.role, invitedBy: user.email || user.name, createdAt: iso(), expiresAt: iso(Date.now() + 7 * 86400e3), emailed: smtpConfigured() && !smtpFails };
        A.invitations.push(inv);
        if (inv.emailed) mail(inv.email, 'You are invited to meilisearch-cdc', link);
        audit(user, 'invitation.create', inv.email, `role ${inv.role}`, req);
        return send(res, 201, { invitation: view(inv), link, emailed: inv.emailed, emailError: smtpConfigured() && smtpFails ? 'dial tcp: connection refused' : '' }), true;
      }
      if (b && M === 'DELETE') {
        const inv = A.invitations.find((i) => i.id === b);
        if (!inv) return err(res, 404, 'invitation not found'), true;
        A.invitations = A.invitations.filter((i) => i !== inv);
        audit(user, 'invitation.revoke', inv.email, '', req);
        return send(res, 204), true;
      }
    }
    if (a === 'settings' && b === 'smtp') {
      const view = () => ({ host: A.smtp.host, port: A.smtp.port, username: A.smtp.username, password: A.smtp.password ? MASK : '', from: A.smtp.from, tls: A.smtp.tls, publicUrl: A.smtp.publicUrl, configured: smtpConfigured() });
      if (!c && M === 'GET') return send(res, 200, view()), true;
      if (!c && M === 'PUT') {
        const x = await readBody(req) || {};
        if (x.host && !EMAIL_RE.test(x.from || '')) return err(res, 400, 'from must be a valid email address'), true;
        if (!['auto', 'starttls', 'tls', 'none'].includes(x.tls || 'auto')) return err(res, 400, 'tls must be auto, starttls, tls or none'), true;
        A.smtp = { host: x.host || '', port: Number(x.port) || 587, username: x.username || '', password: x.password === MASK ? A.smtp.password : (x.password || ''), from: x.from || '', tls: x.tls || 'auto', publicUrl: x.publicUrl || '' };
        audit(user, 'settings.smtp', A.smtp.host || '(disabled)', '', req);
        return send(res, 200, view()), true;
      }
      if (c === 'test' && M === 'POST') {
        const x = await readBody(req) || {};
        if (!smtpConfigured()) return send(res, 200, { ok: false, error: 'SMTP is not configured' }), true;
        if (/fail/.test(A.smtp.host)) return send(res, 200, { ok: false, error: `dial tcp ${A.smtp.host}:${A.smtp.port}: i/o timeout (hint: port ${A.smtp.port} usually needs ${A.smtp.port === 465 ? 'implicit TLS' : 'STARTTLS'}; check the firewall and the encryption setting)` }), true;
        await new Promise((r) => setTimeout(r, 400));
        if (!EMAIL_RE.test(x.to || '')) return send(res, 200, { ok: false, error: '550 invalid recipient' }), true;
        mail(x.to, 'meilisearch-cdc test email', '');
        return send(res, 200, { ok: true, error: '' }), true;
      }
    }
    if (a === 'settings' && b === 'telegram') {
      const view = () => ({ botToken: A.telegram.botToken ? MASK : '', apiUrl: A.telegram.apiUrl, configured: telegramConfigured(), botName: A.telegram.botName });
      if (!c && M === 'GET') return send(res, 200, view()), true;
      if (!c && M === 'PUT') {
        const x = await readBody(req) || {};
        const token = x.botToken === MASK ? A.telegram.botToken : (x.botToken || '');
        if (token && !/^\d{5,}:[\w-]{20,}$/.test(token)) return err(res, 422, 'invalid bot token: Unauthorized'), true;
        A.telegram = { botToken: token, apiUrl: x.apiUrl || 'https://api.telegram.org', botName: token ? 'meili_cdc_bot' : '' };
        audit(user, 'settings.telegram', token ? '@meili_cdc_bot' : '(disabled)', '', req);
        return send(res, 200, view()), true;
      }
      if (c === 'test' && M === 'POST') {
        const x = await readBody(req) || {};
        if (!telegramConfigured()) return send(res, 200, { ok: false, error: 'Telegram is not configured' }), true;
        if (!/^-?\d+$/.test(String(x.chatId || ''))) return send(res, 200, { ok: false, error: 'Bad Request: chat not found' }), true;
        return send(res, 200, { ok: true, error: '' }), true;
      }
    }
    if (a === 'settings' && b === 'mcp') {
      const view = () => ({ enabled: A.mcp.enabled, endpoint: '/mcp', policy: A.mcp.policy, tools: MCP_TOOLS, resources: MCP_RESOURCES.map(({ uri, name, description }) => ({ uri, name, description })) });
      if (!c && M === 'GET') return send(res, 200, view()), true;
      if (!c && M === 'PUT') {
        const x = await readBody(req) || {};
        if (typeof x.enabled === 'boolean') A.mcp.enabled = x.enabled;
        if (x.policy) {
          const names = new Set(MCP_TOOLS.map((t) => t.name)); const uris = new Set(MCP_RESOURCES.map((r) => r.uri));
          const p = {};
          for (const role of Object.keys(ROLE_RANK)) p[role] = { tools: (x.policy[role]?.tools || []).filter((t) => names.has(t)), resources: (x.policy[role]?.resources || []).filter((r) => uris.has(r)) };
          A.mcp.policy = p;
        }
        audit(user, 'settings.mcp', A.mcp.enabled ? 'enabled' : 'disabled', x.policy ? 'policy updated' : '', req);
        return send(res, 200, view()), true;
      }
      if (c === 'reset-policy' && M === 'POST') { A.mcp.policy = defaultPolicy(); audit(user, 'settings.mcp', 'policy reset', '', req); return send(res, 200, view()), true; }
    }
    if (a === 'mcp' && b === 'tokens') {
      const view = ({ secret, ...t }) => t;
      const d = parts[3];
      if (!c && M === 'GET') return send(res, 200, A.mcp.tokens.map(view)), true;
      if (!c && M === 'POST') {
        const x = await readBody(req) || {};
        if (!String(x.name || '').trim()) return err(res, 400, 'name is required'), true;
        if (!ROLE_RANK.hasOwnProperty(x.role)) return err(res, 400, 'role is invalid'), true;
        if (x.expiresIn && !DURATION_RE.test(x.expiresIn)) return err(res, 400, 'expiresIn is not a duration'), true;
        const id = `t_${rand(3).toLowerCase().replace(/[^a-z0-9]/g, 'x')}`;
        const secret = `cdcmcp_${id.slice(-4)}${rand(24)}`;
        const hours = x.expiresIn ? parseFloat(x.expiresIn) : 0;
        const t = { id, name: x.name.trim(), role: x.role, prefix: secret.slice(0, 11), createdAt: iso(), createdBy: user.email || user.name, lastUsedAt: ZERO_TIME, expiresAt: hours ? iso(Date.now() + hours * 3600e3) : ZERO_TIME, secret };
        A.mcp.tokens.unshift(t);
        audit(user, 'mcp.token.create', t.name, `role ${t.role}`, req);
        return send(res, 201, { token: secret, info: view(t) }), true;
      }
      const t = A.mcp.tokens.find((y) => y.id === c);
      if (!t) return err(res, 404, 'token not found'), true;
      if (d === 'rotate' && M === 'POST') {
        t.secret = `cdcmcp_${t.id.slice(-4)}${rand(24)}`; t.prefix = t.secret.slice(0, 11); t.lastUsedAt = ZERO_TIME;
        audit(user, 'mcp.token.rotate', t.name, '', req);
        return send(res, 200, { token: t.secret, info: view(t) }), true;
      }
      if (!d && M === 'DELETE') {
        A.mcp.tokens = A.mcp.tokens.filter((y) => y !== t);
        audit(user, 'mcp.token.revoke', t.name, '', req);
        return send(res, 204), true;
      }
    }
    if (a === 'alerts') {
      if (b === 'channels' && M === 'GET') return send(res, 200, { email: smtpConfigured(), telegram: telegramConfigured(), webhook: true }), true;
      if (b === 'active' && M === 'GET') return send(res, 200, activeAlerts()), true;
      if (b === 'events' && M === 'GET') {
        const lim = Number(q.get('limit') || 100);
        return send(res, 200, A.events.filter((e) => (!q.get('ruleId') || e.ruleId === q.get('ruleId')) && (!q.get('state') || e.state === q.get('state'))).slice(0, lim)), true;
      }
      if (b === 'rules') {
        if (!c && M === 'GET') return send(res, 200, A.rules), true;
        if (!c && M === 'POST') {
          const x = await readBody(req) || {};
          const errs = validateRule(x);
          if (errs.length) return err(res, 400, 'invalid rule', errs), true;
          if (channelErr(x, true, res)) return true;
          const r = { id: `r_${rand(4)}`, ...normRule(x), createdAt: iso(), updatedAt: iso() };
          A.rules.push(r);
          audit(user, 'rule.create', r.name, '', req);
          return send(res, 201, r), true;
        }
        const r = A.rules.find((y) => y.id === c);
        if (!r) return err(res, 404, 'rule not found'), true;
        if (!parts[3] && M === 'PUT') {
          const x = await readBody(req) || {};
          const errs = validateRule(x);
          if (errs.length) return err(res, 400, 'invalid rule', errs), true;
          if (x.enabled !== false && channelErr(x, false, res)) return true;
          Object.assign(r, normRule(x), { updatedAt: iso() });
          audit(user, 'rule.update', r.name, '', req);
          return send(res, 200, r), true;
        }
        if (!parts[3] && M === 'DELETE') { A.rules = A.rules.filter((y) => y !== r); audit(user, 'rule.delete', r.name, '', req); return send(res, 204), true; }
        if (parts[3] === 'test' && M === 'POST') return send(res, 200, { results: deliver(r) }), true;
      }
    }
    if (a === 'audit' && M === 'GET') return send(res, 200, A.audit.slice(0, Number(q.get('limit') || 200))), true;
    return false;
  }

  return { init, authenticate, publicAuth, requiredRole, handle, audit, emitAlert, state: () => A, ROLE_RANK, pub, newSession };
}
