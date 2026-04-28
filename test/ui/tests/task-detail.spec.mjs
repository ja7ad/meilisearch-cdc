import { test, expect, open } from './fixtures.mjs';

test('tabs render and live counters change via SSE', async ({ page }) => {
  await open(page, '#/tasks/products-pg', 'Products catalog');
  const upserted = page.locator('[data-counter="upserted"]');
  await expect(upserted).toBeVisible();
  const first = await upserted.textContent();
  await expect.poll(async () => upserted.textContent(), { timeout: 8000 }).not.toBe(first);
  await expect(page.getByRole('img', { name: 'Documents per second' })).toBeVisible();
  await expect(page.getByTestId('checkpoint')).toContainText('0/');

  await page.getByRole('tab', { name: 'Logs' }).click();
  await expect(page).toHaveURL(/\/logs$/);
  const box = page.getByTestId('log-box');
  await expect(box.locator('.log-line').first()).toBeVisible();
  const n = await box.locator('.log-line').count();
  await expect.poll(() => box.locator('.log-line').count(), { timeout: 8000 }).toBeGreaterThan(n);
  await page.getByRole('group', { name: 'Minimum level' }).getByRole('button', { name: 'warn' }).click();
  await expect(box.locator('.log-line')).toHaveCount(1);

  await page.getByRole('tab', { name: 'Schema check' }).click();
  await expect(page.getByTestId('schema-fields').locator('tbody tr').first()).toBeVisible();
  await expect(page.getByTestId('schema-check')).toContainText('consistent');

  await page.getByRole('tab', { name: 'Diff' }).click();
  await expect(page.getByTestId('diff-tab')).toContainText('Out of sync');
  await expect(page.getByTestId('mismatch').first()).toContainText('price');

  await page.getByRole('tab', { name: /DLQ/ }).click();
  await expect(page.getByTestId('dlq-tab').locator('tbody tr')).toHaveCount(4);

  await page.getByRole('tab', { name: 'Config' }).click();
  await expect(page.getByLabel('Task definition', { exact: true })).toContainText('"id": "products-pg"');
  await expect(page.getByLabel('Task definition', { exact: true })).toContainText('••••••');

  // keyboard navigation between tabs
  await page.getByRole('tab', { name: 'Config' }).press('ArrowLeft');
  await expect(page.getByRole('tab', { name: 'Index settings' })).toHaveAttribute('aria-selected', 'true');
});

test('snapshot progress moves', async ({ page }) => {
  await open(page, '#/tasks/orders-mysql', 'Orders');
  const bar = page.getByTestId('snapshot-progress').getByRole('progressbar');
  const v = Number(await bar.getAttribute('aria-valuenow'));
  await expect.poll(async () => Number(await bar.getAttribute('aria-valuenow')), { timeout: 8000 }).toBeGreaterThan(v);
});

test('run diff and repair', async ({ page }) => {
  await open(page, '#/tasks/users-mongo/diff', 'Users directory');
  await expect(page.getByText('No diff yet')).toBeVisible();
  await page.getByTestId('run-diff').click();
  await expect(page.getByText('Comparing…')).toBeVisible();
  await expect(page.getByTestId('diff-tab')).toContainText('Out of sync', { timeout: 10000 });
  await page.getByRole('button', { name: 'Repair missing & mismatched' }).click();
  await expect(page.getByText('Repair done')).toBeVisible();
  await expect(page.getByTestId('diff-tab')).toContainText('In sync');
});

test('DLQ: view document, replay and discard', async ({ page }) => {
  await open(page, '#/tasks/events-kafka/dlq', 'Inventory events');
  const rows = page.getByTestId('dlq-tab').locator('tbody tr');
  await expect(rows).toHaveCount(12);
  await page.getByRole('button', { name: 'View document 1000' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText('invalid_document_id');
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await page.getByRole('checkbox', { name: 'Select dead letter 1000' }).check();
  await page.getByRole('checkbox', { name: 'Select dead letter 1017' }).check();
  await page.getByRole('button', { name: 'Discard 2' }).click();
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByText('Discarded 2')).toBeVisible();
  await expect(rows).toHaveCount(10);
});

test('actions: pause, resume, and delete with typed confirmation', async ({ page }) => {
  await open(page, '#/tasks/products-pg', 'Products catalog');
  await page.locator('[data-action="pause"]').click();
  await expect(page.locator('h1 + [data-state]')).toHaveAttribute('data-state', 'paused');
  await page.locator('[data-action="resume"]').click();
  await expect(page.locator('h1 + [data-state]')).toHaveAttribute('data-state', 'streaming');

  await page.locator('[data-action="delete"]').click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await page.locator('[data-action="delete"]').click();
  await expect(dialog.getByTestId('confirm-ok')).toBeDisabled();
  await dialog.getByLabel(/Type/).fill('products');
  await expect(dialog.getByTestId('confirm-ok')).toBeDisabled();
  await dialog.getByLabel(/Type/).fill('products-pg');
  await dialog.getByTestId('confirm-ok').click();
  await expect(page.getByText('Task deleted')).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: 'Tasks' })).toBeVisible();
});

test('reset checkpoint on a stopped task', async ({ page }) => {
  await open(page, '#/tasks/legacy-poll', 'Legacy items');
  await page.locator('[data-action="reset-checkpoint"]').click();
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByText('Reset checkpoint: Legacy items')).toBeVisible();
  await expect(page.getByTestId('checkpoint')).toContainText('none');
});
