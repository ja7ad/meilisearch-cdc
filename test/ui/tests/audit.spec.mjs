import { test, expect, open } from './fixtures.mjs';

test('audit log records actions and filters them', async ({ page }) => {
  await open(page, '#/tasks/products-pg', 'Products catalog');
  await page.locator('[data-action="pause"]').click();
  await expect(page.locator('h1 + [data-state]')).toHaveAttribute('data-state', 'paused');
  await open(page, '#/settings/audit', 'Settings');
  const table = page.getByTestId('audit-table');
  await expect(table.locator('tbody tr').first()).toContainText('task.pause');
  await expect(table.locator('tbody tr').first()).toContainText('admin@example.com');
  await page.getByTestId('audit-action').selectOption('login.failed');
  await expect(table.locator('tbody tr')).toHaveCount(1);
  await expect(table).toContainText('root@example.com');
  await page.getByTestId('audit-action').selectOption('');
  await page.getByTestId('audit-user').selectOption('mod@example.com');
  await expect(table.locator('tbody tr')).toHaveCount(1);
});
