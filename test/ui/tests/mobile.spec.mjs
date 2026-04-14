import { test, expect, open, PAGES } from './fixtures.mjs';

test.use({ viewport: { width: 375, height: 812 }, hasTouch: true });

test('drawer navigation works on a phone', async ({ page }) => {
  await open(page, '#/overview', 'Overview');
  const nav = page.getByRole('navigation', { name: 'Main' });
  await expect(nav).not.toBeInViewport();
  const menu = page.getByTestId('menu-button');
  await expect(menu).toBeVisible();
  await menu.click();
  await expect(menu).toHaveAttribute('aria-expanded', 'true');
  await expect(nav).toBeInViewport();
  await nav.getByRole('link', { name: 'Indexes' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Indexes' })).toBeVisible();
  await expect(nav).not.toBeInViewport();
  // Escape and the backdrop close it too
  await menu.click();
  await expect(nav).toBeInViewport();
  await page.keyboard.press('Escape');
  await expect(nav).not.toBeInViewport();
  await menu.click();
  await page.mouse.click(360, 400);
  await expect(nav).not.toBeInViewport();
});

for (const p of PAGES) {
  test(`no horizontal page overflow: ${p.name}`, async ({ page }) => {
    await open(page, p.hash, p.heading);
    await page.waitForTimeout(800);
    const w = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(w).toBeLessThanOrEqual(375);
  });
}
