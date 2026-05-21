// Step 9: index settings and per-instance Meilisearch features, driven in
// the GUI and verified on the instance itself. Works on the Replica.
import { test, expect, login, lab, api, meiliHeaders, meiliCount } from './helpers.mjs';

test.describe.configure({ mode: 'serial' });
let replica; // lab info
let destId;  // dashboard id of the Replica destination
const UID = 'e2e_settings';
const UID_B = 'e2e_settings_b';

// Every Meilisearch index setting, with non-default values.
const ALL_SETTINGS = {
  searchableAttributes: ['name', 'brand', 'category'],
  displayedAttributes: ['*'],
  filterableAttributes: ['category', { attributePatterns: ['price'], features: { facetSearch: false, filter: { equality: true, comparison: true } } }],
  sortableAttributes: ['price', 'rating'],
  distinctAttribute: 'name',
  rankingRules: ['words', 'typo', 'proximity', 'attribute', 'sort', 'exactness', 'rating:desc'],
  typoTolerance: { enabled: true, minWordSizeForTypos: { oneTypo: 4, twoTypos: 9 }, disableOnWords: ['acme'], disableOnAttributes: ['brand'], disableOnNumbers: true },
  synonyms: { lamp: ['light'], light: ['lamp'] },
  stopWords: ['the', 'a'],
  dictionary: ['e-reader'],
  separatorTokens: ['|'],
  nonSeparatorTokens: ['@'],
  faceting: { maxValuesPerFacet: 50, sortFacetValuesBy: { '*': 'count' } },
  pagination: { maxTotalHits: 500 },
  proximityPrecision: 'byAttribute',
  searchCutoffMs: 150,
  prefixSearch: 'disabled',
  facetSearch: false,
  localizedAttributes: [{ attributePatterns: ['name'], locales: ['eng'] }],
  embedders: { manual: { source: 'userProvided', dimensions: 3 } },
};

const meiliGet = async (request, path) => (await request.get(`${replica.url}${path}`, { headers: meiliHeaders(replica) })).json();

test.beforeAll(async ({ request }) => {
  replica = (await lab(request)).meilisearch[1];
  for (const uid of [UID, UID_B]) await request.delete(`${replica.url}/indexes/${uid}`, { headers: meiliHeaders(replica) });
});

test('create indexes on a chosen instance', async ({ page, request }) => {
  await login(page);
  destId = (await api(page, 'GET', '/api/v1/destinations')).json.find((d) => d.name === 'Replica').id;
  for (const uid of [UID, UID_B]) {
    await page.goto('/#/indexes');
    await page.getByRole('button', { name: 'Create index' }).first().click();
    const d = page.getByRole('dialog');
    await d.locator('#ci-dest').selectOption(destId);
    await d.locator('#ci-uid').fill(uid);
    await d.locator('#ci-pk').fill('id');
    await d.getByRole('button', { name: /Create/ }).last().click();
    await expect(page.getByRole('dialog')).toHaveCount(0);
  }
  await expect(page.locator(`[data-instance="${destId}"]`)).toContainText(UID_B);
  expect((await meiliGet(request, `/indexes/${UID}`)).primaryKey).toBe('id');
  const primary = (await lab(request)).meilisearch[0];
  expect((await request.get(`${primary.url}/indexes/${UID}`, { headers: meiliHeaders(primary) })).status()).toBe(404);

  // Data to search: the replicated MySQL catalogue, copied for this test.
  // UID carries user-provided vectors for the "manual" embedder; UID_B has none.
  const docs = (await meiliGet(request, '/indexes/e2e_mysql/documents?limit=1000')).results;
  const withVectors = docs.map((d) => ({ ...d, _vectors: { manual: [Number(d.price) / 100, Number(d.rating) / 5, (d.id % 10) / 10] } }));
  await request.post(`${replica.url}/indexes/${UID}/documents`, { headers: meiliHeaders(replica), data: withVectors });
  await request.post(`${replica.url}/indexes/${UID_B}/documents`, { headers: meiliHeaders(replica), data: docs.slice(0, 10) });
  await expect.poll(() => meiliCount(request, replica, UID)).toBe(docs.length);
});

test('every index setting is previewed, applied and live on the instance', async ({ page, request }) => {
  await login(page);
  await page.goto(`/#/indexes/${UID}/settings?destination=${destId}`);
  await page.getByTestId('ix-editor').getByRole('tab', { name: 'JSON' }).click();
  await page.getByTestId('index-settings-json').fill(JSON.stringify(ALL_SETTINGS, null, 2));
  await page.getByTestId('preview-settings').click();
  const changes = page.getByTestId('settings-changes');
  for (const key of Object.keys(ALL_SETTINGS).filter((k) => k !== 'displayedAttributes')) await expect(changes).toContainText(key);
  await page.getByTestId('apply-settings').click();
  await expect(page.getByText(/Settings applied|Settings update enqueued/).first()).toBeVisible({ timeout: 60_000 });

  await expect.poll(async () => (await meiliGet(request, `/indexes/${UID}/settings`)).searchCutoffMs, { timeout: 60_000 }).toBe(150);
  const live = await meiliGet(request, `/indexes/${UID}/settings`);
  expect(live.searchableAttributes).toEqual(ALL_SETTINGS.searchableAttributes);
  expect(live.filterableAttributes).toEqual(expect.arrayContaining(['category']));
  expect(JSON.stringify(live.filterableAttributes)).toContain('"comparison":true');
  expect(live.sortableAttributes.sort()).toEqual(['price', 'rating']);
  expect(live.distinctAttribute).toBe('name');
  expect(live.rankingRules).toEqual(ALL_SETTINGS.rankingRules);
  expect(live.typoTolerance).toMatchObject({ minWordSizeForTypos: { oneTypo: 4, twoTypos: 9 }, disableOnWords: ['acme'], disableOnNumbers: true });
  expect(live.synonyms).toEqual(ALL_SETTINGS.synonyms);
  expect(live.stopWords.sort()).toEqual(['a', 'the']);
  expect(live.dictionary).toEqual(['e-reader']);
  expect(live.separatorTokens).toEqual(['|']);
  expect(live.nonSeparatorTokens).toEqual(['@']);
  expect(live.faceting).toMatchObject({ maxValuesPerFacet: 50, sortFacetValuesBy: { '*': 'count' } });
  expect(live.pagination.maxTotalHits).toBe(500);
  expect(live.proximityPrecision).toBe('byAttribute');
  expect(live.prefixSearch).toBe('disabled');
  expect(live.facetSearch).toBe(false);
  expect(live.localizedAttributes).toEqual(ALL_SETTINGS.localizedAttributes);
  expect(live.embedders.manual).toMatchObject({ source: 'userProvided', dimensions: 3 });

  // Nothing left to change.
  await page.getByTestId('preview-settings').click();
  await expect(page.getByText('The index already has these settings.')).toBeVisible();
});

test('a settings update Meilisearch rejects is reported with its reason', async ({ page, request }) => {
  await login(page);
  await page.goto(`/#/indexes/${UID_B}/settings?destination=${destId}`);
  await page.getByTestId('ix-editor').getByRole('tab', { name: 'JSON' }).click();
  // Documents without vectors cannot get a user-provided embedder.
  await page.getByTestId('index-settings-json').fill(JSON.stringify({ embedders: { manual: { source: 'userProvided', dimensions: 3 } } }));
  await page.getByTestId('preview-settings').click();
  await page.getByTestId('apply-settings').click();
  await expect(page.getByText('Settings update failed')).toBeVisible({ timeout: 60_000 });
  await expect(page.getByText(/no vectors provided/).first()).toBeVisible();
  expect((await meiliGet(request, `/indexes/${UID_B}/settings`)).embedders).toEqual({});
});

test('the settings form edits ranking rules and synonyms', async ({ page, request }) => {
  await login(page);
  await page.goto(`/#/indexes/${UID}/settings?destination=${destId}`);
  const editor = page.getByTestId('ix-editor');
  await editor.getByRole('tab', { name: 'Form' }).click();
  await editor.getByText('Ranking rules', { exact: true }).first().click();
  await editor.getByRole('button', { name: 'Move exactness up' }).click();
  await editor.getByText('Synonyms', { exact: true }).first().click();
  await editor.getByLabel('New synonym word').fill('kettle');
  const list = editor.getByLabel(/synonyms of the new word|Synonyms list|New synonyms/i).first();
  await list.fill('teapot');
  await list.press('Enter');
  await editor.getByTestId('ix-syn-addbtn').click();
  await page.getByTestId('preview-settings').click();
  await expect(page.getByTestId('settings-changes')).toContainText('rankingRules');
  await expect(page.getByTestId('settings-changes')).toContainText('synonyms');
  await page.getByTestId('apply-settings').click();
  await expect.poll(async () => (await meiliGet(request, `/indexes/${UID}/settings`)).synonyms.kettle, { timeout: 60_000 }).toEqual(['teapot']);
  const rules = (await meiliGet(request, `/indexes/${UID}/settings`)).rankingRules;
  expect(rules.indexOf('exactness')).toBeLessThan(rules.indexOf('sort'));
});

test('search playground: synonyms, filters, sort and the documents browser', async ({ page }) => {
  await login(page);
  await page.goto(`/#/indexes/${UID}?destination=${destId}`);
  await page.getByTestId('search-q').fill('light');
  await expect(page.getByTestId('search-hits')).toContainText('Lamp'); // via the lamp/light synonym
  await page.getByTestId('search-q').fill('');
  await page.getByTestId('search-filter').fill('price >= 90');
  await expect(page.getByTestId('search-total')).not.toHaveText('0');
  await page.getByLabel('Sort (comma separated field:asc|desc)').fill('price:desc');
  await expect(page.getByTestId('search-hits').locator('.hit').first()).toContainText('Classic Racket 96'); // price 96.99, the highest
  await page.getByRole('tab', { name: 'Documents' }).click();
  await expect(page.getByTestId('documents-tab').locator('tbody tr').first()).toBeVisible();
});

test('compact, swap and delete indexes on the instance', async ({ page, request }) => {
  await login(page);
  await page.goto(`/#/indexes/${UID}?destination=${destId}`);
  await page.getByRole('button', { name: 'Compact' }).click();
  await expect(page.getByText('Compaction enqueued')).toBeVisible();

  const before = { a: await meiliCount(request, replica, UID), b: await meiliCount(request, replica, UID_B) };
  await page.goto('/#/indexes');
  await page.getByRole('button', { name: 'Swap' }).click();
  const d = page.getByRole('dialog');
  await d.locator('#sw-dest').selectOption(destId);
  await d.locator('#sw-a').selectOption(UID);
  await d.locator('#sw-b').selectOption(UID_B);
  await d.getByTestId('swap-confirm').click();
  const ok = page.getByRole('dialog').getByTestId('confirm-ok');
  if (await ok.isVisible({ timeout: 1500 }).catch(() => false)) await ok.click();
  await expect.poll(() => meiliCount(request, replica, UID)).toBe(before.b);
  expect(await meiliCount(request, replica, UID_B)).toBe(before.a);

  await page.goto('/#/indexes');
  await page.locator(`[data-instance="${destId}"] [data-index="${UID_B}"]`).getByRole('button', { name: `Delete index ${UID_B}` }).click();
  const del = page.getByRole('dialog');
  const typed = del.getByRole('textbox');
  if (await typed.count()) await typed.fill(UID_B);
  await del.getByTestId('confirm-ok').click();
  await expect.poll(async () => (await request.get(`${replica.url}/indexes/${UID_B}`, { headers: meiliHeaders(replica) })).status()).toBe(404);
});

test('instance features: experimental toggles and snapshots apply to that instance only', async ({ page, request }) => {
  await login(page);
  const primary = (await lab(request)).meilisearch[0];
  await page.goto(`/#/destinations/${destId}/experimental`);
  const toggle = page.locator('#exp-containsFilter');
  const was = (await meiliGet(request, '/experimental-features')).containsFilter;
  await toggle.setChecked(!was);
  await expect.poll(async () => (await meiliGet(request, '/experimental-features')).containsFilter).toBe(!was);
  const onPrimary = await (await request.get(`${primary.url}/experimental-features`, { headers: meiliHeaders(primary) })).json();
  expect(onPrimary.containsFilter).toBe(was);
  await toggle.setChecked(was);
  await expect.poll(async () => (await meiliGet(request, '/experimental-features')).containsFilter).toBe(was);

  await page.goto(`/#/destinations/${destId}/backups`);
  await page.getByTestId('create-snapshot').click();
  await expect.poll(async () => (await meiliGet(request, '/tasks?types=snapshotCreation&statuses=succeeded&limit=1')).results.length, { timeout: 60_000 }).toBe(1);
});

test('Meilisearch tasks page filters by index on the chosen instance', async ({ page }) => {
  await login(page);
  destId ||= (await api(page, 'GET', '/api/v1/destinations')).json.find((d) => d.name === 'Replica').id;
  await page.goto('/#/meili-tasks');
  await page.getByTestId('mt-instance').selectOption(destId);
  await page.getByTestId('mt-index').selectOption(UID);
  await expect(page.getByTestId('meili-tasks-table')).toContainText(/settingsUpdate/);
  await expect(page.getByTestId('meili-tasks-table')).not.toContainText('e2e_postgres');
});
