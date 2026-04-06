import { test, expect, open } from './fixtures.mjs';

const card = (page, name) => page.locator('[data-destination]').filter({ hasText: name });

test('list with health, sync counts and nav dots', async ({ page, consoleErrors }) => {
  await open(page, '#/destinations', 'Destinations');
  await expect(page.locator('[data-destination]')).toHaveCount(3);
  await expect(card(page, 'Production')).toContainText('default');
  await expect(card(page, 'Production')).toContainText('meili.prod.example.com');
  await expect(card(page, 'Production')).toContainText('v1.22.1');
  await expect(card(page, 'Production')).toContainText(/last sync/);
  await expect(card(page, 'Staging')).toContainText('connection refused');
  await expect(page.getByTestId('nav-dest-health')).toHaveAttribute('aria-label', '2 of 3 destinations healthy');
  expect(consoleErrors).toEqual([]);
});

test('create with test, edit keeps the key, make default, delete; 409 while used', async ({ page }) => {
  await open(page, '#/destinations', 'Destinations');
  await page.getByTestId('new-destination').click();
  const dialog = page.getByRole('dialog', { name: 'New destination' });
  await page.getByTestId('destination-save').click();
  await expect(dialog.getByText('Use an http(s) URL')).toBeVisible();
  await dialog.getByLabel('Name').fill('Analytics');
  await dialog.getByLabel('URL').fill('https://meili.analytics.example.com');
  await dialog.getByLabel('API key').fill('wrong');
  await page.getByTestId('destination-test-btn').click();
  await expect(dialog.getByTestId('destination-test')).toContainText('API key is invalid');
  await dialog.getByLabel('API key').fill('s3cret-key');
  await page.getByTestId('destination-test-btn').click();
  await expect(dialog.getByTestId('destination-test')).toContainText('v1.22.1');
  await page.getByTestId('destination-save').click();
  await expect(page.getByText('Destination added')).toBeVisible();
  const c = card(page, 'Analytics');
  await expect(c).toBeVisible();

  await c.getByRole('button', { name: 'Edit Analytics' }).click();
  const edit = page.getByRole('dialog', { name: 'Edit Analytics' });
  await expect(edit.getByLabel('API key')).toHaveValue('••••••');
  await edit.getByLabel('Name').fill('Analytics EU');
  const req = page.waitForRequest((r) => r.method() === 'PUT' && /\/destinations\//.test(r.url()));
  await page.getByTestId('destination-save').click();
  expect((await req).postDataJSON().apiKey).toBe('••••••');
  await expect(page.getByText('Destination saved')).toBeVisible();

  const c2 = card(page, 'Analytics EU');
  await c2.getByRole('button', { name: 'Make Analytics EU the default' }).click();
  await expect(page.getByText('Analytics EU is now the default')).toBeVisible();
  await expect(c2.getByTestId('default-badge')).toBeVisible();
  await expect(card(page, 'Production').getByTestId('default-badge')).toHaveCount(0);

  // EU replica is used by a task
  await card(page, 'EU replica').getByRole('button', { name: 'Delete EU replica' }).click();
  await page.getByRole('dialog').getByLabel(/Type/).fill('EU replica');
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByRole('alert').filter({ hasText: 'Destination is in use' })).toContainText('Users directory');

  await card(page, 'Staging').getByRole('button', { name: 'Delete Staging' }).click();
  await page.getByRole('dialog').getByLabel(/Type/).fill('Staging');
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByText('Destination deleted')).toBeVisible();
  await expect(card(page, 'Staging')).toHaveCount(0);
});

test('detail: health, sync summary and synced tasks with live rates', async ({ page }) => {
  await open(page, '#/destinations/default', 'Production');
  await expect(page.getByTestId('destination-health')).toContainText('Healthy');
  await expect(page.getByTestId('sync-summary')).toContainText('Running');
  const row = page.getByTestId('synced-tasks').locator('[data-task-id="products-pg"]');
  await expect(row).toContainText('Streaming');
  const rate = row.getByTestId('synced-rate');
  const first = await rate.textContent();
  await expect.poll(() => rate.textContent(), { timeout: 8000 }).not.toBe(first);
  await row.getByRole('button', { name: 'Open index products on Production' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'products' })).toBeVisible();
  await page.getByTestId('destination-detail-test').count();
});

test('switcher scopes Meilisearch requests', async ({ page }) => {
  await open(page, '#/meili-tasks', 'Meilisearch tasks');
  await page.getByTestId('destination-switcher').click();
  const req = page.waitForRequest((r) => /\/api\/v1\/meili\/tasks\?.*destination=eu-replica/.test(r.url()));
  await page.getByRole('menuitemradio', { name: /EU replica/ }).click();
  await req;
  await expect(page.getByTestId('scope-badge')).toContainText('EU replica');
  // the choice is remembered
  await page.reload();
  await expect(page.getByTestId('destination-switcher')).toContainText('EU replica');
  // an unreachable destination explains itself
  await page.getByTestId('destination-switcher').click();
  await page.getByRole('menuitemradio', { name: /Staging/ }).click();
  await expect(page.getByRole('alert').filter({ hasText: 'unreachable' })).toBeVisible();
});

test('wizard: choose the destination and see existing index suggestions', async ({ page }) => {
  await open(page, '#/tasks/new', 'New task');
  await page.locator('[data-connector="c_shop_pg"]').click();
  await page.getByTestId('wizard-next').click();
  await page.getByLabel('Name').fill('Catalog EU');
  await page.getByRole('textbox', { name: 'Table', exact: true }).fill('products');
  await page.getByTestId('wizard-next').click();
  await page.getByTestId('wizard-next').click();
  const dest = page.getByTestId('wizard-destination');
  await expect(dest.locator('[data-destination-card]')).toHaveCount(3);
  await dest.getByText('EU replica').click();
  await expect(page.getByLabel('Index uid')).toHaveValue('products');
  await expect(page.getByText(/Existing index · 1,249,002 documents/)).toBeVisible();
  await page.getByTestId('wizard-next').click();
  await page.getByTestId('wizard-next').click();
  await expect(page.getByText('Destination', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('EU replica');
  const req = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/v1/tasks'));
  await page.getByTestId('create-task').click();
  expect((await req).postDataJSON().index.destinationId).toBe('eu-replica');
  await expect(page.getByTestId('dest-badge')).toContainText('EU replica');
});

test('overview shows a tile per destination', async ({ page }) => {
  await open(page, '#/overview', 'Overview');
  const strip = page.getByTestId('destinations-strip');
  await expect(strip.locator('[data-destination]')).toHaveCount(3);
  await expect(strip.locator('[data-destination="staging"]')).toContainText('connection refused');
  await expect(page.getByTestId('task-card').filter({ hasText: 'Users directory' }).getByTestId('dest-badge')).toContainText('EU replica');
});

test.describe('single destination (MEILI_URL)', () => {
  test('hides multi-instance controls', async ({ page }) => {
    await page.request.post('/__mock/single?on=1');
    await open(page, '#/destinations', 'Destinations');
    await expect(page.getByTestId('single-note')).toContainText('Pinned by MEILI_URL');
    await expect(page.locator('[data-destination]')).toHaveCount(1);
    await expect(page.getByTestId('new-destination')).toHaveCount(0);
    await expect(page.getByRole('button', { name: /Make .* the default/ })).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^Delete / })).toHaveCount(0);
    await expect(page.locator('[data-destination]')).toContainText('managed by env');
    await expect(page.getByTestId('destination-switcher')).toHaveCount(0);
    await open(page, '#/tasks/new', 'New task');
    await page.locator('[data-connector="c_shop_pg"]').click();
    await page.getByTestId('wizard-next').click();
    await page.getByLabel('Name').fill('Single');
    await page.getByRole('textbox', { name: 'Table', exact: true }).fill('categories');
    await page.getByTestId('wizard-next').click();
    await page.getByTestId('wizard-next').click();
    await expect(page.getByTestId('wizard-destination')).toContainText('Writes to');
    await expect(page.getByRole('radiogroup', { name: 'Destination' })).toHaveCount(0);
  });
});

test('health is pushed over SSE; check now; interval select', async ({ page }) => {
  await open(page, '#/destinations/eu-replica', 'EU replica');
  const panel = page.getByTestId('destination-health');
  await expect(panel.getByTestId('check-times')).toContainText(/next in \d+s/);
  // eu-replica is checked every 5s: a destination.health event resets "checked … ago"
  await expect(panel.getByTestId('check-times')).toContainText(/checked (just now|[0-4]s ago)/, { timeout: 9000 });
  const checks = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/destinations/eu-replica/health/check'));
  await page.getByTestId('check-now').click();
  await checks;
  await expect(page.getByText('EU replica: healthy')).toBeVisible();

  await page.getByRole('tab', { name: 'Settings' }).click();
  const dialog = page.getByTestId('destination-settings');
  const sel = dialog.getByLabel('Health check every');
  await expect(sel).toHaveValue('5s');
  await expect(sel.locator('option')).toHaveCount(8);
  await sel.selectOption('1m0s');
  const req = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/destinations/eu-replica'));
  await page.getByTestId('destination-save').click();
  expect((await req).postDataJSON().healthCheckInterval).toBe('1m0s');
  await expect(page.getByText('Destination saved')).toBeVisible();
  await expect(sel).toHaveValue('1m0s');
});

test('unhealthy destinations show consecutive failures', async ({ page }) => {
  await open(page, '#/destinations', 'Destinations');
  await expect(card(page, 'Staging')).toContainText(/failed checks in a row/);
});
