import { test, expect, open } from './fixtures.mjs';

test('settings preview lists changes, then apply', async ({ page }) => {
  await open(page, '#/indexes/products/settings', 'products');
  await page.getByTestId('ix-editor').getByRole('tab', { name: 'JSON' }).click();
  const ta = page.getByTestId('index-settings-json');
  await expect(ta).toHaveValue(/filterableAttributes/);
  const cur = JSON.parse(await ta.inputValue());
  cur.filterableAttributes = ['category', 'brand', 'in_stock', 'price'];
  cur.stopWords = ['the', 'a'];
  await ta.fill(JSON.stringify(cur, null, 2));
  await page.getByTestId('preview-settings').click();
  const changes = page.getByTestId('settings-changes');
  await expect(changes.locator('.change')).toHaveCount(2);
  await expect(changes).toContainText('filterableAttributes');
  await expect(changes).toContainText('stopWords');
  await page.getByTestId('apply-settings').click();
  await expect(page.getByText('Settings applied')).toBeVisible();
  await page.getByTestId('preview-settings').click();
  await expect(page.getByText('The index already has these settings.')).toBeVisible();
});

test('invalid settings JSON is reported inline', async ({ page }) => {
  await open(page, '#/indexes/products/settings', 'products');
  await page.getByTestId('ix-editor').getByRole('tab', { name: 'JSON' }).click();
  await page.getByTestId('index-settings-json').fill('{ nope');
  await expect(page.getByRole('alert').filter({ hasText: /Invalid JSON/ })).toBeVisible();
});

test('search playground returns results as you type', async ({ page }) => {
  await open(page, '#/indexes/products', 'products');
  await expect(page.getByTestId('search-hits').locator('.hit').first()).toBeVisible();
  await page.getByTestId('search-q').fill('cobalt');
  await expect.poll(async () => Number((await page.getByTestId('search-total').textContent()).replace(/,/g, ''))).toBeLessThan(1000);
  await expect(page.getByTestId('search-hits').locator('.hit').first()).toContainText(/cobalt/i);
  await expect(page.getByTestId('search-time')).toHaveText(/\d+/);
  await page.getByTestId('search-q').fill('');
  await page.getByTestId('search-filter').fill('category = Audio');
  await expect(page.getByTestId('search-hits').locator('.hit').first()).toContainText('Audio');
  await page.getByTestId('search-filter').fill('category ~ Audio');
  await expect(page.getByText('Search error')).toBeVisible();
});

test('documents browser paginates', async ({ page }) => {
  await open(page, '#/indexes/movies/documents', 'movies');
  const tab = page.getByTestId('documents-tab');
  await expect(tab).toContainText('1–20 of 31,944');
  await tab.getByRole('button', { name: 'Next page' }).click();
  await expect(tab).toContainText('21–40 of 31,944');
});

test('delete index requires typing the uid', async ({ page }) => {
  await open(page, '#/indexes', 'Indexes');
  await expect(page.locator('[data-index="movies"]')).toBeVisible();
  await page.getByRole('button', { name: 'Delete index movies' }).click();
  const dialog = page.getByRole('dialog');
  const ok = dialog.getByTestId('confirm-ok');
  await expect(ok).toBeDisabled();
  await dialog.getByLabel(/Type/).fill('movie');
  await expect(ok).toBeDisabled();
  await dialog.getByLabel(/Type/).fill('movies');
  await expect(ok).toBeEnabled();
  await ok.click();
  await expect(page.getByText('Index deletion enqueued')).toBeVisible();
  await expect(page.locator('[data-index="movies"]')).toHaveCount(0);
});

test('deleting an index written by a running task is refused', async ({ page }) => {
  await open(page, '#/indexes/products', 'products');
  await page.getByTestId('delete-index').click();
  await page.getByRole('dialog').getByLabel(/Type/).fill('products');
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByText(/writing to this index/)).toBeVisible();
});

test('create and swap indexes', async ({ page }) => {
  await open(page, '#/indexes', 'Indexes');
  await page.getByRole('button', { name: 'Create index' }).click();
  await page.getByLabel('Index uid').fill('scratch');
  await page.getByRole('dialog').getByRole('button', { name: 'Create' }).click();
  await expect(page.getByText('Index “scratch” created')).toBeVisible();
  await expect(page.locator('[data-index="scratch"]')).toBeVisible();
  await page.getByRole('button', { name: 'Swap' }).click();
  await page.getByLabel('First index', { exact: true }).selectOption('scratch');
  await page.getByLabel('Second index', { exact: true }).selectOption('movies');
  await page.getByTestId('swap-confirm').click();
  await expect(page.getByText('Swapped scratch and movies')).toBeVisible();
});

test('primary key change is refused when the index has documents', async ({ page }) => {
  await open(page, '#/indexes/orders', 'orders');
  await page.getByRole('button', { name: 'Edit primary key' }).click();
  await page.getByLabel('Primary key', { exact: true }).fill('order_id');
  await page.getByRole('button', { name: 'Save primary key' }).click();
  await expect(page.getByText('Index has documents')).toBeVisible();
});

test('indexes are grouped per Meilisearch instance with a URL filter', async ({ page }) => {
  await open(page, '#/indexes', 'Indexes');
  await expect(page.locator('[data-instance]')).toHaveCount(3);
  await expect(page.locator('[data-instance="eu-replica"] [data-index]')).toHaveCount(2);
  await expect(page.locator('[data-instance="staging"]')).toContainText('Unreachable');
  await page.getByTestId('instance-filter').selectOption('eu-replica');
  await expect(page).toHaveURL(/#\/indexes\?instance=eu-replica$/);
  await expect(page.locator('[data-instance]')).toHaveCount(1);
  // detail links carry the destination; API calls pass it
  const req = page.waitForRequest((r) => /\/api\/v1\/indexes\/users\?destination=eu-replica/.test(r.url()));
  await page.locator('[data-instance="eu-replica"]').getByRole('link', { name: 'users', exact: true }).click();
  await req;
  await expect(page).toHaveURL(/#\/indexes\/users\?destination=eu-replica/);
  await expect(page.getByTestId('scope-badge')).toContainText('EU replica');
  // create asks for the instance, prefilled from the filter
  await page.goto('/#/indexes?instance=eu-replica');
  await page.getByRole('button', { name: 'Create index' }).click();
  const dialog = page.getByRole('dialog', { name: 'Create index' });
  await expect(dialog.getByLabel('Meilisearch instance')).toHaveValue('eu-replica');
  await dialog.getByLabel('Index uid').fill('eu_scratch');
  const create = page.waitForRequest((r) => r.method() === 'POST' && /\/api\/v1\/indexes\?destination=eu-replica$/.test(r.url()));
  await dialog.getByRole('button', { name: 'Create' }).click();
  await create;
  await expect(page.locator('[data-instance="eu-replica"] [data-index="eu_scratch"]')).toBeVisible();
});

test('single instance mode keeps the flat list', async ({ page }) => {
  await page.request.post('/__mock/single?on=1');
  await open(page, '#/indexes', 'Indexes');
  await expect(page.locator('[data-instance]')).toHaveCount(0);
  await expect(page.getByTestId('index-list')).toBeVisible();
  await expect(page.getByTestId('instance-filter')).toHaveCount(0);
});
