import { test, expect, open } from './fixtures.mjs';

const CREDENTIAL_LABELS = [/Connection string/i, /DSN/, /AWS region/i, /Access key/i, /Secret access key/i, /Brokers/i, /password/i];
async function expectNoCredentials(page) {
  for (const l of CREDENTIAL_LABELS) await expect(page.locator('#main').getByLabel(l)).toHaveCount(0);
}

test('create task wizard: connector, source, mapping, index, create', async ({ page }) => {
  await open(page, '#/tasks/new', 'New task');
  const next = page.getByTestId('wizard-next');
  await expect(next).toBeDisabled();
  await expect(page.getByRole('link', { name: /Manage connectors/ })).toBeVisible();
  await page.locator('[data-connector="c_shop_pg"]').click();
  await expect(page.locator('[data-connector="c_shop_pg"]')).toHaveAttribute('aria-pressed', 'true');
  await next.click();

  // source step: validation first, never credentials
  await expect(page.getByTestId('wizard-connector')).toContainText('Shop Postgres (primary)');
  await expectNoCredentials(page);
  await next.click();
  await expect(page.getByText('Name is required')).toBeVisible();
  await expect(page.getByText('Table is required')).toBeVisible();
  await page.getByLabel('Name').fill('Catalog v2');
  await page.getByRole('group', { name: 'Tables in this connector' }).getByRole('button', { name: /^products/ }).click();
  await expect(page.getByRole('textbox', { name: 'Table', exact: true })).toHaveValue('products');
  await expect(page.getByText('Name is required')).toHaveCount(0);
  await page.getByTestId('test-connection').click();
  const result = page.getByTestId('test-result');
  await expect(result).toContainText('Connected');
  await expect(page.getByTestId('schema-table').locator('tbody tr')).toHaveCount(15);
  await next.click();

  // mapping
  await expect(page.getByTestId('mapping-columns')).toBeVisible();
  await page.getByRole('group', { name: 'Primary key columns' }).getByRole('button', { name: 'sku' }).click();
  await page.getByRole('checkbox', { name: 'Exclude description' }).check();
  await page.getByLabel('Rename name').fill('title');
  await page.getByLabel('Soft delete column').selectOption('deleted_at');
  await page.getByLabel('Geo latitude column').selectOption('lat');
  await page.getByLabel('Geo longitude column').selectOption('lng');
  await page.getByRole('group', { name: 'Time format' }).getByRole('button', { name: 'unix', exact: true }).click();
  await page.getByRole('button', { name: 'Add static field' }).click();
  await page.getByLabel('Static field 1 name').fill('tenant');
  await page.getByLabel('Static field 1 value').fill('"acme"');
  await next.click();

  // index
  await expect(page.getByLabel('Index uid')).toHaveValue('products');
  await page.getByLabel('Index uid').fill('catalog_v2');
  await page.getByRole('checkbox', { name: 'Filter: category' }).check();
  await page.getByRole('checkbox', { name: 'Sort: price' }).check();
  await expect(page.getByTestId('settings-json')).toHaveValue(/"filterableAttributes": \[\s*"category"\s*\]/);
  await page.getByText('Manage index settings').click();
  await page.getByText('Upsert', { exact: true }).click();
  await next.click();

  // sync
  await page.getByLabel('Batch size').fill('0');
  await next.click();
  await expect(page.getByText('Must be a positive integer')).toBeVisible();
  await page.getByLabel('Batch size').fill('500');
  await page.getByLabel('Flush interval').fill('250ms');
  await next.click();

  // review
  await expect(page.getByText('Everything looks valid.')).toBeVisible();
  const json = page.getByLabel('Task JSON', { exact: true });
  await expect(json).toContainText('"connectorId": "c_shop_pg"');
  await expect(json).toContainText('"uid": "catalog_v2"');
  await expect(json).toContainText('"tenant": "acme"');
  await expect(json).not.toContainText('"dsn"');
  await page.getByTestId('create-start-task').click();
  await expect(page.getByText('Task created and started')).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: 'Catalog v2' })).toBeVisible();
  await expect(page.getByTestId('task-connector')).toContainText('Shop Postgres (primary)');
});

test('new connector from the wizard is created and selected in place', async ({ page }) => {
  await open(page, '#/tasks/new', 'New task');
  await page.getByTestId('wizard-new-connector').click();
  const dialog = page.getByRole('dialog', { name: 'New connector' });
  await dialog.locator('[data-source="postgres"]').click();
  await dialog.getByLabel('Name').fill('Broken replica');
  await dialog.getByLabel('Connection string (DSN)').fill('postgres://ro:pw@fail.internal:5432/shop');
  await page.getByTestId('connector-save').click();
  await expect(page.getByText('Connector selected')).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: 'New task' })).toBeVisible();
  const card = page.locator('[data-connector]').filter({ hasText: 'Broken replica' });
  await expect(card).toHaveAttribute('aria-pressed', 'true');
  await page.getByTestId('wizard-next').click();
  await expect(page.getByTestId('wizard-connector')).toContainText('Broken replica');
  await page.getByRole('textbox', { name: 'Table', exact: true }).fill('orders');
  await page.getByTestId('test-connection').click();
  await expect(page.getByRole('alert').filter({ hasText: 'Connection failed' })).toContainText('connection refused');
});

test('source options follow the connector type; Change goes back', async ({ page }) => {
  await open(page, '#/tasks/new', 'New task');
  await page.locator('[data-connector="c_inventory_kafka"]').click();
  await page.getByTestId('wizard-next').click();
  await expect(page.getByLabel('Topic')).toBeVisible();
  await expect(page.getByLabel('Consumer group')).toBeVisible();
  await expectNoCredentials(page);
  await page.getByTestId('change-connector').click();
  await page.locator('[data-connector="c_aws_dynamo"]').click();
  await page.getByTestId('wizard-next').click();
  await expect(page.getByLabel('DynamoDB table')).toBeVisible();
  await expect(page.getByLabel('Consumer group')).toHaveCount(0);
  await expectNoCredentials(page);
  await page.getByTestId('change-connector').click();
  await page.locator('[data-connector="c_legacy_sqlite"]').click();
  await page.getByTestId('wizard-next').click();
  await expect(page.getByLabel('Cursor column')).toBeVisible();
  await expect(page.getByLabel('Poll interval')).toBeVisible();
  await expectNoCredentials(page);
});

test('edit uses the connector and only sends table fields', async ({ page }) => {
  await open(page, '#/tasks/orders-mysql/edit', 'Edit Orders');
  await expect(page.getByTestId('wizard-connector')).toContainText('Shop MySQL');
  await expectNoCredentials(page);
  await page.getByLabel('Name').fill('Orders (EU)');
  await page.locator('[data-step="review"]').click();
  await expect(page.getByText('Connector', { exact: true }).locator('xpath=following-sibling::dd[1]')).toContainText('Shop MySQL');
  const req = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/v1/tasks/orders-mysql'));
  await page.getByTestId('save-task').click();
  const body = (await req).postDataJSON();
  expect(body.name).toBe('Orders (EU)');
  expect(body.source).toEqual(expect.objectContaining({ connectorId: 'c_shop_mysql', table: 'orders' }));
  expect(body.source.dsn).toBeUndefined();
  expect(body.source.type).toBeUndefined();
  await expect(page.getByRole('heading', { level: 1, name: 'Orders (EU)' })).toBeVisible();
});

test('edit can switch the connector', async ({ page }) => {
  await open(page, '#/tasks/users-mongo/edit', 'Edit Users directory');
  await page.getByTestId('change-connector').click();
  await expect(page.locator('[data-connector="c_app_mongo"]')).toHaveAttribute('aria-pressed', 'true');
});

test('SQL polling: cursor column is suggested from the table columns', async ({ page }) => {
  await open(page, '#/tasks/new', 'New task');
  await page.locator('[data-connector="c_legacy_sqlite"]').click();
  await page.getByTestId('wizard-next').click();
  await page.getByLabel('Name').fill('Legacy 2');
  await page.getByRole('group', { name: 'Tables in this connector' }).getByRole('button', { name: /^legacy_items/ }).click();
  const cursor = page.getByLabel(/Change tracking column/);
  await expect(cursor).toHaveValue('modified_seq');
  await expect(page.getByTestId('cursor-suggested')).toBeVisible();
  await expect(page.getByText(/SQL polling has no change log/)).toBeVisible();
  await cursor.selectOption('id');
  await expect(page.getByTestId('cursor-id-warning')).toContainText("updates to existing rows won't be detected");
  await cursor.selectOption('modified_seq');
  await expect(page.getByTestId('cursor-id-warning')).toHaveCount(0);
  await expect(page.getByLabel('Poll interval')).toHaveValue('2s');
  await page.getByLabel('Poll interval').selectOption('5s');
  await page.getByLabel('Cursor column example').click();
  await expect(page.getByText(/each poll runs/)).toBeVisible();
  await page.getByLabel('Cursor column example').click();
  await page.getByTestId('goto-soft-delete').click();
  await expect(page.getByLabel('Soft delete column')).toBeFocused();
});
