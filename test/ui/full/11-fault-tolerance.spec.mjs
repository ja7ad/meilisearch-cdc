// Step 11: fault tolerance, for real. The tasks of step 2 keep streaming
// while their databases restart or freeze, Meilisearch goes away, the
// service is killed with SIGKILL (run it with deploy/e2e/service.sh so it
// comes back) and bursts of writes arrive. After every fault the changes
// made around it must be in Meilisearch: nothing lost, nothing stuck.
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test, expect, login, lab, change, meiliDoc, meiliCount, api, sql, waitMail, taskId } from './helpers.mjs';

test.describe.configure({ mode: 'serial' });
test.setTimeout(600_000);

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');
const PIDFILE = path.join(ROOT, 'deploy/e2e/.service.pid');
const SOURCES = ['postgres', 'mysql', 'mariadb', 'mongodb', 'dynamodb', 'couchdb', 'sqlserver', 'cockroachdb', 'kafka', 'redis', 'sqlpoll'];
// The compose service each source lives in.
const CONTAINER = { kafka: 'redpanda', sqlpoll: 'postgres' };
const container = (s) => `cdc-e2e-${CONTAINER[s] || s}-1`;
// In-memory DynamoDB Local and Redis without persistence lose their data
// on restart, so they are frozen instead of restarted.
const VOLATILE = ['dynamodb', 'redis'];
const RUN = 50_000 + (Math.floor(Date.now() / 1000) % 40_000) * 10;

const docker = (...args) => execFileSync('docker', args, { encoding: 'utf8' }).trim();
async function healthy(name) {
  await expect.poll(() => {
    try { return docker('inspect', '-f', '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}', name); } catch { return 'missing'; }
  }, { timeout: 180_000, intervals: [1000] }).toMatch(/healthy|running/);
}
/** lab change, retried while the source is still coming back. */
async function changeSoon(request, source, op, id, name) {
  await expect.poll(async () => {
    const r = await request.post('http://127.0.0.1:8799/change', { data: { source, op, id, name } });
    return r.status();
  }, { timeout: 120_000, intervals: [1000, 2000, 3000] }).toBe(204);
}
const meiliOf = (info, source) => info.meilisearch[SOURCES.indexOf(source) % 2];
async function expectDoc(request, info, source, id, name, timeout = 180_000) {
  const m = meiliOf(info, source);
  await expect.poll(async () => {
    const d = await meiliDoc(request, m, `e2e_${source}`, String(id));
    return d ? d.name : null;
  }, { timeout, message: `${source} #${id}` }).toBe(name);
}
async function expectStreaming(page) {
  await expect.poll(async () => {
    const tasks = (await api(page, 'GET', '/api/v1/tasks')).json || [];
    return SOURCES.filter((s) => (tasks.find((t) => t.name === `E2E ${s} products`) || { status: {} }).status.state !== 'streaming');
  }, { timeout: 180_000, intervals: [2000], message: 'tasks not streaming' }).toEqual([]);
  await page.goto('/#/tasks');
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible();
}
async function dlqCounts(page) {
  const tasks = (await api(page, 'GET', '/api/v1/tasks')).json.filter((t) => /^E2E \w+ products$/.test(t.name));
  return Object.fromEntries(tasks.map((t) => [t.name, (t.status.counters || {}).dlq || 0]));
}

test('every source survives a restart of its database', async ({ page, request }) => {
  const info = await lab(request);
  await login(page);
  await expectStreaming(page);
  const restartable = SOURCES.filter((s) => !VOLATILE.includes(s));
  // A change just before the restart must not be lost either.
  for (const s of restartable) await change(request, s, 'insert', RUN + 1, `Before restart ${s}`);
  const names = [...new Set(restartable.map(container))];
  docker('restart', '-t', '5', ...names);
  for (const n of names) await healthy(n);
  for (const s of restartable) await changeSoon(request, s, 'insert', RUN + 2, `After restart ${s}`);
  for (const s of restartable) {
    await expectDoc(request, info, s, RUN + 1, `Before restart ${s}`);
    await expectDoc(request, info, s, RUN + 2, `After restart ${s}`);
  }
  await expectStreaming(page);
});

test('frozen databases (no network) catch up when they come back', async ({ page, request }) => {
  const info = await lab(request);
  await login(page);
  const names = [...new Set(SOURCES.map(container))];
  docker('pause', ...names);
  try {
    await page.waitForTimeout(25_000); // longer than most read timeouts
    await page.goto('/#/tasks');
    await page.screenshot({ path: test.info().outputPath('frozen-sources.png'), fullPage: true });
  } finally {
    docker('unpause', ...names);
  }
  for (const s of SOURCES) await changeSoon(request, s, 'update', 5, `Thawed ${s}`);
  for (const s of SOURCES) await expectDoc(request, info, s, 5, `Thawed ${s}`);
  await expectStreaming(page);
});

test('changes made while Meilisearch is down land when it returns', async ({ page, request }) => {
  const info = await lab(request);
  await login(page);
  const before = await dlqCounts(page);
  const primary = SOURCES.filter((s) => SOURCES.indexOf(s) % 2 === 0);
  docker('stop', '-t', '2', 'cdc-e2e-meili-primary-1');
  try {
    for (const s of primary) await change(request, s, 'update', 6, `During outage ${s}`);
    for (const s of primary) await change(request, s, 'delete', 7);
    // The dashboard shows the outage.
    await page.goto('/#/destinations');
    await expect(page.locator('[data-destination]', { hasText: 'Primary' }).first()).toContainText(/unreachable|refused|connect|EOF|timeout/i, { timeout: 90_000 });
    await page.screenshot({ path: test.info().outputPath('meili-down.png'), fullPage: true });
    await page.waitForTimeout(15_000);
  } finally {
    docker('start', 'cdc-e2e-meili-primary-1');
  }
  await healthy('cdc-e2e-meili-primary-1');
  for (const s of primary) {
    await expectDoc(request, info, s, 6, `During outage ${s}`);
    await expectDoc(request, info, s, 7, null);
  }
  await expectStreaming(page);
  expect(await dlqCounts(page), 'an outage is retried, not dead-lettered').toEqual(before);
});

test('a SIGKILLed service resumes every task without losing changes', async ({ page, request }) => {
  test.skip(!fs.existsSync(PIDFILE), 'run the service with deploy/e2e/service.sh');
  const info = await lab(request);
  await login(page);
  await expectStreaming(page);
  const pid = Number(fs.readFileSync(PIDFILE, 'utf8'));
  process.kill(pid, 'SIGKILL');
  // While the service is down, every source keeps changing.
  for (const s of SOURCES) {
    await change(request, s, 'insert', RUN + 3, `While down ${s}`);
    await change(request, s, 'update', 8, `Updated while down ${s}`);
    await change(request, s, 'delete', 9);
  }
  await expect.poll(async () => (await request.get('/api/v1/health').catch(() => null))?.status() ?? 0, { timeout: 60_000 }).toBe(200);
  expect(Number(fs.readFileSync(PIDFILE, 'utf8'))).not.toBe(pid);
  for (const s of SOURCES) {
    await expectDoc(request, info, s, RUN + 3, `While down ${s}`);
    await expectDoc(request, info, s, 8, `Updated while down ${s}`);
    await expectDoc(request, info, s, 9, null);
  }
  await login(page);
  await expectStreaming(page);
});

test('a crash in the middle of a snapshot still copies every row exactly once', async ({ page, request }) => {
  test.skip(!fs.existsSync(PIDFILE), 'run the service with deploy/e2e/service.sh');
  const info = await lab(request);
  const meili = info.meilisearch[0];
  await login(page);
  const N = 60_000;
  await sql(request, 'DROP TABLE IF EXISTS crash_snapshot');
  await sql(request, `CREATE TABLE crash_snapshot AS SELECT g AS id, 'row ' || g AS name, repeat('x', 400) AS pad FROM generate_series(1, ${N}) g`);
  await sql(request, 'ALTER TABLE crash_snapshot ADD PRIMARY KEY (id)');
  for (const t of (await api(page, 'GET', '/api/v1/tasks')).json.filter((x) => x.name === 'E2E crash snapshot')) {
    await api(page, 'DELETE', `/api/v1/tasks/${t.id}?dropSource=true&deleteIndex=e2e_crash_snapshot`);
  }
  const conns = (await api(page, 'GET', '/api/v1/connectors')).json;
  const pg = conns.find((c) => c.name === 'E2E postgres');
  const dests = (await api(page, 'GET', '/api/v1/destinations')).json;
  const created = await api(page, 'POST', '/api/v1/tasks?start=true', {
    name: 'E2E crash snapshot',
    source: { connectorId: pg.id, table: 'crash_snapshot' },
    index: { uid: 'e2e_crash_snapshot', destinationId: dests.find((d) => d.name === meili.name).id },
    sync: { batchSize: 500, maxInflight: 1 },
  });
  expect(created.status, created.text).toBe(201);
  const id = created.json.id;
  // Kill it once part of the table is in Meilisearch.
  await expect.poll(() => meiliCount(request, meili, 'e2e_crash_snapshot'), { timeout: 120_000, intervals: [200] }).toBeGreaterThan(5_000);
  process.kill(Number(fs.readFileSync(PIDFILE, 'utf8')), 'SIGKILL');
  await sql(request, `UPDATE crash_snapshot SET name = 'changed during crash' WHERE id = ${N}`);
  await expect.poll(async () => (await request.get('/api/v1/health').catch(() => null))?.status() ?? 0, { timeout: 60_000 }).toBe(200);
  await expect.poll(() => meiliCount(request, meili, 'e2e_crash_snapshot'), { timeout: 300_000 }).toBe(N);
  await expect.poll(async () => (await meiliDoc(request, meili, 'e2e_crash_snapshot', String(N)))?.name, { timeout: 60_000 }).toBe('changed during crash');
  await login(page);
  await page.goto(`/#/tasks/${id}`);
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 120_000 });
  // The data quality check agrees, row by row.
  await page.goto(`/#/tasks/${id}/diff`);
  await page.getByTestId('run-diff').click();
  await expect(page.getByTestId('diff-tab').getByText('In sync').first()).toBeVisible({ timeout: 180_000 });
  await api(page, 'DELETE', `/api/v1/tasks/${id}?dropSource=true&deleteIndex=e2e_crash_snapshot`);
  await sql(request, 'DROP TABLE crash_snapshot');
});

test('a slow Meilisearch raises a lag alert that resolves on recovery', async ({ page, request }) => {
  await login(page);
  const rule = 'E2E lag alert';
  for (const r of (await api(page, 'GET', '/api/v1/alerts/rules')).json.filter((x) => x.name === rule)) await api(page, 'DELETE', `/api/v1/alerts/rules/${r.id}`);
  const pgTask = await taskId(page, 'E2E postgres products');
  const created = await api(page, 'POST', '/api/v1/alerts/rules', {
    name: rule, enabled: true, severity: 'warning', notifyResolved: true, cooldown: '1m', taskIds: [pgTask],
    condition: { type: 'lag', thresholdMs: 5000, for: '0s' }, channels: { emails: ['lag@example.com'], telegramChatIds: [] },
  });
  expect(created.status, created.text).toBe(201);
  docker('pause', 'cdc-e2e-meili-primary-1'); // accepts connections, never answers: worse than down
  try {
    await change(request, 'postgres', 'update', 10, 'Stuck behind a slow Meilisearch');
    await waitMail(request, 'lag@example.com', /^\[WARNING\] E2E lag alert/);
    await page.goto('/#/alerts');
    await expect(page.getByTestId('active-alerts')).toContainText(rule);
  } finally {
    docker('unpause', 'cdc-e2e-meili-primary-1');
  }
  const info = await lab(request);
  await expectDoc(request, info, 'postgres', 10, 'Stuck behind a slow Meilisearch');
  await waitMail(request, 'lag@example.com', /^\[RESOLVED\] E2E lag alert/);
  await api(page, 'DELETE', `/api/v1/alerts/rules/${created.json.id}`);
});

test('bursts of writes to the same rows converge to the database state', async ({ page, request }) => {
  await login(page);
  // 40 rounds over 200 rows in single statements, and 300 tiny transactions.
  await sql(request, `DO $$ BEGIN FOR i IN 1..40 LOOP UPDATE products SET price = price + 0.01, name = 'burst ' || i || ' #' || id WHERE id <= 200; END LOOP; END $$`);
  for (let i = 0; i < 300; i++) await sql(request, `UPDATE products SET stock = stock + 1 WHERE id = ${1 + (i % 20)}`);
  const id = await taskId(page, 'E2E postgres products');
  await page.waitForTimeout(5_000);
  await page.goto(`/#/tasks/${id}/diff`);
  await page.getByTestId('run-diff').click();
  await expect(page.getByTestId('diff-tab').getByText('In sync').first()).toBeVisible({ timeout: 180_000 });
});
