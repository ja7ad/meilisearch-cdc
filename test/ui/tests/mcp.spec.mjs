import { test, expect, open } from './fixtures.mjs';

test('enable toggle, endpoint and client snippet', async ({ page }) => {
  await open(page, '#/settings/mcp', 'Settings');
  await expect(page.getByTestId('mcp-endpoint')).toHaveValue(/\/mcp$/);
  await expect(page.getByTestId('mcp-snippet')).toContainText('"Authorization": "Bearer <token>"');
  const sw = page.getByRole('switch', { name: 'Disabled' });
  await sw.check();
  await expect(page.getByText('MCP server enabled')).toBeVisible();
  await page.reload();
  await expect(page.getByRole('switch', { name: 'Enabled' })).toBeChecked();
});

test('generate shows the token once; rotate; revoke', async ({ page }) => {
  await open(page, '#/settings/mcp', 'Settings');
  await expect(page.getByTestId('mcp-tokens').locator('tbody tr')).toHaveCount(2);
  await page.getByTestId('mcp-generate').click();
  const dialog = page.getByRole('dialog', { name: 'Generate MCP token' });
  await dialog.getByLabel('Name').fill('Cursor (alice)');
  await dialog.getByLabel('Role').selectOption('moderator');
  await dialog.getByLabel('Expires').selectOption('168h');
  await page.getByTestId('token-generate').click();
  const reveal = page.getByTestId('token-reveal');
  await expect(reveal).toContainText('only time the token is shown');
  const token = await page.getByTestId('mcp-token-value').inputValue();
  expect(token).toMatch(/^cdcmcp_/);
  await expect(reveal).toContainText(`Bearer ${token}`);
  await page.getByTestId('token-done').click();
  await expect(page.getByText(token)).toHaveCount(0);
  const row = page.locator('[data-token="Cursor (alice)"]');
  await expect(row).toContainText(`${token.slice(0, 11)}…`);
  await expect(row).toContainText('moderator');

  await row.getByRole('button', { name: 'Rotate Cursor (alice)' }).click();
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByRole('dialog', { name: 'Token rotated' })).toBeVisible();
  const rotated = await page.getByTestId('mcp-token-value').inputValue();
  expect(rotated).not.toBe(token);
  await page.getByTestId('token-done').click();

  await row.getByRole('button', { name: 'Revoke Cursor (alice)' }).click();
  const confirm = page.getByRole('dialog');
  await expect(confirm.getByTestId('confirm-ok')).toBeDisabled();
  await confirm.getByLabel(/Type/).fill('Cursor (alice)');
  await confirm.getByTestId('confirm-ok').click();
  await expect(page.getByText('Token revoked')).toBeVisible();
  await expect(row).toHaveCount(0);
});

test('access matrix: save, warn on mutating viewer tools, reset', async ({ page }) => {
  await open(page, '#/settings/mcp', 'Settings');
  const matrix = page.getByTestId('matrix-tools');
  const cell = matrix.getByRole('switch', { name: 'Allow task_action for Viewer' });
  await expect(cell).not.toBeChecked();
  await expect(page.getByTestId('mcp-save')).toBeDisabled();
  await cell.check();
  await expect(page.getByTestId('mcp-risky')).toContainText('task_action');
  await matrix.getByRole('switch', { name: 'Allow delete_task for Moderator' }).check();
  await page.getByTestId('mcp-save').click();
  await expect(page.getByText('Access policy saved')).toBeVisible();
  await page.reload();
  await expect(page.getByTestId('matrix-tools').getByRole('switch', { name: 'Allow task_action for Viewer' })).toBeChecked();
  await page.getByTestId('matrix-resources').getByRole('switch', { name: 'Allow cdc://docs/api for Viewer' }).uncheck();
  await expect(page.getByTestId('mcp-unsaved')).toContainText('Unsaved changes');
  await page.getByTestId('mcp-reset').click();
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByText('Policy reset to defaults')).toBeVisible();
  await expect(page.getByTestId('matrix-tools').getByRole('switch', { name: 'Allow task_action for Viewer' })).not.toBeChecked();
  await expect(page.getByTestId('matrix-resources').getByRole('switch', { name: 'Allow cdc://docs/api for Viewer' })).toBeChecked();
  await expect(page.getByTestId('mcp-risky')).toHaveCount(0);
});

test('matrix: select all per role and per group, no overflow', async ({ page }) => {
  await open(page, '#/settings/mcp', 'Settings');
  const m = page.getByTestId('matrix-tools');
  await m.getByRole('switch', { name: 'Allow all Operate tools for Viewer' }).check();
  await expect(m.getByRole('switch', { name: 'Allow task_action for Viewer' })).toBeChecked();
  await expect(m.getByRole('switch', { name: 'Allow all tools for Viewer' })).toHaveAttribute('aria-checked', 'mixed');
  await m.getByRole('switch', { name: 'Allow all tools for Admin' }).uncheck();
  await expect(m.getByRole('switch', { name: 'Allow list_tasks for Admin' })).not.toBeChecked();
  await expect(page.getByTestId('mcp-unsaved')).toBeVisible();
  const w = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
  expect(w[0]).toBeLessThanOrEqual(w[1]);
  // body cells are not styled like the header
  const tt = await m.locator('.matrix-item').first().evaluate((el) => getComputedStyle(el).textTransform);
  expect(tt).toBe('none');
});
