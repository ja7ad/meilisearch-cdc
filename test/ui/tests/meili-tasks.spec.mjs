import { test, expect, open } from './fixtures.mjs';

test('filters by status and index, shows metadata, cancels enqueued tasks', async ({ page }) => {
  await open(page, '#/meili-tasks', 'Meilisearch tasks');
  const table = page.getByTestId('meili-tasks-table');
  await expect(table.locator('.meta-chip').first()).toContainText('cdc:');
  await page.getByRole('switch', { name: 'Auto refresh' }).uncheck();
  await page.locator('[data-status="enqueued"]').click();
  await expect(table.locator('tbody tr')).toHaveCount(3);
  await page.getByRole('checkbox', { name: 'Select all cancellable tasks' }).check();
  await page.getByTestId('cancel-selected').click();
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByText('Cancelation enqueued')).toBeVisible();
  await expect(page.getByText('No tasks')).toBeVisible();
  await page.locator('[data-status="enqueued"]').click();
  await page.locator('[data-status="canceled"]').click();
  await expect(table.locator('tbody tr')).toHaveCount(3);
  await page.locator('[data-status="canceled"]').click();
  await page.getByTestId('mt-index').selectOption('movies');
  await expect(table.locator('tbody tr').first()).toContainText('movies');
});

test('load more paginates', async ({ page }) => {
  await open(page, '#/meili-tasks', 'Meilisearch tasks');
  await page.getByRole('switch', { name: 'Auto refresh' }).uncheck();
  const rows = page.getByTestId('meili-tasks-table').locator('tbody tr');
  await expect(rows).toHaveCount(50);
  await page.getByRole('button', { name: 'Load more' }).click();
  await expect.poll(() => rows.count()).toBeGreaterThan(50);
});
