// Shared helpers for the full GUI suite: sign in, lab changes and
// Meilisearch checks (assertions only; the actions go through the GUI).
import { test as base, expect } from '@playwright/test';

export const LAB = process.env.LAB_URL || 'http://127.0.0.1:8799';
export const ADMIN = { email: process.env.CDC_ADMIN_EMAIL || 'admin@example.com', password: process.env.CDC_ADMIN_PASSWORD || 'e2e-admin-password' };

export const test = base.extend({
  consoleErrors: [async ({ page }, use) => {
    const errors = [];
    page.on('console', (m) => {
      if (m.type() === 'error' && !/401|Unauthorized|Failed to load resource/.test(m.text())) errors.push(m.text());
    });
    page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
    await use(errors);
    expect(errors, 'console errors').toEqual([]);
  }, { auto: true }],
});
export { expect };

let labInfo;
export async function lab(request) {
  if (!labInfo) labInfo = await (await request.get(`${LAB}/info`)).json();
  return labInfo;
}

export async function change(request, source, op, id, name) {
  const r = await request.post(`${LAB}/change`, { data: { source, op, id, name } });
  expect(r.status(), `${source} ${op} ${id}: ${await r.text()}`).toBe(204);
}

export async function login(page) {
  await page.goto('/#/overview');
  const loginForm = page.getByRole('button', { name: 'Sign in' });
  const shell = page.getByRole('navigation').first();
  await expect(loginForm.or(shell)).toBeVisible();
  if (await loginForm.isVisible()) {
    await page.getByLabel('Email').fill(ADMIN.email);
    await page.getByLabel('Password').fill(ADMIN.password);
    await loginForm.click();
    await page.waitForURL((u) => !u.hash.startsWith('#/login'));
  }
  await dismissOnboarding(page);
}

/** Onboarding opens right after sign-in until a task exists; close it. */
export async function dismissOnboarding(page) {
  const dismiss = page.getByTestId('ob-dismiss');
  await dismiss.waitFor({ timeout: 2_500 }).catch(() => {});
  if (await dismiss.isVisible()) {
    await dismiss.click();
    await page.waitForURL((u) => !u.hash.startsWith('#/welcome'));
  }
}

/** Meilisearch document (or null) straight from the instance, for assertions. */
export async function meiliDoc(request, meili, uid, id) {
  const r = await request.get(`${meili.url}/indexes/${uid}/documents/${encodeURIComponent(id)}`, { headers: { Authorization: `Bearer ${meili.apiKey}` } });
  return r.status() === 200 ? r.json() : null;
}

export async function meiliCount(request, meili, uid) {
  const r = await request.get(`${meili.url}/indexes/${uid}/stats`, { headers: { Authorization: `Bearer ${meili.apiKey}` } });
  return r.status() === 200 ? (await r.json()).numberOfDocuments : -1;
}

/** Calls the service API from the signed in page (same origin, session cookie). */
export async function api(page, method, path, body) {
  return page.evaluate(async ({ method, path, body }) => {
    const r = await fetch(path, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    const text = await r.text();
    let json = null;
    try { json = JSON.parse(text); } catch { /* not json */ }
    return { status: r.status, json, text };
  }, { method, path, body });
}

// ---------------------------------------------------------------- Mailpit
export const MAILPIT = process.env.MAILPIT_URL || 'http://localhost:18025';
// REAL_SMTP=1 keeps the SMTP settings already saved in the dashboard (your
// own server) and sends alerts to ALERT_EMAIL; otherwise mail goes to Mailpit.
export const REAL_SMTP = !!process.env.REAL_SMTP;
export const ALERT_EMAIL = process.env.ALERT_EMAIL || 'oncall@example.com';

/** Waits for a Mailpit message to `to` whose subject matches, returns its text. */
export async function waitMail(request, to, subject) {
  let found;
  await expect.poll(async () => {
    const list = await (await request.get(`${MAILPIT}/api/v1/messages?limit=200`)).json();
    found = (list.messages || []).find((m) => m.To.some((t) => t.Address === to) && subject.test(m.Subject));
    return !!found;
  }, { timeout: 60_000, message: `mail to ${to} matching ${subject}` }).toBe(true);
  const msg = await (await request.get(`${MAILPIT}/api/v1/message/${found.ID}`)).json();
  return { subject: msg.Subject, text: msg.Text, html: msg.HTML };
}

// ---------------------------------------------------------------- MCP
/** A minimal MCP client over Streamable HTTP (JSON-RPC 2.0), wire level. */
export class MCP {
  constructor(request, base, token) { this.request = request; this.url = `${base}/mcp`; this.token = token; this.id = 0; this.session = ''; }
  async rpc(method, params, { notify = false } = {}) {
    const headers = { Authorization: `Bearer ${this.token}`, 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream' };
    if (this.session) headers['Mcp-Session-Id'] = this.session;
    const body = { jsonrpc: '2.0', method, ...(params ? { params } : {}), ...(notify ? {} : { id: ++this.id }) };
    const r = await this.request.post(this.url, { headers, data: body });
    const sid = r.headers()['mcp-session-id'];
    if (sid) this.session = sid;
    if (notify) return { status: r.status() };
    const text = await r.text();
    if (r.status() >= 300) return { status: r.status(), error: text };
    // Responses come as JSON or as an SSE stream carrying the JSON message.
    const json = r.headers()['content-type']?.includes('text/event-stream')
      ? JSON.parse(text.split('\n').filter((l) => l.startsWith('data:')).map((l) => l.slice(5)).pop())
      : JSON.parse(text);
    return { status: r.status(), ...json };
  }
  async connect() {
    const r = await this.rpc('initialize', { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'cdc-e2e', version: '1.0' } });
    if (r.status < 300) await this.rpc('notifications/initialized', undefined, { notify: true });
    return r;
  }
  async tools() { return ((await this.rpc('tools/list', {})).result?.tools || []).map((t) => t.name); }
  async call(name, args) { return this.rpc('tools/call', { name, arguments: args || {} }); }
}

/** Signs in as someone in a fresh browser context. */
export async function signInAs(browser, baseURL, email, password) {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 1440, height: 960 } });
  const page = await ctx.newPage();
  await page.goto('/#/login');
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password').fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((u) => !u.hash.startsWith('#/login'));
  await dismissOnboarding(page);
  return { ctx, page };
}

/** Runs a task action from the task page, confirming when asked. */
export async function taskAction(page, action) {
  await page.locator(`[data-action="${action}"]`).click();
  const ok = page.getByRole('dialog').getByTestId('confirm-ok');
  if (await ok.isVisible({ timeout: 1500 }).catch(() => false)) await ok.click();
}

export async function taskId(page, name) {
  const r = await api(page, 'GET', '/api/v1/tasks');
  const t = (r.json || []).find((x) => x.name === name);
  if (!t) throw new Error(`task ${name} not found`);
  return t.id;
}

export async function sql(request, statement) {
  const r = await request.post(`${LAB}/sql`, { data: { sql: statement } });
  expect(r.status(), await r.text()).toBe(204);
}

/**
 * Creates a task with the wizard. `opts` hooks into the steps:
 * source(page), mapping(page), index(page), sync(page).
 */
export async function wizardTask(page, { connector, name, table, uid, destination, start = true, ...hooks }) {
  await page.goto('/#/tasks/new');
  await page.locator('[data-connector]', { hasText: connector }).click();
  await page.getByTestId('wizard-next').click();
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(name);
  await page.getByRole('textbox', { name: 'Table', exact: true }).fill(table);
  if (hooks.source) await hooks.source(page);
  await page.getByTestId('test-connection').click();
  await expect(page.getByTestId('test-result')).toContainText('Connected');
  await page.getByTestId('wizard-next').click();
  if (hooks.mapping) await hooks.mapping(page);
  await page.getByTestId('wizard-next').click();
  await page.getByLabel('Index uid').fill(uid);
  if (destination) await page.locator('[data-destination-card]', { hasText: destination }).click();
  if (hooks.index) await hooks.index(page);
  await page.getByTestId('wizard-next').click();
  if (hooks.sync) await hooks.sync(page);
  await page.getByTestId('wizard-next').click();
  await expect(page.getByText('Everything looks valid.')).toBeVisible();
  await page.getByTestId(start ? 'create-start-task' : 'create-task').click();
  await expect(page.getByRole('heading', { level: 1, name })).toBeVisible();
  return page.url().split('#/tasks/')[1].split('/')[0];
}

export const meiliHeaders = (m) => ({ Authorization: `Bearer ${m.apiKey}` });
