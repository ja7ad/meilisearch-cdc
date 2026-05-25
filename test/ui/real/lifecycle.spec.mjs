// End-to-end dashboard flow against the real service, a real Meilisearch
// and a SQLite source (realserver/). Tests share state and run in order.
import { test as base, expect } from '@playwright/test';

const test = base.extend({
  consoleErrors: [async ({ page }, use) => {
    const errors = [];
    page.on('console', (m) => {
      // A 401 before the token is entered is expected and handled by the UI.
      if (m.type() === 'error' && !/401|Unauthorized/.test(m.text())) errors.push(m.text());
    });
    page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
    await use(errors);
    expect(errors, 'console errors').toEqual([]);
  }, { auto: true }],
});
test.describe.configure({ mode: 'serial' });

let info;
let taskUrl;
const INDEX = `products_ui_${Date.now()}`;

async function sql(request, statement) {
  const r = await request.post('/__test/sql', { data: { sql: statement } });
  expect(r.status(), await r.text()).toBe(204);
}

async function login(page) {
  await page.goto('/#/overview');
  await page.waitForURL(/#\/login/);
  await page.getByLabel('Email').fill(info.email);
  await page.getByLabel('Password').fill(info.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((u) => !u.hash.startsWith('#/login'));
  // Onboarding takes over while nothing is set up; these tests drive the pages directly.
  const dismiss = page.getByRole('button', { name: 'Dismiss onboarding' });
  const heading = page.getByRole('heading', { level: 1, name: 'Overview' });
  await expect(dismiss.or(heading)).toBeVisible();
  if (await dismiss.isVisible()) await dismiss.click();
  await expect(heading).toBeVisible();
}

let connectorId;

test.beforeAll(async ({ request }) => {
  info = await (await request.get('/__test/info')).json();
});

test('sign in protects the dashboard', async ({ page }) => {
  await page.goto('/#/tasks');
  await expect(page).toHaveURL(/#\/login\?next=/);
  await expect(page.getByRole('link', { name: /Forgot password/ })).toHaveCount(0); // no SMTP
  await page.getByLabel('Email').fill(info.email);
  await page.getByLabel('Password').fill('wrong-password');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('alert')).toBeVisible();
  await page.getByLabel('Password').fill(info.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Tasks' })).toBeVisible();
  await expect(page.getByText(/Meilisearch v\d/).first()).toBeVisible();
});

test('create a SQLite connector and browse its tables', async ({ page }) => {
  await login(page);
  await page.goto('/#/connectors');
  await page.getByRole('button', { name: /New connector/ }).first().click();
  const dialog = page.getByRole('dialog');
  await dialog.locator('[data-source="sqlpoll"]').click();
  await dialog.getByLabel('Name').fill('Shop SQLite');
  await dialog.getByLabel('Driver').selectOption('sqlite');
  await dialog.getByLabel(/Connection string/).fill(info.sqlite);
  await dialog.getByRole('button', { name: 'Test connection' }).click();
  await expect(dialog.getByText(/Connected/)).toBeVisible();
  await dialog.getByRole('button', { name: /Create connector/ }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  const card = page.locator('[data-connector]', { hasText: 'Shop SQLite' }).first();
  await expect(card).toBeVisible();
  connectorId = await card.getAttribute('data-connector');
});

test('create a SQL polling task with the wizard and watch the snapshot', async ({ page }) => {
  await login(page);
  await page.goto('/#/tasks/new');
  await page.locator(`[data-connector="${connectorId}"]`).click();
  await page.getByTestId('wizard-next').click();

  await page.getByRole('textbox', { name: 'Name', exact: true }).fill('UI products');
  await page.locator('[data-table="products"]').click();
  await expect(page.locator('#f-cursor')).toHaveValue('version'); // suggested from the schema
  await page.locator('#f-poll').selectOption('500ms');
  await page.getByTestId('test-connection').click();
  const result = page.getByTestId('test-result');
  await expect(result).toContainText('Connected');
  await expect(page.getByTestId('schema-table').locator('tbody tr')).toHaveCount(5);
  await expect(result).toContainText('Product 1');
  await page.getByTestId('wizard-next').click();

  await expect(page.getByTestId('mapping-columns')).toBeVisible();
  await page.getByRole('checkbox', { name: 'Exclude version' }).check();
  await page.getByTestId('wizard-next').click();

  await page.getByLabel('Index uid').fill(INDEX);
  await page.getByRole('checkbox', { name: 'Filter: category' }).check();
  await page.getByRole('checkbox', { name: 'Sort: price' }).check();
  await page.getByText('Manage index settings').click();
  await page.getByTestId('wizard-next').click();
  await page.getByLabel('Flush interval').fill('100ms');
  await page.getByTestId('wizard-next').click();

  await expect(page.getByText('Everything looks valid.')).toBeVisible();
  await page.getByTestId('create-start-task').click();
  await expect(page.getByRole('heading', { level: 1, name: 'UI products' })).toBeVisible();
  taskUrl = page.url();
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible();
  await expect(page.locator('[data-counter="upserted"]')).toContainText('500');
});

test('live changes flow through and show in counters and logs', async ({ page, request }) => {
  await login(page);
  await page.goto(taskUrl);
  await expect(page.locator('[data-counter="upserted"]')).toContainText('500');
  await sql(request, `INSERT INTO products VALUES (501, 'Brand new widget', 'garden', 12.5, 1001)`);
  await sql(request, `UPDATE products SET name = 'Renamed product', version = 1002 WHERE id = 7`);
  await expect(page.locator('[data-counter="upserted"]')).toContainText('502');
  await expect(page.getByTestId('checkpoint')).toContainText('1002');

  await page.getByRole('tab', { name: 'Logs' }).click();
  await expect(page.getByTestId('log-box')).toContainText('streaming');
});

test('index page: search, filter and documents come from Meilisearch', async ({ page }) => {
  await login(page);
  await page.goto('/#/indexes');
  const row = page.locator(`[data-index="${INDEX}"]`);
  await expect(row).toContainText('501');
  await row.getByRole('link', { name: INDEX }).click();
  await expect(page.getByRole('heading', { level: 1, name: INDEX })).toBeVisible();

  await page.getByTestId('search-q').fill('widget');
  await expect(page.getByTestId('search-hits').locator('.hit').first()).toContainText('Brand new widget');
  await page.getByTestId('search-q').fill('');
  await page.getByTestId('search-filter').fill('category = books');
  await expect(page.getByTestId('search-total')).toHaveText('125');
});

test('settings preview and apply against the live index', async ({ page }) => {
  await login(page);
  await page.goto(`/#/indexes/${INDEX}/settings`);
  await page.getByTestId('ix-editor').getByRole('tab', { name: 'JSON' }).click();
  const ta = page.getByTestId('index-settings-json');
  await expect(ta).toHaveValue(/category/);
  const cur = JSON.parse(await ta.inputValue());
  cur.stopWords = ['the', 'a'];
  await ta.fill(JSON.stringify(cur, null, 2));
  await page.getByTestId('preview-settings').click();
  await expect(page.getByTestId('settings-changes').locator('.change')).toHaveCount(1);
  await expect(page.getByTestId('settings-changes')).toContainText('stopWords');
  await page.getByTestId('apply-settings').click();
  await expect(page.getByText('Settings applied')).toBeVisible();
  await page.getByTestId('preview-settings').click();
  await expect(page.getByText('The index already has these settings.')).toBeVisible();
});

test('schema check and diff report a consistent index', async ({ page }) => {
  await login(page);
  await page.goto(`${taskUrl}/schema`);
  await expect(page.getByTestId('schema-fields').locator('tbody tr', { hasText: 'category' })).toContainText('filterable');

  await page.getByRole('tab', { name: 'Diff' }).click();
  await expect(page.getByText('No diff yet')).toBeVisible();
  await page.getByTestId('run-diff').click();
  await expect(page.getByTestId('diff-tab')).toContainText('In sync');
});

test('Meilisearch tasks carry the CDC task metadata', async ({ page }) => {
  await login(page);
  await page.goto('/#/meili-tasks');
  await expect(page.getByRole('heading', { level: 1, name: 'Meilisearch tasks' })).toBeVisible();
  await expect(page.getByText(/cdc:/).first()).toBeVisible();
});

test('batch stop, then delete with typed confirmation', async ({ page }) => {
  await login(page);
  await page.goto('/#/tasks');
  await page.getByRole('checkbox', { name: 'Select UI products' }).check();
  await page.getByTestId('batch-bar').getByRole('button', { name: 'Stop selected tasks' }).click();
  await expect(page.getByText('Stop: 1 of 1 succeeded')).toBeVisible();
  await expect(page.locator('tr', { hasText: 'UI products' }).locator('[data-state]')).toHaveAttribute('data-state', 'stopped');

  await page.getByRole('checkbox', { name: 'Select UI products' }).check();
  await page.getByTestId('batch-bar').getByRole('button', { name: 'Delete selected tasks' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel(/Type/).fill('delete 1');
  await dialog.getByTestId('confirm-ok').click();
  await expect(page.getByText('Delete: 1 of 1 succeeded')).toBeVisible();
  await expect(page.locator('tr', { hasText: 'UI products' })).toHaveCount(0);
});
