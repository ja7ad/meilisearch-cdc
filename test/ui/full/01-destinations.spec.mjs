// Step 1 of the full suite: both Meilisearch instances become destinations
// through the dashboard.
import { test, expect, login, lab } from './helpers.mjs';

test.describe.configure({ mode: 'serial' });

test('add both Meilisearch instances as destinations', async ({ page, request }) => {
  const info = await lab(request);
  await login(page);
  for (const [i, m] of info.meilisearch.entries()) {
    await page.goto('/#/destinations');
    const card = page.locator('[data-destination]', { hasText: m.name });
    if (await card.count()) continue; // kept from an earlier run
    await page.getByTestId('new-destination').click();
    const dialog = page.getByRole('dialog');
    await dialog.locator('#ds-name').fill(m.name);
    await dialog.locator('#ds-url').fill(m.url);
    await dialog.locator('#ds-key').fill(m.apiKey);
    await dialog.locator('#ds-interval').selectOption(i === 0 ? '10s' : '5s');
    await dialog.getByTestId('destination-test-btn').click();
    await expect(dialog.getByTestId('destination-test')).toContainText('Connected');
    await dialog.getByTestId('destination-save').click();
    await expect(page.getByRole('dialog')).toHaveCount(0);
  }
  await page.goto('/#/destinations');
  for (const m of info.meilisearch) {
    await expect(page.locator('[data-destination]', { hasText: m.name })).not.toHaveClass(/is-down/);
  }
  await expect(page.locator('[data-destination]', { hasText: 'Primary' }).getByTestId('default-badge')).toBeVisible();
});
