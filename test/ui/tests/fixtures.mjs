// Shared fixtures: reset the mock before each test and fail on console errors.
import { test as base, expect } from '@playwright/test';

export const PAGES = [
  { name: 'overview', hash: '#/overview', heading: 'Overview' },
  { name: 'tasks', hash: '#/tasks', heading: 'Tasks' },
  { name: 'task-detail', hash: '#/tasks/products-pg', heading: 'Products catalog' },
  { name: 'task-new', hash: '#/tasks/new', heading: 'New task' },
  { name: 'task-edit', hash: '#/tasks/orders-mysql/edit', heading: 'Edit Orders' },
  { name: 'indexes', hash: '#/indexes', heading: 'Indexes' },
  { name: 'index-detail', hash: '#/indexes/products', heading: 'products' },
  { name: 'meili-tasks', hash: '#/meili-tasks', heading: 'Meilisearch tasks' },
  { name: 'settings', hash: '#/settings', heading: 'Settings' },
  { name: 'alerts', hash: '#/alerts', heading: 'Alerts' },
  { name: 'connectors', hash: '#/connectors', heading: 'Connectors' },
  { name: 'destinations', hash: '#/destinations', heading: 'Destinations' },
  { name: 'destination-detail', hash: '#/destinations/default', heading: 'Production' },
  { name: 'connector-detail', hash: '#/connectors/c_shop_pg', heading: 'Shop Postgres (primary)' },
  { name: 'settings-users', hash: '#/settings/users', heading: 'Settings' },
  { name: 'settings-email', hash: '#/settings/email', heading: 'Settings' },
  { name: 'settings-telegram', hash: '#/settings/telegram', heading: 'Settings' },
  { name: 'settings-mcp', hash: '#/settings/mcp', heading: 'Settings' },
  { name: 'settings-audit', hash: '#/settings/audit', heading: 'Settings' },
];

/** Public pages rendered without a session. */
export const PUBLIC = [
  { name: 'login', hash: '#/login', heading: 'Sign in' },
  { name: 'forgot', hash: '#/forgot', heading: 'Reset your password' },
  { name: 'reset', hash: '#/reset/some-token', heading: 'Choose a new password' },
];

const isMock = !process.env.UI_BASE_URL;

export const test = base.extend({
  consoleErrors: [async ({ page }, use) => {
    const errors = [];
    // 4xx answers that are part of the protocol (401 before login, 403 role checks, 409/422/429...) are logged by
    // the browser as "Failed to load resource"; they are expected and asserted by the tests themselves.
    const expected = /Failed to load resource: the server responded with a status of (400|401|403|404|409|422|429)/;
    page.on('console', (m) => { if (m.type() === 'error' && !expected.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
    await use(errors);
  }, { auto: true }],
  // Role of the session the test starts with ("admin" | "moderator" | "viewer"), or null for logged out.
  role: ['admin', { option: true }],
  // Mock state for the test: "default" or "empty" (no users: setup required).
  mockState: ['default', { option: true }],
  resetMock: [async ({ page, role, mockState }, use) => {
    if (isMock) {
      await page.request.post(`/__mock/reset${mockState === 'empty' ? '?empty=1' : ''}`);
      if (role && mockState !== 'empty') await page.request.post(`/__mock/login?role=${role}`);
    }
    await use();
  }, { auto: true }],
});

/** Open a route and wait for the page heading. */
export async function open(page, hash, heading) {
  await page.goto(`/${hash}`);
  if (heading) await expect(page.getByRole('heading', { level: 1, name: heading })).toBeVisible();
}

/** A second, independent browser session (e.g. an invitee or another role). */
export async function newSession(browser, baseURL, role) {
  const context = await browser.newContext({ baseURL, viewport: { width: 1360, height: 900 } });
  const page = await context.newPage();
  if (role) await page.request.post(`/__mock/login?role=${role}`);
  return { context, page };
}

export const outbox = async (request) => (await request.get('/__mock/outbox')).json();

export { expect, isMock };
