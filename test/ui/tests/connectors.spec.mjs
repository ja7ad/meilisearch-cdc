import { test, expect, open } from './fixtures.mjs';

const card = (page, name) => page.locator('[data-connector]').filter({ hasText: name });

test('list, filter, nav badge', async ({ page, consoleErrors }) => {
  await open(page, '#/connectors', 'Connectors');
  await expect(page.getByTestId('nav-badge-connectors')).toHaveText('8');
  await expect(page.locator('[data-connector]')).toHaveCount(8);
  await expect(card(page, 'Shop Postgres')).toContainText('db.internal:5432');
  await expect(card(page, 'Shop Postgres')).not.toContainText('s3cret');
  await expect(card(page, 'Shop Postgres')).toContainText('Products catalog');
  await expect(card(page, 'Analytics Kafka')).toContainText('Not used by any task');
  await page.getByTestId('connector-type-filter').selectOption('kafka');
  await expect(page.locator('[data-connector]')).toHaveCount(2);
  await page.getByTestId('connector-type-filter').selectOption('');
  await page.getByLabel('Search connectors').fill('mysql.internal');
  await expect(page.locator('[data-connector]')).toHaveCount(1);
  expect(consoleErrors).toEqual([]);
});

test('create, test, edit keeping the masked password, delete', async ({ page }) => {
  await open(page, '#/connectors', 'Connectors');
  await page.getByTestId('new-connector').click();
  const dialog = page.getByRole('dialog', { name: 'New connector' });
  await dialog.locator('[data-source="postgres"]').click();
  await page.getByTestId('connector-save').click();
  await expect(dialog.getByText('Give the connector a name')).toBeVisible();
  await expect(dialog.getByText('Connection string is required')).toBeVisible();
  await dialog.getByLabel('Name').fill('Reporting replica');
  await dialog.getByLabel('Tags').fill('reporting');
  await dialog.getByLabel('Connection string (DSN)').fill('postgres://ro:fail@replica:5432/shop');
  await page.getByTestId('connector-test-btn').click();
  await expect(dialog.getByTestId('connector-test')).toContainText('connection refused');
  await dialog.getByLabel('Connection string (DSN)').fill('postgres://ro:pw@replica.internal:5432/shop');
  await page.getByTestId('connector-test-btn').click();
  const ok = dialog.getByTestId('connector-test');
  await expect(ok).toContainText('PostgreSQL 16.4');
  await expect(ok).toContainText(/\d+ ms/);
  await expect(ok).toContainText('9 tables discovered');
  await page.getByTestId('connector-save').click();
  await expect(page.getByText('Connector created')).toBeVisible();
  await expect(page.getByTestId('nav-badge-connectors')).toHaveText('9');
  const c = card(page, 'Reporting replica');
  await expect(c).toContainText('replica.internal:5432');

  await c.getByRole('button', { name: 'Test Reporting replica' }).click();
  await expect(c.getByTestId('connector-last-test')).toHaveText('OK');

  await c.getByRole('button', { name: 'Edit Reporting replica' }).click();
  const edit = page.getByRole('dialog', { name: 'Edit Reporting replica' });
  await expect(edit.getByLabel('Connection string (DSN)')).toHaveValue('postgres://ro:••••••@replica.internal:5432/shop');
  await edit.getByLabel('Name').fill('Reporting replica (EU)');
  const req = page.waitForRequest((r) => r.method() === 'PUT' && /\/api\/v1\/connectors\//.test(r.url()));
  await page.getByTestId('connector-save').click();
  expect((await req).postDataJSON().config.dsn).toContain('••••••');
  await expect(page.getByText('Connector saved')).toBeVisible();

  await card(page, 'Reporting replica (EU)').getByRole('button', { name: 'Delete Reporting replica (EU)' }).click();
  const confirm = page.getByRole('dialog');
  await expect(confirm.getByTestId('confirm-ok')).toBeDisabled();
  await confirm.getByLabel(/Type/).fill('Reporting replica (EU)');
  await confirm.getByTestId('confirm-ok').click();
  await expect(page.getByText('Connector deleted')).toBeVisible();
  await expect(page.locator('[data-connector]')).toHaveCount(8);
});

test('delete is refused while tasks use the connector', async ({ page }) => {
  await open(page, '#/connectors/c_shop_pg', 'Shop Postgres (primary)');
  await expect(page.getByTestId('connector-used-by')).toContainText('Products catalog');
  await page.getByTestId('connector-delete').click();
  await page.getByRole('dialog').getByLabel(/Type/).fill('Shop Postgres (primary)');
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByRole('alert').filter({ hasText: 'Connector is in use' })).toContainText('Used by Products catalog');
  await expect(page.getByRole('heading', { level: 1, name: 'Shop Postgres (primary)' })).toBeVisible();
});

test('table browser → create task pre-filled with connector and table', async ({ page }) => {
  await open(page, '#/connectors/c_shop_pg', 'Shop Postgres (primary)');
  await page.getByTestId('connector-detail-test').click();
  await expect(page.getByTestId('connector-test')).toContainText('PostgreSQL 16.4');
  const tables = page.getByTestId('tables-table');
  await expect(tables.locator('tbody tr')).toHaveCount(9);
  await page.getByTestId('tables-search').fill('review');
  await expect(tables.locator('tbody tr')).toHaveCount(1);
  await page.getByRole('link', { name: 'Create task for reviews' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'New task' })).toBeVisible();
  await expect(page.getByTestId('wizard-connector')).toContainText('Shop Postgres (primary)');
  await expect(page.getByRole('textbox', { name: 'Table', exact: true })).toHaveValue('reviews');
  await expect(page.getByLabel('Connection string (DSN)')).toHaveCount(0);
  await expect(page.getByLabel('Replication slot')).toBeVisible();
  await page.getByTestId('test-connection').click();
  await expect(page.getByTestId('test-result')).toContainText('Connected');
  for (let i = 0; i < 4; i++) await page.getByTestId('wizard-next').click();
  await expect(page.getByText('Everything looks valid.')).toBeVisible();
  const req = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/v1/tasks'));
  await page.getByTestId('create-task').click();
  const body = (await req).postDataJSON();
  expect(body.source).toEqual({ connectorId: 'c_shop_pg', schema: 'public', table: 'reviews' });
  await expect(page.getByText('Task created')).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: 'Reviews' })).toBeVisible();
});

test('wizard: pick a saved connector and a table from the picker', async ({ page }) => {
  await open(page, '#/tasks/new', 'New task');
  await page.locator('[data-connector="c_shop_mysql"]').click();
  await page.getByTestId('wizard-next').click();
  await page.getByLabel('Name').fill('Payments');
  await page.getByRole('textbox', { name: 'Table', exact: true }).fill('pay');
  await page.getByRole('group', { name: 'Tables in this connector' }).getByRole('button', { name: /payments/ }).click();
  await expect(page.getByRole('textbox', { name: 'Table', exact: true })).toHaveValue('payments');
  for (let i = 0; i < 4; i++) await page.getByTestId('wizard-next').click();
  await expect(page.getByText('Everything looks valid.')).toBeVisible();
  const req = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/v1/tasks'));
  await page.getByTestId('create-task').click();
  expect((await req).postDataJSON().source).toEqual({ connectorId: 'c_shop_mysql', table: 'payments' });
  await expect(page.getByRole('heading', { level: 1, name: 'Payments' })).toBeVisible();
});

test.describe('viewer', () => {
  test.use({ role: 'viewer' });
  test('browses connectors without test or edit controls', async ({ page }) => {
    await open(page, '#/connectors', 'Connectors');
    await expect(page.getByTestId('new-connector')).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^Test / })).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^Edit / })).toHaveCount(0);
    await open(page, '#/connectors/c_shop_pg', 'Shop Postgres (primary)');
    await expect(page.getByTestId('connector-detail-test')).toHaveCount(0);
    await expect(page.getByTestId('tables-table').locator('tbody tr')).toHaveCount(9);
    await expect(page.getByRole('link', { name: /Create task for/ })).toHaveCount(0);
  });
});

test.describe('moderator', () => {
  test.use({ role: 'moderator' });
  test('can test but not edit', async ({ page }) => {
    await open(page, '#/connectors', 'Connectors');
    await card(page, 'Analytics Kafka').getByRole('button', { name: 'Test Analytics Kafka' }).click();
    await expect(card(page, 'Analytics Kafka').getByTestId('connector-last-test')).toHaveText('OK');
    await expect(page.getByRole('button', { name: /^Edit / })).toHaveCount(0);
    await expect(page.getByTestId('new-connector')).toHaveCount(0);
  });
});
