import { test, expect, open, newSession } from './fixtures.mjs';

test.describe('viewer', () => {
  test.use({ role: 'viewer' });
  test('sees no mutating controls', async ({ page, consoleErrors }) => {
    await open(page, '#/tasks', 'Tasks');
    await expect(page.getByRole('link', { name: 'New task' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Import' })).toHaveCount(0);
    await expect(page.getByRole('checkbox', { name: 'Select all visible tasks' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Stop Products catalog' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Export' })).toBeVisible();

    await open(page, '#/tasks/products-pg', 'Products catalog');
    await expect(page.locator('[data-action]')).toHaveCount(0);
    await expect(page.getByText('read only')).toBeVisible();
    await page.getByRole('tab', { name: 'Diff' }).click();
    await expect(page.getByTestId('run-diff')).toBeDisabled();
    await expect(page.getByTestId('run-diff')).toHaveAttribute('title', 'Requires moderator role');
    await page.getByRole('tab', { name: /DLQ/ }).click();
    await expect(page.getByRole('button', { name: 'Replay all' })).toBeDisabled();

    await open(page, '#/tasks/new', 'New task');
    await expect(page.getByText('Not available for your role')).toBeVisible();

    await open(page, '#/indexes', 'Indexes');
    await expect(page.getByRole('button', { name: 'Create index' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: /Delete index/ })).toHaveCount(0);
    await open(page, '#/indexes/products/settings', 'products');
    await expect(page.getByTestId('preview-settings')).toHaveCount(0);
    await expect(page.getByTestId('ix-editor')).toBeVisible();
    await expect(page.getByTestId('index-settings-json')).toHaveValue(/filterableAttributes/);

    await open(page, '#/meili-tasks', 'Meilisearch tasks');
    await expect(page.getByRole('checkbox', { name: 'Select all cancellable tasks' })).toHaveCount(0);

    await open(page, '#/alerts', 'Alerts');
    await expect(page.getByTestId('new-rule')).toHaveCount(0);
    await expect(page.getByRole('switch', { name: /Enable rule/ }).first()).toBeDisabled();

    await open(page, '#/settings', 'Settings');
    await expect(page.getByRole('tab')).toHaveCount(0);
    await open(page, '#/destinations/default/experimental', 'Production');
    await expect(page.getByRole('switch', { name: /containsFilter/ })).toBeDisabled();
    await open(page, '#/destinations/default/backups', 'Production');
    await expect(page.getByTestId('create-dump')).toBeDisabled();
    await open(page, '#/settings/users', 'Settings');
    await expect(page.getByText('Not available for your role')).toBeVisible();
    await expect(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'New task' })).toHaveCount(0);
    expect(consoleErrors).toEqual([]);
  });
});

test.describe('moderator', () => {
  test.use({ role: 'moderator' });
  test('operates tasks but cannot define them', async ({ page }) => {
    await open(page, '#/tasks', 'Tasks');
    await expect(page.getByRole('link', { name: 'New task' })).toHaveCount(0);
    await page.getByRole('checkbox', { name: 'Select Orders' }).check();
    const bar = page.getByTestId('batch-bar');
    await expect(bar.getByRole('button', { name: 'Stop selected tasks' })).toBeVisible();
    await expect(bar.getByRole('button', { name: 'Delete selected tasks' })).toHaveCount(0);
    await expect(page.getByRole('link', { name: 'Edit Orders' })).toHaveCount(0);

    await open(page, '#/tasks/products-pg', 'Products catalog');
    await expect(page.locator('[data-action="pause"]')).toBeVisible();
    await expect(page.locator('[data-action="delete"]')).toHaveCount(0);
    await expect(page.getByRole('link', { name: 'Edit' })).toHaveCount(0);
    await page.locator('[data-action="pause"]').click();
    await expect(page.locator('h1 + [data-state]')).toHaveAttribute('data-state', 'paused');
    await page.getByRole('tab', { name: 'Diff' }).click();
    await expect(page.getByTestId('run-diff')).toBeEnabled();

    await open(page, '#/settings', 'Settings');
    await expect(page.getByRole('tab')).toHaveCount(0);
    await open(page, '#/tasks/products-pg/edit', 'Edit task');
    await expect(page.getByText('Not available for your role')).toBeVisible();
  });

  test('a 403 after a role change is reported', async ({ page, browser, baseURL }) => {
    await open(page, '#/tasks', 'Tasks');
    const { context, page: admin } = await newSession(browser, baseURL, 'admin');
    const users = await (await admin.request.get('/api/v1/users')).json();
    const mod = users.find((u) => u.role === 'moderator');
    await admin.request.patch(`/api/v1/users/${mod.id}`, { data: { role: 'viewer' } });
    await context.close();
    await page.getByRole('button', { name: 'Stop Products catalog' }).click();
    await page.getByRole('dialog').getByTestId('confirm-ok').click();
    await expect(page.getByRole('alert').filter({ hasText: 'Not allowed: requires the moderator role' })).toBeVisible();
  });
});
