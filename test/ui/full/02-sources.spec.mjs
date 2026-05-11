// Step 2: every source, through the GUI: create its connector, test it,
// create a task with the wizard, wait for the snapshot, then insert, update
// and delete in the source and see it in Meilisearch.
import { test, expect, login, lab, change, meiliDoc, meiliCount } from './helpers.mjs';

test.describe.configure({ mode: 'serial' });

const SOURCES = (process.env.SOURCES || 'postgres,mysql,mariadb,mongodb,dynamodb,couchdb,sqlserver,cockroachdb,kafka,redis,sqlpoll').split(',');
const TABLE_LABEL = { kafka: 'Topic', mongodb: 'Collection', dynamodb: 'DynamoDB table', couchdb: 'Database', redis: 'Stream key' };

async function createConnector(page, src) {
  const name = `E2E ${src.source}`;
  await page.goto('/#/connectors');
  if (await page.locator('[data-connector]', { hasText: name }).count()) return name;
  await page.getByRole('button', { name: /New connector/ }).first().click();
  const dialog = page.getByRole('dialog');
  await dialog.locator(`[data-source="${src.type}"]`).click();
  await dialog.getByRole('textbox', { name: 'Name', exact: true }).fill(name);
  const c = src.connector;
  if (c.driver) await dialog.getByLabel('Driver').selectOption(c.driver);
  if (c.dsn) await dialog.getByLabel('Connection string (DSN)').fill(c.dsn);
  if (c.database) await dialog.getByLabel('Database').fill(c.database);
  if (c.region) await dialog.getByLabel('AWS region').fill(c.region);
  if (c.endpoint) await dialog.getByLabel('Endpoint').fill(c.endpoint);
  if (c.accessKeyId) await dialog.getByLabel('Access key id').fill(c.accessKeyId);
  if (c.secretAccessKey) await dialog.getByLabel('Secret access key').fill(c.secretAccessKey);
  if (c.brokers) await dialog.getByLabel('Brokers').fill(c.brokers.join(','));
  await dialog.getByTestId('connector-test-btn').click();
  await expect(dialog.getByText(/Connected/).first()).toBeVisible();
  await dialog.getByTestId('connector-save').click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.locator('[data-connector]', { hasText: name })).toBeVisible();
  return name;
}

for (const [i, name] of SOURCES.entries()) {
  test(`${name}: connector, wizard, snapshot and live changes`, async ({ page, request }) => {
    const info = await lab(request);
    const src = info.sources.find((s) => s.source === name);
    const meili = info.meilisearch[i % 2]; // alternate Primary / Replica
    const uid = `e2e_${name}`;
    await login(page);
    const connector = await createConnector(page, src);

    await page.goto('/#/tasks/new');
    await page.locator('[data-connector]', { hasText: connector }).click();
    await page.getByTestId('wizard-next').click();
    await page.getByRole('textbox', { name: 'Name', exact: true }).fill(`E2E ${name} products`);
    // Discovery lists the connector's tables as chips; pick ours when listed.
    const chip = page.locator(`[data-table="${src.table}"]`);
    await chip.waitFor({ timeout: 8_000 }).catch(() => {});
    if (await chip.isVisible()) await chip.click();
    else await page.getByRole('textbox', { name: TABLE_LABEL[src.type] || 'Table', exact: true }).fill(src.table);
    if (src.cursorColumn) await expect(page.locator('#f-cursor')).toHaveValue(src.cursorColumn);
    await page.getByTestId('test-connection').click();
    await expect(page.getByTestId('test-result')).toContainText('Connected');
    await page.getByTestId('wizard-next').click(); // mapping
    if (src.softDelete) {
      // Polling cannot see removed rows: map the soft delete flag to deletes.
      const col = page.locator('#f-sdc');
      if (await col.evaluate((el) => el.tagName === 'SELECT')) await col.selectOption(src.softDelete.column);
      else await col.fill(src.softDelete.column);
      await page.locator('#f-sdv').fill(src.softDelete.value);
    }
    await page.getByTestId('wizard-next').click(); // index
    await page.getByLabel('Index uid').fill(uid);
    await page.locator('[data-destination-card]', { hasText: meili.name }).click();
    await page.getByRole('checkbox', { name: 'Filter: category' }).check();
    await page.getByRole('checkbox', { name: 'Sort: price' }).check();
    await page.getByText('Manage index settings').click();
    await page.getByTestId('wizard-next').click(); // sync
    await page.getByTestId('wizard-next').click(); // review
    await page.getByTestId('create-start-task').click();
    await expect(page.getByRole('heading', { level: 1, name: `E2E ${name} products` })).toBeVisible();
    await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 120_000 });
    await expect(page.locator('[data-counter="upserted"]')).toContainText(String(info.rows), { timeout: 120_000 });
    await expect.poll(() => meiliCount(request, meili, uid), { timeout: 60_000 }).toBe(info.rows);

    // Live changes made in the source itself.
    await change(request, name, 'insert', 9001, `Fresh ${name} product`);
    await change(request, name, 'update', 1, `Renamed ${name} product`);
    await change(request, name, 'delete', 2);
    await expect.poll(async () => (await meiliDoc(request, meili, uid, '9001'))?.name, { timeout: 90_000 }).toBe(`Fresh ${name} product`);
    await expect.poll(async () => (await meiliDoc(request, meili, uid, '1'))?.name, { timeout: 90_000 }).toBe(`Renamed ${name} product`);
    await expect.poll(async () => meiliDoc(request, meili, uid, '2'), { timeout: 90_000 }).toBeNull();
    await expect(page.locator('[data-counter="deleted"]')).not.toHaveText('0');

    // The settings chosen in the wizard are live on that instance.
    const st = await (await request.get(`${meili.url}/indexes/${uid}/settings`, { headers: { Authorization: `Bearer ${meili.apiKey}` } })).json();
    expect(JSON.stringify(st.filterableAttributes)).toContain('category');
    expect(st.sortableAttributes).toContain('price');
  });
}
