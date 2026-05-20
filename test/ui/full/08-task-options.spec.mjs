// Step 8: task configuration beyond the defaults, all set in the wizard and
// operated in the GUI: sync modes, write mode, mapping options, the mass
// delete guard, lifecycle actions, DLQ, diff repair, editing, batch actions
// and import/export. Uses its own Postgres tables (opt_items, guard_items).
import fs from 'node:fs';
import { test, expect, login, lab, api, sql, signInAs, ADMIN, wizardTask, taskAction, taskId, meiliDoc, meiliCount, meiliHeaders } from './helpers.mjs';

test.describe.configure({ mode: 'serial' });
const CONN = 'E2E postgres';
let primary;

test.beforeAll(async ({ request, browser, baseURL }) => {
  primary = (await lab(request)).meilisearch[0];
  // Tasks left by an earlier run of this spec.
  const { ctx, page } = await signInAs(browser, baseURL, ADMIN.email, ADMIN.password);
  for (const t of (await api(page, 'GET', '/api/v1/tasks')).json || []) {
    if (/^E2E (options|imported)/.test(t.name)) await api(page, 'DELETE', `/api/v1/tasks/${t.id}?dropSource=true`);
  }
  await ctx.close();
  for (const uid of ['e2e_opt_snapshot', 'e2e_opt_stream', 'e2e_opt_composite', 'e2e_opt_guard', 'e2e_opt_dlq', 'e2e_imported']) {
    await request.delete(`${primary.url}/indexes/${uid}`, { headers: meiliHeaders(primary) });
  }
  await sql(request, `DROP TABLE IF EXISTS opt_items; DROP TABLE IF EXISTS guard_items;
    CREATE TABLE opt_items (id int PRIMARY KEY, name text, category text, price numeric(10,2), lat double precision, lng double precision,
      created_at timestamptz, internal_note text);
    INSERT INTO opt_items SELECT g, 'Item ' || g, (ARRAY['books','games','music'])[1 + g % 3], g + 0.5,
      35.6 + g / 100.0, 51.4 + g / 100.0, '2026-02-01'::timestamptz + g * interval '1 hour', 'secret ' || g
      FROM generate_series(1, 50) g;
    DROP TABLE IF EXISTS dlq_items;
    CREATE TABLE dlq_items (code text PRIMARY KEY, name text, meta jsonb);
    INSERT INTO dlq_items SELECT 'C' || g, 'Good ' || g, '{}' FROM generate_series(1, 20) g;
    INSERT INTO dlq_items SELECT 'BAD' || g, 'Bad vectors ' || g, '"bad"' FROM generate_series(1, 3) g;
    CREATE TABLE guard_items (id int PRIMARY KEY, name text);
    INSERT INTO guard_items SELECT g, 'Guarded ' || g FROM generate_series(1, 100) g;`);
});

test('snapshot only + upsert, with mapping options', async ({ page, request }) => {
  await login(page);
  const id = await wizardTask(page, {
    connector: CONN, name: 'E2E options snapshot', table: 'opt_items', uid: 'e2e_opt_snapshot', destination: 'Primary',
    mapping: async (p) => {
      await p.getByRole('checkbox', { name: 'Exclude internal_note' }).check();
      await p.getByLabel('Rename name').fill('title');
      await p.getByRole('button', { name: 'Add static field' }).click();
      await p.getByLabel('Static field 1 name').fill('origin');
      await p.getByLabel('Static field 1 value').fill('lab');
      await p.getByRole('group', { name: 'Time format' }).getByRole('button', { name: 'unix', exact: true }).click();
      await p.locator('#f-geo-lat').selectOption('lat');
      await p.locator('#f-geo-lng').selectOption('lng');
    },
    index: async (p) => {
      await p.getByText('Manage index settings').click();
      await p.getByTestId('wz-editor').getByRole('tab', { name: 'JSON' }).click();
      await p.getByTestId('settings-json').fill(JSON.stringify({ filterableAttributes: ['_geo', 'category'] }));
      await p.getByRole('radiogroup', { name: 'Write mode' }).getByText('Upsert').click();
    },
    sync: async (p) => {
      await p.getByRole('radiogroup', { name: 'Sync mode' }).getByText('Snapshot only').click();
    },
  });
  await expect(page.locator('[data-state="completed"]').first()).toBeVisible({ timeout: 90_000 });
  await expect.poll(() => meiliCount(request, primary, 'e2e_opt_snapshot')).toBe(50);
  const doc = await meiliDoc(request, primary, 'e2e_opt_snapshot', '1');
  expect(doc).toMatchObject({ title: 'Item 1', origin: 'lab', category: 'games', _geo: { lat: 35.61, lng: 51.41 } });
  expect(doc).not.toHaveProperty('internal_note');
  expect(doc).not.toHaveProperty('name');
  expect(typeof doc.created_at).toBe('number');

  // Snapshot only: later source changes are not followed.
  await sql(request, `UPDATE opt_items SET name = 'Changed later' WHERE id = 1`);
  await page.waitForTimeout(4000);
  expect((await meiliDoc(request, primary, 'e2e_opt_snapshot', '1')).title).toBe('Item 1');
  expect(id).toBeTruthy();
});

test('documents Meilisearch rejects are isolated in the DLQ; the rest is indexed', async ({ page, request }) => {
  await login(page);
  await wizardTask(page, {
    connector: CONN, name: 'E2E options dlq', table: 'dlq_items', uid: 'e2e_opt_dlq', destination: 'Primary',
    // meta becomes _vectors: {} is valid, a string is rejected by Meilisearch.
    mapping: async (p) => p.getByLabel('Rename meta').fill('_vectors'),
  });
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 90_000 });
  // The 3 rows with a malformed _vectors are rejected; bisection keeps the 20 good ones.
  await expect.poll(() => meiliCount(request, primary, 'e2e_opt_dlq'), { timeout: 60_000 }).toBe(20);
  await expect(page.locator('[data-counter="dlq"]')).toContainText('3');
  await page.getByRole('tab', { name: 'DLQ' }).click();
  const rows = page.getByTestId('dlq-table').locator('tbody tr');
  await expect(rows).toHaveCount(3);
  await expect(page.getByTestId('dlq-table')).toContainText(/expected a map/i);
  // Replaying unchanged documents cannot succeed: they are reported as still failing.
  await page.getByTestId('dlq-table').locator('thead input[type=checkbox]').check();
  await page.getByRole('button', { name: /Replay/ }).click();
  await expect(page.getByText(/Replayed 0 documents/)).toBeVisible();
  await expect(rows).toHaveCount(3);
  await page.getByTestId('dlq-table').locator('thead input[type=checkbox]').check();
  await page.getByRole('button', { name: /Discard/ }).click();
  const ok = page.getByRole('dialog').getByTestId('confirm-ok');
  if (await ok.isVisible({ timeout: 1500 }).catch(() => false)) await ok.click();
  await expect(rows).toHaveCount(0);
});

test('stream only: existing rows are skipped, new changes flow', async ({ page, request }) => {
  await login(page);
  await wizardTask(page, {
    connector: CONN, name: 'E2E options stream', table: 'opt_items', uid: 'e2e_opt_stream', destination: 'Primary',
    sync: async (p) => {
      await p.getByRole('radiogroup', { name: 'Sync mode' }).getByText('Stream only').click();
      await p.getByLabel('Flush interval').fill('200ms');
    },
  });
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 90_000 });
  await sql(request, `INSERT INTO opt_items (id, name, category, price, lat, lng, created_at) VALUES (100, 'Streamed', 'books', 1, 10, 10, now())`);
  await expect.poll(async () => (await meiliDoc(request, primary, 'e2e_opt_stream', '100'))?.name).toBe('Streamed');
  expect(await meiliCount(request, primary, 'e2e_opt_stream')).toBe(1);
});

test('composite primary key builds one document per key pair', async ({ page, request }) => {
  await login(page);
  await wizardTask(page, {
    connector: CONN, name: 'E2E options composite', table: 'opt_items', uid: 'e2e_opt_composite', destination: 'Primary',
    mapping: async (p) => {
      const pk = p.getByRole('group', { name: 'Primary key columns' });
      await pk.locator('[data-chip="category"]').click();
      await pk.locator('[data-chip="id"]').click();
      await p.locator('#f-pkf').fill('key');
    },
  });
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 90_000 });
  await expect.poll(() => meiliCount(request, primary, 'e2e_opt_composite')).toBe(51);
  const r = await (await request.post(`${primary.url}/indexes/e2e_opt_composite/search`, { headers: meiliHeaders(primary), data: { q: 'Item 7', limit: 1 } })).json();
  expect(r.hits[0].key).toMatch(/books|games|music/);
  expect(r.hits[0].key).toContain('7');
});

test('the mass delete guard holds the task; resync rebuilds the index', async ({ page, request }) => {
  await login(page);
  await wizardTask(page, {
    connector: CONN, name: 'E2E options guard', table: 'guard_items', uid: 'e2e_opt_guard', destination: 'Primary',
    sync: async (p) => {
      await p.getByLabel('Flush interval').fill('200ms');
      await p.locator('#f-ratio').evaluate((el) => { el.value = '0.3'; el.dispatchEvent(new Event('input', { bubbles: true })); });
      await p.locator('#f-mindocs').fill('10');
    },
  });
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 90_000 });
  await expect.poll(() => meiliCount(request, primary, 'e2e_opt_guard')).toBe(100);

  await sql(request, `DELETE FROM guard_items WHERE id <= 60`);
  await expect(page.locator('[data-state="held"]').first()).toBeVisible({ timeout: 60_000 });
  await expect(page.getByText(/safety guard/i).first()).toBeVisible();
  expect(await meiliCount(request, primary, 'e2e_opt_guard')).toBe(100); // nothing deleted

  await taskAction(page, 'resync');
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 90_000 });
  await expect.poll(() => meiliCount(request, primary, 'e2e_opt_guard'), { timeout: 60_000 }).toBe(40);
});

test('lifecycle: restart, zero-downtime resync and reset checkpoint', async ({ page, request }) => {
  await login(page);
  const id = await taskId(page, 'E2E postgres products');
  await page.goto(`/#/tasks/${id}`);
  const before = await meiliCount(request, primary, 'e2e_postgres');

  await taskAction(page, 'restart');
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 60_000 });

  // While the shadow index is built and swapped, search keeps answering.
  let lowest = Infinity;
  await taskAction(page, 'resync');
  const watch = (async () => {
    for (let i = 0; i < 40; i++) {
      lowest = Math.min(lowest, await meiliCount(request, primary, 'e2e_postgres'));
      await new Promise((r) => setTimeout(r, 250));
    }
  })();
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 90_000 });
  await watch;
  expect(lowest).toBeGreaterThanOrEqual(before);
  await expect.poll(() => meiliCount(request, primary, 'e2e_postgres')).toBe(before);

  await taskAction(page, 'stop');
  await expect(page.locator('[data-state="stopped"]').first()).toBeVisible();
  await taskAction(page, 'reset-checkpoint');
  await taskAction(page, 'start');
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 90_000 });
  await expect.poll(() => meiliCount(request, primary, 'e2e_postgres')).toBe(before);
});

test('diff finds drift made behind the task and repairs it', async ({ page, request }) => {
  await login(page);
  const id = await taskId(page, 'E2E postgres products');
  // Drift: one document removed and one edited directly in Meilisearch.
  await request.delete(`${primary.url}/indexes/e2e_postgres/documents/5`, { headers: meiliHeaders(primary) });
  await request.put(`${primary.url}/indexes/e2e_postgres/documents`, { headers: meiliHeaders(primary), data: [{ id: 6, name: 'Tampered' }] });
  await expect.poll(async () => (await meiliDoc(request, primary, 'e2e_postgres', '6'))?.name).toBe('Tampered');

  await page.goto(`/#/tasks/${id}/diff`);
  await page.getByTestId('run-diff').click();
  const tab = page.getByTestId('diff-tab');
  await expect(tab).toContainText('Out of sync', { timeout: 60_000 });
  await expect(tab).toContainText(/Missing\s*1/);
  await expect(tab).toContainText(/Mismatched\s*1/);
  await tab.getByRole('button', { name: /Repair/ }).first().click();
  const ok = page.getByRole('dialog').getByTestId('confirm-ok');
  if (await ok.isVisible({ timeout: 1500 }).catch(() => false)) await ok.click();
  await expect.poll(async () => (await meiliDoc(request, primary, 'e2e_postgres', '6'))?.name, { timeout: 60_000 }).not.toBe('Tampered');
  expect(await meiliDoc(request, primary, 'e2e_postgres', '5')).not.toBeNull();
  await page.getByTestId('run-diff').click();
  await expect(tab).toContainText('In sync', { timeout: 60_000 });
});

test('edit a running task', async ({ page }) => {
  await login(page);
  const id = await taskId(page, 'E2E options stream');
  await page.goto(`/#/tasks/${id}/edit`);
  await page.locator('[data-step="connection"]').click();
  await page.getByRole('textbox', { name: 'Description' }).fill('Edited by the e2e suite');
  await page.locator('[data-step="sync"]').click();
  await page.getByLabel('Flush interval').fill('500ms');
  await page.locator('[data-step="review"]').click();
  await page.getByRole('button', { name: /Save/ }).last().click();
  await expect(page.getByText('Edited by the e2e suite')).toBeVisible();
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 60_000 });
  const t = (await api(page, 'GET', `/api/v1/tasks/${id}`)).json;
  expect(t.sync.flushInterval).toBe('500ms');
});

test('batch pause and resume from the tasks list', async ({ page }) => {
  await login(page);
  await page.goto('/#/tasks');
  for (const n of ['E2E mysql products', 'E2E mariadb products']) await page.getByRole('checkbox', { name: `Select ${n}` }).check();
  await page.getByTestId('batch-bar').getByRole('button', { name: 'Pause selected tasks' }).click();
  await expect(page.getByText('Pause: 2 of 2 succeeded')).toBeVisible();
  for (const n of ['E2E mysql products', 'E2E mariadb products']) {
    await expect(page.locator('tr', { hasText: n }).locator('[data-state]').first()).toHaveAttribute('data-state', 'paused');
    await page.getByRole('checkbox', { name: `Select ${n}` }).check();
  }
  await page.getByTestId('batch-bar').getByRole('button', { name: 'Resume selected tasks' }).click();
  await expect(page.getByText('Resume: 2 of 2 succeeded')).toBeVisible();
  for (const n of ['E2E mysql products', 'E2E mariadb products']) {
    await expect(page.locator('tr', { hasText: n }).locator('[data-state]').first()).toHaveAttribute('data-state', 'streaming');
  }
});

test('export tasks and import one back', async ({ page, request }, testInfo) => {
  await login(page);
  await page.goto('/#/tasks');
  const download = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Export' }).click();
  const file = await (await download).path();
  const tasks = JSON.parse(fs.readFileSync(file, 'utf8'));
  expect(tasks.length).toBeGreaterThanOrEqual(15);
  expect(JSON.stringify(tasks)).not.toContain('cdc:cdc@'); // no credentials in exports

  const one = tasks.find((t) => t.name === 'E2E options composite');
  const copy = { ...one, id: 'e2e-imported', name: 'E2E imported', index: { ...one.index, uid: 'e2e_imported' } };
  const path = testInfo.outputPath('import.json');
  fs.writeFileSync(path, JSON.stringify([copy]));
  await page.getByLabel('Import tasks JSON file').setInputFiles(path);
  await expect(page.getByText('Imported 1 task')).toBeVisible();
  await expect(page.locator('tr', { hasText: 'E2E imported' })).toBeVisible();
  const id = await taskId(page, 'E2E imported');
  await page.goto(`/#/tasks/${id}`);
  await taskAction(page, 'start');
  await expect.poll(() => meiliCount(request, primary, 'e2e_imported'), { timeout: 90_000 }).toBe(51);
});
