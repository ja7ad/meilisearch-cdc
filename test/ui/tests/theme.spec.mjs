import { test, expect, open } from './fixtures.mjs';

test('theme toggle switches and persists across reloads', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' });
  await open(page, '#/overview', 'Overview');
  const html = page.locator('html');
  await expect(html).toHaveAttribute('data-theme', 'light');
  const bgLight = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  await page.getByTestId('theme-toggle').click();
  await expect(html).toHaveAttribute('data-theme', 'dark');
  await expect.poll(() => page.evaluate(() => getComputedStyle(document.body).backgroundColor)).not.toBe(bgLight);
  expect(await page.evaluate(() => localStorage.getItem('cdc.theme'))).toBe('dark');
  await page.reload();
  await expect(html).toHaveAttribute('data-theme', 'dark');
  await page.getByRole('button', { name: 'Switch to light theme' }).click();
  await expect(html).toHaveAttribute('data-theme', 'light');
});

test('follows the OS preference when nothing is stored', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await open(page, '#/overview', 'Overview');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
});

test('settings page theme buttons', async ({ page }) => {
  await open(page, '#/settings', 'Settings');
  await page.getByRole('group', { name: 'Theme' }).getByRole('button', { name: 'Dark' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
});
