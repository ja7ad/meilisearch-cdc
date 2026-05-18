// Step 7: every section of the dashboard against the real data the source
// tasks produced (11 tasks over two Meilisearch instances).
import { test, expect, login, lab, api, taskId } from './helpers.mjs';

test.describe.configure({ mode: 'serial' });

test('overview shows every task and both instances', async ({ page }) => {
  await login(page);
  await page.goto('/#/overview');
  await expect(page.getByTestId('task-card')).toHaveCount(11);
  await expect(page.getByTestId('destinations-strip')).toContainText('Primary');
  await expect(page.getByTestId('destinations-strip')).toContainText('Replica');
  await expect(page.getByTestId('activity-feed')).not.toBeEmpty();
});

test('tasks list: search and filters', async ({ page }) => {
  await login(page);
  await page.goto('/#/tasks');
  const rows = page.getByTestId('tasks-table').locator('tbody tr');
  await expect(rows).toHaveCount(11);
  await page.getByTestId('task-search').fill('mongo');
  await expect(rows).toHaveCount(1);
  await page.getByTestId('task-search').fill('');
  await page.getByTestId('filter-source').selectOption('kafka');
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText('E2E kafka products');
});

test('task detail tabs: logs, schema check, diff, DLQ and index settings', async ({ page }) => {
  await login(page);
  const id = await taskId(page, 'E2E postgres products');
  await page.goto(`/#/tasks/${id}`);
  await expect(page.getByTestId('task-connector')).toContainText('E2E postgres');
  await expect(page.getByTestId('checkpoint')).not.toBeEmpty();

  await page.getByRole('tab', { name: 'Logs' }).click();
  await expect(page.getByTestId('log-box')).toContainText(/stream/i);

  await page.getByRole('tab', { name: 'Schema check' }).click();
  await expect(page.getByTestId('schema-fields').locator('tbody tr', { hasText: 'category' })).toContainText('filterable');

  await page.getByRole('tab', { name: 'Diff' }).click();
  await page.getByTestId('run-diff').click();
  await expect(page.getByTestId('diff-tab')).toContainText('In sync', { timeout: 60_000 });

  await page.getByRole('tab', { name: 'DLQ' }).click();
  await expect(page.getByTestId('dlq-tab')).toBeVisible();

  await page.getByRole('tab', { name: 'Index settings' }).click();
  await expect(page.getByTestId('task-settings')).toContainText('category');
});

test('indexes are grouped per instance; search runs against the right one', async ({ page, request }) => {
  const info = await lab(request);
  await login(page);
  const dests = (await api(page, 'GET', '/api/v1/destinations')).json;
  const primary = page.locator(`[data-instance="${dests.find((d) => d.name === 'Primary').id}"]`);
  const replica = page.locator(`[data-instance="${dests.find((d) => d.name === 'Replica').id}"]`);
  await page.goto('/#/indexes');
  await expect(page.locator('[data-instance]')).toHaveCount(2);
  await expect(primary).toContainText('e2e_postgres');
  await expect(replica).toContainText('e2e_mysql');
  await expect(primary).not.toContainText('e2e_mysql');

  await replica.getByRole('link', { name: 'e2e_mysql' }).click();
  await page.getByTestId('search-q').fill('Renamed');
  await expect(page.getByTestId('search-hits')).toContainText('Renamed mysql product');
  await page.getByTestId('search-q').fill('');
  await page.getByTestId('search-filter').fill('category = books');
  await expect(page.getByTestId('search-total')).toHaveText(String(info.rows / 6));
});

test('Meilisearch tasks per instance carry the CDC metadata', async ({ page }) => {
  await login(page);
  await page.goto('/#/meili-tasks');
  await expect(page.getByTestId('meili-tasks-table')).toContainText(/cdc:/);
  await page.getByTestId('mt-instance').selectOption({ label: 'Replica' });
  await expect(page.getByTestId('meili-tasks-table')).toContainText(/e2e_(mysql|dynamodb|sqlserver|kafka|sqlpoll)/);
});

test('destination page: health, synced tasks, backups and experimental features', async ({ page, request }) => {
  await login(page);
  const dests = (await api(page, 'GET', '/api/v1/destinations')).json;
  const replica = dests.find((d) => d.name === 'Replica');
  expect(replica.usedBy.length).toBeGreaterThanOrEqual(5);
  await page.goto(`/#/destinations/${replica.id}`);
  await expect(page.getByTestId('destination-health')).toHaveClass(/is-up/);
  await expect(page.getByTestId('synced-tasks').locator('tbody tr')).toHaveCount(replica.usedBy.length);
  await page.getByRole('tab', { name: 'Backups' }).click();
  await page.getByTestId('create-dump').click();
  await expect(page.getByText(/dump/i).first()).toBeVisible();
  await page.getByRole('tab', { name: 'Experimental' }).click();
  await expect(page.getByTestId('experimental-features')).toBeVisible();
  await page.getByRole('tab', { name: 'Settings' }).click();
  await expect(page.locator('#ds-interval')).toHaveValue('5s');
});

test('connector page browses tables and shows the tasks using it', async ({ page }) => {
  await login(page);
  await page.goto('/#/connectors');
  await expect(page.getByTestId('connector-list').locator('[data-connector]')).toHaveCount(11);
  await page.locator('[data-connector]', { hasText: 'E2E postgres' }).getByRole('link').first().click();
  await expect(page.getByTestId('tables-table')).toContainText('products');
  await expect(page.getByTestId('connector-used-by')).toContainText('E2E postgres products');
  await page.getByTestId('connector-detail-test').click();
  await expect(page.getByTestId('connector-test')).toContainText('Connected');
  await page.goto('/#/connectors');
  await expect(page.locator('[data-connector]', { hasText: 'E2E postgres' }).getByTestId('connector-last-test')).toContainText('OK');
});

test('settings: general and the audit log', async ({ page }) => {
  await login(page);
  await page.goto('/#/settings');
  await expect(page.getByTestId('instances-summary')).toContainText('Replica');
  await page.goto('/#/settings/audit');
  await expect(page.getByTestId('audit-table')).toContainText(/POST \/api\/v1\/tasks|auth\.login/);
});
