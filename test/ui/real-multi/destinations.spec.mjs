// Many Meilisearch instances managed in the dashboard (no MEILI_URL).
// Tests share state and run in order.
import { test as base, expect } from '@playwright/test';

const test = base.extend({
  consoleErrors: [async ({ page }, use) => {
    const errors = [];
    page.on('console', (m) => {
      // 401 before sign-in and failed health checks of the broken instance are expected.
      if (m.type() === 'error' && !/401|Unauthorized|502|503|Failed to load resource/.test(m.text())) errors.push(m.text());
    });
    page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
    await use(errors);
    expect(errors, 'console errors').toEqual([]);
  }, { auto: true }],
});
test.describe.configure({ mode: 'serial' });

let info;
const api = (request, method, path, data) => request.fetch(path, { method, data, headers: { Authorization: `Bearer ${info.token}` } });
const destinationId = async (request, name) => (await (await api(request, 'GET', '/api/v1/destinations')).json()).find((d) => d.name === name).id;

async function login(page) {
  await page.goto('/#/overview');
  await page.waitForURL(/#\/login/);
  await page.getByLabel('Email').fill(info.email);
  await page.getByLabel('Password').fill(info.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((u) => !u.hash.startsWith('#/login'));
  const dismiss = page.getByRole('button', { name: 'Dismiss onboarding' });
  const heading = page.getByRole('heading', { level: 1, name: 'Overview' });
  await expect(dismiss.or(heading)).toBeVisible();
  if (await dismiss.isVisible()) await dismiss.click();
  await expect(heading).toBeVisible();
}

async function addDestination(page, { name, url, key, interval }) {
  await page.goto('/#/destinations');
  await page.getByTestId('new-destination').click();
  const dialog = page.getByRole('dialog');
  await dialog.locator('#ds-name').fill(name);
  await dialog.locator('#ds-url').fill(url);
  if (key) await dialog.locator('#ds-key').fill(key);
  if (interval) await dialog.locator('#ds-interval').selectOption(interval);
  await dialog.getByTestId('destination-test-btn').click();
  await expect(dialog.getByTestId('destination-test')).toBeVisible();
  await dialog.getByTestId('destination-save').click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
}

test.beforeAll(async ({ request }) => {
  info = await (await request.get('/__test/info')).json();
  expect(info.meilisearch).toHaveLength(2);
});

test('add Meilisearch instances as destinations', async ({ page }) => {
  await login(page);
  await addDestination(page, { name: 'Primary', url: info.meilisearch[0], key: info.masterKey });
  await addDestination(page, { name: 'Replica', url: info.meilisearch[1], key: info.masterKey, interval: '5s' });
  await addDestination(page, { name: 'Broken', url: 'http://127.0.0.1:1', interval: '10s' });

  const list = page.getByTestId('destination-list');
  await expect(list.locator('[data-destination]')).toHaveCount(3);
  await expect(list.locator('[data-destination]', { hasText: 'Primary' }).getByTestId('default-badge')).toBeVisible();
  await expect(list.locator('[data-destination]', { hasText: 'Broken' })).toHaveClass(/is-down/);
});

test('destination health updates live and on demand', async ({ page, request }) => {
  await login(page);
  const replica = await destinationId(request, 'Replica');
  await page.goto(`/#/destinations/${replica}`);
  const health = page.getByTestId('destination-health');
  await expect(health).toHaveClass(/is-up/);
  await expect(page.getByTestId('check-times')).toContainText('next in');
  // The 5s background check arrives over SSE without a reload.
  const first = await (await api(request, 'GET', `/api/v1/destinations/${replica}`)).json();
  await expect.poll(async () => (await (await api(request, 'GET', `/api/v1/destinations/${replica}`)).json()).health.checkedAt, { timeout: 15_000 })
    .not.toBe(first.health.checkedAt);
  await page.getByTestId('check-now').click();
  await expect(page.getByTestId('check-times')).toContainText(/checked (just now|\d+s ago)/);

  const broken = await destinationId(request, 'Broken');
  await page.goto(`/#/destinations/${broken}`);
  await expect(page.getByTestId('destination-health')).toHaveClass(/is-down/);
});

test('per-instance Meilisearch features live on the destination page', async ({ page, request }) => {
  await login(page);
  const replica = await destinationId(request, 'Replica');
  await page.goto(`/#/destinations/${replica}/experimental`);
  await expect(page.getByTestId('experimental-features')).toBeVisible();
  await page.goto(`/#/destinations/${replica}/backups`);
  await page.getByTestId('create-dump').click();
  await expect(page.getByText(/dump/i).first()).toBeVisible();
  // The dump task is queued on the replica only.
  await expect.poll(async () => {
    const r = await (await api(request, 'GET', `/api/v1/meili/tasks?destination=${replica}&type=dumpCreation`)).json();
    return r.results.length;
  }).toBe(1);
  const primary = await destinationId(request, 'Primary');
  const p = await (await api(request, 'GET', `/api/v1/meili/tasks?destination=${primary}&type=dumpCreation`)).json();
  expect(p.results).toHaveLength(0);

  // Settings → General no longer has per-instance actions.
  await page.goto('/#/settings');
  await expect(page.getByTestId('create-dump')).toHaveCount(0);
});

test('indexes are grouped per instance', async ({ page, request }) => {
  const primary = await destinationId(request, 'Primary');
  const replica = await destinationId(request, 'Replica');
  for (const [d, uid] of [[primary, 'books'], [replica, 'books'], [replica, 'films']]) {
    const r = await api(request, 'POST', `/api/v1/indexes?destination=${d}`, { uid, primaryKey: 'id' });
    expect(r.status(), await r.text()).toBeLessThan(300);
  }
  await login(page);
  await page.goto('/#/indexes');
  const groupOf = (id) => page.locator(`[data-instance="${id}"]`);
  await expect(groupOf(primary)).toContainText('books');
  await expect(groupOf(replica)).toContainText('films');
  await expect(groupOf(primary)).not.toContainText('films');
  const broken = await destinationId(request, 'Broken');
  await expect(groupOf(broken)).toHaveClass(/is-down/);

  await page.getByTestId('instance-filter').selectOption(replica);
  await expect(groupOf(primary)).toHaveCount(0);
  await expect(groupOf(replica)).toBeVisible();
});

test('the health interval is edited from a dropdown', async ({ page, request }) => {
  await login(page);
  const replica = await destinationId(request, 'Replica');
  await page.goto(`/#/destinations/${replica}/settings`);
  const panel = page.getByTestId('destination-settings');
  await expect(panel.locator('#ds-interval')).toHaveValue('5s');
  await panel.locator('#ds-interval').selectOption('1m0s');
  await panel.getByTestId('destination-save').click();
  await expect.poll(async () => (await (await api(request, 'GET', `/api/v1/destinations/${replica}`)).json()).healthCheckInterval).toBe('1m0s');
});
