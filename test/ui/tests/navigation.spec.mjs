import { test, expect, open, PAGES } from './fixtures.mjs';

test.describe('navigation', () => {
  for (const p of PAGES) {
    test(`renders ${p.name} without console errors`, async ({ page, consoleErrors }) => {
      await open(page, p.hash, p.heading);
      // let live data, charts and staggered animations settle
      await page.waitForTimeout(1200);
      await expect(page.locator('.skeleton-table')).toHaveCount(0);
      expect(consoleErrors, consoleErrors.join('\n')).toEqual([]);
    });
  }

  test('sidebar links reach every section', async ({ page }) => {
    await open(page, '#/overview', 'Overview');
    const nav = page.getByRole('navigation', { name: 'Main' });
    for (const [label, heading, hash] of [
      ['Tasks', 'Tasks', '#/tasks'], ['Indexes', 'Indexes', '#/indexes'],
      ['Meilisearch tasks', 'Meilisearch tasks', '#/meili-tasks'], ['Settings', 'Settings', '#/settings'], ['Overview', 'Overview', '#/overview'],
    ]) {
      const link = nav.getByRole('link', { name: new RegExp(`^${label}`) });
      await link.click();
      await expect(page).toHaveURL(new RegExp(`${hash.replace('/', '\\/')}$`));
      await expect(page.getByRole('heading', { level: 1, name: heading })).toBeVisible();
      await expect(link).toHaveAttribute('aria-current', 'page');
    }
  });

  test('task cards and table rows link to the detail page', async ({ page }) => {
    await open(page, '#/overview', 'Overview');
    await page.getByTestId('task-card').filter({ hasText: 'Products catalog' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Products catalog' })).toBeVisible();
    await page.getByRole('link', { name: 'Tasks', exact: true }).first().click();
    await page.getByRole('link', { name: 'Orders', exact: true }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Orders' })).toBeVisible();
  });

  test('unknown routes show a not found page', async ({ page }) => {
    await page.goto('/#/nope');
    await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible();
  });

  test('sidebar collapses and remembers it', async ({ page }) => {
    await open(page, '#/overview', 'Overview');
    await page.getByRole('button', { name: 'Collapse sidebar' }).click();
    await expect(page.locator('.shell')).toHaveClass(/is-collapsed/);
    await page.reload();
    await expect(page.locator('.shell')).toHaveClass(/is-collapsed/);
    await page.getByRole('button', { name: 'Expand sidebar' }).click();
    await expect(page.locator('.shell')).not.toHaveClass(/is-collapsed/);
  });
});
