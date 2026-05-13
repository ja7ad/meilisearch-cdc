// Step 5: the MCP server. The admin enables it and creates tokens in the
// GUI; an MCP client then speaks the protocol (Streamable HTTP, JSON-RPC)
// and only sees and runs what its role's policy allows.
import { test, expect, login, api, MCP, taskId } from './helpers.mjs';

test.describe.configure({ mode: 'serial' });
const tokens = {};
const RUN = Date.now().toString(36); // unique token names per run

async function generate(page, name, role) {
  await page.getByTestId('mcp-generate').click();
  const d = page.getByRole('dialog');
  await d.locator('#mt-name').fill(name);
  await d.locator('#mt-role').selectOption(role);
  await d.getByTestId('token-generate').click();
  const field = page.getByTestId('mcp-token-value');
  await expect(field).toBeVisible();
  const value = await field.evaluate((el) => el.value ?? el.querySelector('input')?.value ?? el.textContent);
  expect(value).toMatch(/^cdcmcp_/);
  await page.getByTestId('token-done').click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  return value;
}

test('enable MCP and create a token per role', async ({ page }) => {
  await login(page);
  await page.goto('/#/settings/mcp');
  const enabled = page.locator('#mcp-enabled');
  if (!(await enabled.isChecked())) {
    await enabled.check(); // saved at once (Save is for the role policy)
    const ok = page.getByRole('dialog').getByTestId('confirm-ok');
    if (await ok.isVisible({ timeout: 1500 }).catch(() => false)) await ok.click();
  }
  await expect(enabled).toBeChecked();
  tokens.viewer = await generate(page, `e2e viewer agent ${RUN}`, 'viewer');
  tokens.moderator = await generate(page, `e2e moderator agent ${RUN}`, 'moderator');
  tokens.admin = await generate(page, `e2e admin agent ${RUN}`, 'admin');
  await expect(page.getByTestId('mcp-tokens')).toContainText(`e2e moderator agent ${RUN}`);
});

test('the protocol: initialize, tools and resources per role', async ({ request, baseURL }) => {
  const viewer = new MCP(request, baseURL, tokens.viewer);
  const init = await viewer.connect();
  expect(init.result.serverInfo.name).toBeTruthy();
  expect(init.result.capabilities.tools).toBeTruthy();
  const vt = await viewer.tools();
  expect(vt).toContain('list_tasks');
  expect(vt).toContain('search_index');
  expect(vt).not.toContain('task_action');
  expect(vt).not.toContain('delete_task');

  const listed = await viewer.call('list_tasks');
  expect(listed.result.isError ?? false).toBe(false);
  expect(JSON.stringify(listed.result.content)).toContain('E2E postgres products');

  const res = await viewer.rpc('resources/read', { uri: 'cdc://destinations' });
  expect(JSON.stringify(res.result)).toContain('Replica');

  // Not in the viewer policy: refused even when called directly.
  const denied = await viewer.call('task_action', { id: 'whatever', action: 'stop' });
  expect(denied.error || denied.result?.isError).toBeTruthy();

  const admin = new MCP(request, baseURL, tokens.admin);
  await admin.connect();
  expect(await admin.tools()).toEqual(expect.arrayContaining(['create_task', 'delete_task', 'update_index_settings']));
});

test('a moderator agent pauses and resumes a task the GUI shows', async ({ page, request, baseURL }) => {
  await login(page);
  const id = await taskId(page, 'E2E mysql products');
  const mod = new MCP(request, baseURL, tokens.moderator);
  await mod.connect();
  const tools = await mod.tools();
  expect(tools).toContain('task_action');
  expect(tools).not.toContain('delete_task');
  const r = await mod.call('task_action', { id, action: 'pause' });
  expect(r.result?.isError ?? false, JSON.stringify(r)).toBe(false);
  await page.goto(`/#/tasks/${id}`);
  await expect(page.locator('[data-state="paused"]').first()).toBeVisible();
  await mod.call('task_action', { id, action: 'resume' });
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible();
  // Mutating MCP calls are audited.
  const audit = await api(page, 'GET', '/api/v1/audit?limit=200');
  expect(JSON.stringify(audit.json)).toContain('task_action');
});

test('policy changes and revocation apply immediately', async ({ page, request, baseURL }) => {
  await login(page);
  await page.goto('/#/settings/mcp');
  await page.getByRole('switch', { name: 'Allow list_tasks for Viewer' }).uncheck();
  await page.getByTestId('mcp-save').click();
  await expect(page.getByTestId('mcp-unsaved')).toHaveCount(0);
  const viewer = new MCP(request, baseURL, tokens.viewer);
  await viewer.connect();
  expect(await viewer.tools()).not.toContain('list_tasks');

  await page.getByTestId('mcp-reset').click();
  const ok = page.getByRole('dialog').getByTestId('confirm-ok');
  if (await ok.isVisible({ timeout: 1500 }).catch(() => false)) await ok.click();
  await expect.poll(async () => { const v = new MCP(request, baseURL, tokens.viewer); await v.connect(); return v.tools(); }).toContain('list_tasks');

  await page.getByRole('button', { name: `Revoke e2e viewer agent ${RUN}` }).click();
  const d = page.getByRole('dialog');
  const typed = d.getByRole('textbox');
  if (await typed.count()) await typed.fill(`e2e viewer agent ${RUN}`);
  await d.getByTestId('confirm-ok').click();
  await expect(page.getByRole('button', { name: `Revoke e2e viewer agent ${RUN}` })).toHaveCount(0);
  const revoked = await new MCP(request, baseURL, tokens.viewer).connect();
  expect(revoked.status).toBe(401);
});
