import { test, expect, open } from './fixtures.mjs';

const rows = (page) => page.getByTestId('tasks-table').locator('tbody tr');

test('filters by search, state, source and tag', async ({ page }) => {
  await open(page, '#/tasks', 'Tasks');
  await expect(rows(page)).toHaveCount(7);
  await page.getByTestId('task-search').fill('orders');
  await expect(rows(page)).toHaveCount(1);
  await expect(rows(page).first()).toContainText('Orders');
  await page.getByRole('button', { name: 'Clear' }).click();
  await page.getByTestId('filter-state').selectOption('streaming');
  await expect(rows(page)).toHaveCount(1);
  await expect(rows(page).first()).toContainText('Products catalog');
  await page.getByTestId('filter-state').selectOption('');
  await page.getByTestId('filter-source').selectOption('kafka');
  await expect(rows(page)).toHaveCount(1);
  await page.getByTestId('filter-source').selectOption('');
  await page.getByTestId('filter-tag').selectOption('shop');
  await expect(rows(page)).toHaveCount(2);
  await page.getByTestId('task-search').fill('zzz-nothing');
  await expect(page.getByText('No matching tasks')).toBeVisible();
});

test('multi-select shows the batch bar and a batch action reports a toast', async ({ page }) => {
  await open(page, '#/tasks', 'Tasks');
  await expect(page.getByTestId('batch-bar')).toHaveCount(0);
  await page.getByRole('checkbox', { name: 'Select Products catalog' }).check();
  await page.getByRole('checkbox', { name: 'Select Orders' }).check();
  const bar = page.getByTestId('batch-bar');
  await expect(bar).toBeVisible();
  await expect(bar).toContainText('2 selected');
  await bar.getByRole('button', { name: 'Stop selected tasks' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Stop: 2 of 2 succeeded' })).toBeVisible();
  await expect(page.locator('[data-task-id="products-pg"] [data-state]')).toHaveAttribute('data-state', 'stopped');
  await expect(page.locator('[data-task-id="orders-mysql"] [data-state]')).toHaveAttribute('data-state', 'stopped');
});

test('partial batch failures are reported', async ({ page }) => {
  await open(page, '#/tasks', 'Tasks');
  await page.getByRole('checkbox', { name: 'Select all visible tasks' }).check();
  await expect(page.getByTestId('batch-bar')).toContainText('7 selected');
  await page.getByTestId('batch-bar').getByRole('button', { name: 'Pause selected tasks' }).click();
  // only the 3 running tasks can be paused
  await expect(page.getByText(/Pause: 3 of 7 succeeded/)).toBeVisible();
});

test('batch delete needs typed confirmation', async ({ page }) => {
  await open(page, '#/tasks', 'Tasks');
  await page.getByRole('checkbox', { name: 'Select Legacy items' }).check();
  await page.getByTestId('batch-bar').getByRole('button', { name: 'Delete selected tasks' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  const ok = dialog.getByTestId('confirm-ok');
  await expect(ok).toBeDisabled();
  await dialog.getByLabel(/Type/).fill('delete 1');
  await ok.click();
  await expect(page.getByText('Delete: 1 of 1 succeeded')).toBeVisible();
  await expect(rows(page)).toHaveCount(6);
});

test('row quick action starts a stopped task', async ({ page }) => {
  await open(page, '#/tasks', 'Tasks');
  await page.getByRole('button', { name: 'Start Legacy items' }).click();
  await expect(page.getByText('Start: Legacy items')).toBeVisible();
  await expect(page.locator('[data-task-id="legacy-poll"] [data-state]')).not.toHaveAttribute('data-state', 'stopped');
});

test('export downloads JSON and import creates tasks', async ({ page }) => {
  await open(page, '#/tasks', 'Tasks');
  const [dl] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Export' }).click()]);
  expect(dl.suggestedFilename()).toMatch(/meilisearch-cdc-tasks-.*\.json/);
  const payload = JSON.stringify([{ name: 'Imported one', source: { type: 'postgres', dsn: 'postgres://x@y/z', table: 'things' }, index: { uid: 'things' } }, { name: '' }]);
  await page.getByLabel('Import tasks JSON file').setInputFiles({ name: 'tasks.json', mimeType: 'application/json', buffer: Buffer.from(payload) });
  await expect(page.getByText('Imported 1 task')).toBeVisible();
  await expect(rows(page)).toHaveCount(8);
});
