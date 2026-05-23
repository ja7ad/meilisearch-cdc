// Step 10: webhook signing secrets, through the GUI. A webhook task gets a
// generated secret in the wizard: signed pushes are indexed, unsigned,
// tampered or stale ones are refused, and rotating the secret retires the
// old one. An alert rule signs its webhook deliveries to a real receiver.
import http from 'node:http';
import crypto from 'node:crypto';
import { test, expect, login, lab, api, meiliDoc } from './helpers.mjs';

test.describe.configure({ mode: 'serial' });

const TASK = 'E2E signed webhook';
const UID = 'e2e_webhook_signed';
const RULE = 'E2E signed webhook alert';

const sign = (secret, ts, body) => `sha256=${crypto.createHmac('sha256', secret).update(`${ts}.${body}`).digest('hex')}`;

async function push(request, baseURL, id, body, { secret, ts = Math.floor(Date.now() / 1000), signBody = body } = {}) {
  const headers = { 'Content-Type': 'application/json' };
  if (secret) Object.assign(headers, { 'X-CDC-Timestamp': String(ts), 'X-CDC-Signature': sign(secret, ts, signBody) });
  const r = await request.post(`${baseURL}/api/v1/ingest/${id}`, { data: body, headers });
  return r.status();
}

/** Pushes until the task accepts (it restarts after an edit). */
async function pushSettled(request, baseURL, id, body, opts) {
  let code = 0;
  await expect.poll(async () => { code = await push(request, baseURL, id, body, opts); return code; }, { timeout: 30_000 }).not.toBe(503);
  return code;
}

test('webhook task: generated secret, signed pushes, rotation', async ({ page, request, baseURL }) => {
  const info = await lab(request);
  const meili = info.meilisearch[0];
  await login(page);
  for (const t of (await api(page, 'GET', '/api/v1/tasks')).json.filter((x) => x.name === TASK)) {
    await api(page, 'DELETE', `/api/v1/tasks/${t.id}?deleteIndex=${UID}`);
  }

  // A webhook connector has nothing to configure but a name.
  const connector = 'E2E webhook';
  const existing = (await api(page, 'GET', '/api/v1/connectors')).json.filter((c) => c.name === connector);
  for (const c of existing.slice(1).filter((c) => !(c.usedBy || []).length)) await api(page, 'DELETE', `/api/v1/connectors/${c.id}`);
  await page.goto('/#/connectors');
  if (!existing.length) {
    await page.getByRole('button', { name: /New connector/ }).first().click();
    const dialog = page.getByRole('dialog');
    await dialog.locator('[data-source="webhook"]').click();
    await dialog.getByRole('textbox', { name: 'Name', exact: true }).fill(connector);
    await dialog.getByTestId('connector-save').click();
    await expect(page.getByRole('dialog')).toHaveCount(0);
  }

  await page.goto('/#/tasks/new');
  await page.locator('[data-connector]', { hasText: connector }).first().click();
  await page.getByTestId('wizard-next').click();
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(TASK);
  await page.getByRole('textbox', { name: 'Feed name', exact: true }).fill('orders');
  const security = page.getByTestId('webhook-security');
  await security.scrollIntoViewIfNeeded();
  await expect(security.getByTestId('webhook-unsigned')).toBeVisible();

  // A short secret is refused; a generated one is accepted.
  await security.getByTestId('webhook-secret').fill('too-short');
  await page.getByTestId('wizard-next').click();
  await expect(security).toContainText('at least 16 characters');
  await security.getByTestId('webhook-secret').fill('');
  await security.getByTestId('webhook-secret-generate').click();
  const secret = await security.getByTestId('webhook-secret').inputValue();
  expect(secret).toMatch(/^whsec_[A-Za-z0-9_-]{40,}$/);
  await expect(security.getByTestId('webhook-secret-copy-now')).toBeVisible();
  await security.getByText('How to sign a request').click();
  await expect(security.getByTestId('webhook-curl')).toContainText('X-CDC-Signature: sha256=$SIG');

  await page.getByTestId('wizard-next').click(); // mapping
  await page.getByTestId('wizard-next').click(); // index
  await page.getByLabel('Index uid').fill(UID);
  await page.locator('[data-destination-card]', { hasText: meili.name }).click();
  await page.getByTestId('wizard-next').click(); // sync
  await page.getByTestId('wizard-next').click(); // review
  await page.getByTestId('create-start-task').click();
  await expect(page.getByRole('heading', { level: 1, name: TASK })).toBeVisible();
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible({ timeout: 60_000 });
  const id = page.url().split('#/tasks/')[1].split('/')[0];

  // The task page shows where to push and that only signed requests count.
  const card = page.getByTestId('push-endpoint');
  await expect(card.getByTestId('push-signed')).toBeVisible();
  await expect(card.getByTestId('push-url')).toHaveValue(`${baseURL}/api/v1/ingest/${id}`);
  const view = await api(page, 'GET', `/api/v1/tasks/${id}`);
  expect(view.json.source.webhookSecret).toBe('••••••');

  const body = JSON.stringify([{ op: 'upsert', row: { id: 1, name: 'Signed lamp' } }, { row: { id: 2, name: 'Signed desk' } }]);
  expect(await push(request, baseURL, id, body), 'unsigned').toBe(401);
  expect(await push(request, baseURL, id, body, { secret: 'whsec_not_the_right_secret_value' }), 'wrong secret').toBe(401);
  expect(await push(request, baseURL, id, JSON.stringify({ op: 'delete', row: { id: 1 } }), { secret, signBody: body }), 'tampered').toBe(401);
  expect(await push(request, baseURL, id, body, { secret, ts: Math.floor(Date.now() / 1000) - 600 }), 'stale').toBe(401);
  expect(await push(request, baseURL, id, body, { secret }), 'signed').toBe(200);
  expect((await meiliDoc(request, meili, UID, '1'))?.name).toBe('Signed lamp');
  await expect(page.locator('[data-counter="upserted"]')).toContainText('2', { timeout: 15_000 });

  // Rotate from Edit: the old secret stops working, the new one works.
  await page.goto(`/#/tasks/${id}/edit`);
  const setBox = page.getByTestId('webhook-secret-set');
  await expect(setBox).toContainText('never shown again');
  await page.getByTestId('webhook-secret-rotate').click();
  const rotated = await page.getByTestId('webhook-secret').inputValue();
  expect(rotated).not.toBe(secret);
  for (let i = 0; i < 5 && !(await page.getByTestId('save-task').isVisible()); i++) {
    await page.getByTestId('wizard-next').click();
  }
  await page.getByTestId('save-task').click();
  await expect(page.getByRole('heading', { level: 1, name: TASK })).toBeVisible();

  const update = JSON.stringify({ op: 'upsert', row: { id: 1, name: 'Rotated lamp' } });
  expect(await pushSettled(request, baseURL, id, update, { secret }), 'old secret after rotation').toBe(401);
  expect(await pushSettled(request, baseURL, id, update, { secret: rotated }), 'new secret').toBe(200);
  expect((await meiliDoc(request, meili, UID, '1'))?.name).toBe('Rotated lamp');
});

test('alert webhook deliveries are signed', async ({ page }) => {
  // A real receiver on this machine that checks every signature.
  const received = [];
  let secret = '';
  const server = http.createServer((req, res) => {
    let raw = '';
    req.on('data', (c) => { raw += c; });
    req.on('end', () => {
      const ts = req.headers['x-cdc-timestamp'];
      const sig = req.headers['x-cdc-signature'] || '';
      const want = sign(secret, ts, raw);
      const valid = sig.length === want.length && crypto.timingSafeEqual(Buffer.from(sig), Buffer.from(want))
        && Math.abs(Date.now() / 1000 - Number(ts)) < 300;
      received.push({ valid, body: JSON.parse(raw || '{}') });
      res.writeHead(valid ? 204 : 401).end();
    });
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const hookURL = `http://127.0.0.1:${server.address().port}/cdc-alerts`;
  try {
    await login(page);
    for (const r of (await api(page, 'GET', '/api/v1/alerts/rules')).json.filter((x) => x.name === RULE)) {
      await api(page, 'DELETE', `/api/v1/alerts/rules/${r.id}`);
    }
    await page.goto('/#/alerts');
    await page.getByTestId('new-rule').click();
    const d = page.getByRole('dialog');
    await d.locator('#ar-name').fill(RULE);
    await d.locator('#ar-type').selectOption('task_state');
    await expect(d.locator('#ar-hook-secret')).toHaveCount(0); // only with a URL
    await d.locator('#ar-hook').fill(hookURL);
    await d.getByTestId('alert-webhook-secret-generate').click();
    secret = await d.getByTestId('alert-webhook-secret').inputValue();
    expect(secret).toMatch(/^whsec_/);
    await d.getByTestId('rule-save').click();
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(page.getByTestId('rule-list')).toContainText('signed webhook');

    await page.getByRole('button', { name: `Send test for ${RULE}` }).click();
    await expect(page.getByTestId('rule-test-results')).toContainText(hookURL);
    await expect(page.getByTestId('rule-test-results')).not.toContainText(/fail|error/i);
    expect(received).toHaveLength(1);
    expect(received[0].valid).toBe(true);
    expect(received[0].body.text).toContain('TEST');

    // The secret is never shown again; saving the rule keeps it.
    const rules = await api(page, 'GET', '/api/v1/alerts/rules');
    expect(rules.json.find((r) => r.name === RULE).channels.webhookSecret).toBe('••••••');
    await page.getByRole('button', { name: `Edit rule ${RULE}` }).click();
    await expect(page.getByRole('dialog').getByTestId('alert-webhook-secret-set')).toBeVisible();
    await page.getByRole('dialog').getByTestId('rule-save').click();
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await page.getByRole('button', { name: `Send test for ${RULE}` }).click();
    await expect(page.getByTestId('rule-test-results')).not.toContainText(/fail|error/i);
    await expect.poll(() => received.length).toBe(2);
    expect(received[1].valid).toBe(true);
  } finally {
    server.close();
  }
});
