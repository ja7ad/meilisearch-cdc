import { test, expect, open } from './fixtures.mjs';

// The editor on the index Settings tab (prefix "ix").
const editor = (page) => page.getByTestId('ix-editor');
const json = async (page) => JSON.parse((await page.getByTestId('index-settings-json').inputValue()) || '{}');
/** Poll the JSON tab until fn(settings) passes (the textarea follows the form asynchronously). */
const until = (page, fn) => expect.poll(async () => { try { return fn(await json(page)); } catch { return undefined; } });

test('form and JSON stay in sync both ways', async ({ page }) => {
  await open(page, '#/indexes/movies/settings', 'movies');
  const ed = editor(page);
  await ed.locator('[data-section="words"] summary').click();
  await ed.getByLabel('Stop words').fill('the');
  await ed.getByLabel('Stop words').press('Enter');
  await until(page, (j) => j.stopWords).toEqual(['the']);
  await expect(ed.locator('[data-section="words"]')).toHaveClass(/is-changed/);

  await ed.getByRole('tab', { name: 'JSON' }).click();
  const cur = await json(page);
  cur.sortableAttributes = ['year', 'title'];
  await page.getByTestId('index-settings-json').fill(JSON.stringify(cur, null, 2));
  await ed.getByRole('tab', { name: 'Form' }).click();
  await expect(ed.getByLabel('Sortable attributes')).toBeVisible();
  await expect(ed.locator('[data-section="attributes"] .chips-input').filter({ hasText: 'title' })).toBeVisible();
  await page.getByTestId('preview-settings').click();
  await expect(page.getByTestId('settings-changes')).toContainText('sortableAttributes');
  await expect(page.getByTestId('settings-changes')).toContainText('stopWords');
});

test('granular filterable attributes', async ({ page }) => {
  await open(page, '#/indexes/movies/settings', 'movies');
  const ed = editor(page);
  await ed.getByLabel('Advanced filter settings for genres').check();
  await ed.getByLabel('genres: facet search').check();
  await ed.getByLabel('genres: comparison filters').check();
  await until(page, (j) => j.filterableAttributes).toEqual([{ attributePatterns: ['genres'], features: { facetSearch: true, filter: { equality: true, comparison: true } } }, 'year']);
  await ed.getByLabel('Advanced filter settings for genres').uncheck();
  await until(page, (j) => j.filterableAttributes[0]).toBe('genres');
});

test('ranking rules reorder and custom rules', async ({ page }) => {
  await open(page, '#/indexes/movies/settings', 'movies');
  const ed = editor(page);
  await ed.locator('[data-section="ranking"] summary').click();
  await ed.getByRole('button', { name: 'Move typo up' }).click();
  await until(page, (j) => j.rankingRules.slice(0, 2)).toEqual(['typo', 'words']);
  await ed.getByLabel('Custom rule field').fill('year');
  await ed.getByRole('button', { name: 'Add custom rule' }).click();
  await until(page, (j) => j.rankingRules[j.rankingRules.length - 1]).toBe('year:desc');
  await ed.getByRole('button', { name: 'Move year:desc up' }).click();
  await until(page, (j) => j.rankingRules[j.rankingRules.length - 2]).toBe('year:desc');
});

test('synonyms: mutual helper writes both directions', async ({ page }) => {
  await open(page, '#/indexes/movies/settings', 'movies');
  const ed = editor(page);
  await ed.locator('[data-section="synonyms"] summary').click();
  await ed.getByLabel('New synonym word').fill('film');
  await ed.getByLabel('New synonyms').fill('movie');
  await ed.getByLabel('New synonyms').press('Enter');
  await ed.getByLabel('New synonyms').fill('picture');
  await ed.getByLabel('New synonyms').press('Enter');
  await page.getByTestId('ix-syn-addbtn').click();
  await until(page, (j) => j.synonyms.film.slice().sort()).toEqual(['movie', 'picture']);
  const syn = (await json(page)).synonyms;
  expect(syn.movie.slice().sort()).toEqual(['film', 'picture']);
  expect(syn.picture.slice().sort()).toEqual(['film', 'movie']);
});

test('embedder form', async ({ page }) => {
  await open(page, '#/indexes/movies/settings', 'movies');
  const ed = editor(page);
  await ed.locator('[data-section="embedders"] summary').click();
  await expect(ed.getByText('Embedders call external services')).toBeVisible();
  await ed.getByLabel('New embedder name').fill('semantic');
  await page.getByTestId('ix-emb-add').click();
  await ed.getByLabel('Source').selectOption('rest');
  await ed.getByLabel('URL').fill('https://embed.example.com/v1');
  await ed.getByLabel('Dimensions').fill('768');
  await ed.getByLabel('Document template').fill('{{doc.title}}: {{doc.overview}}');
  await ed.getByLabel('Request (JSON)').fill('{"input": "{{text}}"}');
  await ed.getByLabel('Response (JSON)').fill('{"data": "{{embedding}}"}');
  await ed.getByLabel('Distribution mean').fill('0.7');
  await until(page, (j) => j.embedders.semantic.distribution.mean).toBe(0.7);
  const e = (await json(page)).embedders.semantic;
  expect(e).toEqual(expect.objectContaining({ source: 'rest', url: 'https://embed.example.com/v1', dimensions: 768, documentTemplate: '{{doc.title}}: {{doc.overview}}', request: { input: '{{text}}' }, response: { data: '{{embedding}}' } }));
});

test('task settings tab: drift, apply now, import live', async ({ page }) => {
  await open(page, '#/tasks/articles-maria/settings', 'Blog articles');
  const tab = page.getByTestId('task-settings');
  await expect(tab).toContainText('in sync');
  // edit the desired settings: add a sortable attribute, save → drift
  await tab.getByLabel('Sortable attributes').fill('published_at');
  await tab.getByLabel('Sortable attributes').press('Enter');
  await expect(tab.getByText('unsaved changes')).toBeVisible();
  await page.getByTestId('save-task-settings').click();
  await expect(page.getByText('Task settings saved')).toBeVisible();
  await expect(tab).toContainText('drift · 1');
  await expect(page.getByTestId('drift-changes')).toContainText('sortableAttributes');
  await page.getByTestId('apply-now').click();
  await expect(page.getByText(/Settings applied/)).toBeVisible();
  await expect(tab).toContainText('in sync');
  // import live replaces desired with the full live settings
  await page.getByTestId('import-live').click();
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByText('Live settings imported into the task')).toBeVisible();
  await expect(page.getByTestId('task-settings-json')).toHaveValue(/"rankingRules"/);
});

test('invalid settings are rejected with 422', async ({ page }) => {
  await open(page, '#/tasks/articles-maria/settings', 'Blog articles');
  const tab = page.getByTestId('task-settings');
  await tab.getByRole('tab', { name: 'JSON' }).click();
  await page.getByTestId('task-settings-json').fill('{"filterableAttributes":["author"],"notASetting":1}');
  await page.getByTestId('save-task-settings').click();
  await expect(page.getByRole('alert').filter({ hasText: 'Invalid index settings' })).toContainText('notASetting');
});
