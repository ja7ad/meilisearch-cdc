// Step 4: team members. The admin adds a moderator and a viewer, invites a
// third person by email (Mailpit), then each role sees and can do only what
// it is allowed to — in the GUI and at the API.
import { test, expect, login, api, waitMail, signInAs, taskAction, taskId, REAL_SMTP } from './helpers.mjs';

test.describe.configure({ mode: 'serial' });

const PW = 'member-password-123';
const MOD = 'moderator@example.com';
const VIEWER = 'viewer@example.com';
const INVITED = 'invited@example.com';
const TASK = 'E2E postgres products';

async function addUser(page, email, name, role) {
  await page.goto('/#/settings/users');
  if (await page.locator(`[data-user="${email}"]`).count()) return;
  await page.getByTestId('add-user').click();
  const d = page.getByRole('dialog');
  await d.locator('#au-email').fill(email);
  await d.locator('#au-name').fill(name);
  await d.locator('#au-pw').fill(PW);
  await d.locator('#au-role').selectOption(role);
  await d.getByTestId('add-user-save').click();
  await expect(page.locator(`[data-user="${email}"]`)).toBeVisible();
}

test('admin adds a moderator and a viewer', async ({ page }) => {
  await login(page);
  await addUser(page, MOD, 'Mo Derator', 'moderator');
  await addUser(page, VIEWER, 'Vi Ewer', 'viewer');
  await expect(page.getByLabel(`Role of ${MOD}`)).toHaveValue('moderator');
  await expect(page.getByLabel(`Role of ${VIEWER}`)).toHaveValue('viewer');
});

test('an invitation is emailed and accepted', async ({ page, request, browser, baseURL }) => {
  test.skip(REAL_SMTP, 'the invitation would go to a real mailbox');
  await login(page);
  await page.goto('/#/settings/users');
  await page.getByTestId('invite-user').click();
  const d = page.getByRole('dialog');
  await d.locator('#iv-email').fill(INVITED);
  await d.locator('#iv-role').selectOption('viewer');
  await d.getByTestId('invite-send').click();
  await expect(page.getByTestId('invite-result')).toBeVisible();
  const mail = await waitMail(request, INVITED, /invited/i);
  const link = mail.text.match(/https?:\/\/\S+#\/invite\/\S+/)[0];

  const ctx = await browser.newContext({ baseURL });
  const p = await ctx.newPage();
  await p.goto(link);
  await expect(p.getByTestId('invite-info')).toContainText(INVITED);
  await p.locator('#iv-name').fill('In Vited');
  await p.locator('#iv-pw').fill(PW);
  await p.locator('#iv-pw2').fill(PW);
  await p.getByTestId('invite-submit').click();
  await p.waitForURL((u) => !u.hash.startsWith('#/invite'));
  const me = await api(p, 'GET', '/api/v1/auth/me');
  expect(me.json.user).toMatchObject({ email: INVITED, role: 'viewer' });
  await ctx.close();
});

test('a viewer can look but not touch', async ({ browser, baseURL }) => {
  const { ctx, page } = await signInAs(browser, baseURL, VIEWER, PW);
  await page.goto('/#/tasks');
  await expect(page.getByRole('heading', { level: 1, name: 'Tasks' })).toBeVisible();
  await expect(page.getByRole('link', { name: /New task/ })).toHaveCount(0);
  const id = await taskId(page, TASK);
  await page.goto(`/#/tasks/${id}`);
  await expect(page.getByRole('group', { name: 'Task actions' })).toContainText('read only');
  await expect(page.locator('[data-action]')).toHaveCount(0);
  // The server refuses even when the GUI is bypassed.
  expect((await api(page, 'POST', `/api/v1/tasks/${id}/pause`)).status).toBe(403);
  expect((await api(page, 'DELETE', `/api/v1/tasks/${id}`)).status).toBe(403);
  expect((await api(page, 'GET', '/api/v1/users')).status).toBe(403);
  await page.goto('/#/settings');
  await expect(page.getByRole('tab', { name: 'Users' })).toHaveCount(0);
  await expect(page.getByRole('tab', { name: 'MCP' })).toHaveCount(0);
  await ctx.close();
});

test('a moderator operates tasks but cannot change configuration', async ({ browser, baseURL }) => {
  const { ctx, page } = await signInAs(browser, baseURL, MOD, PW);
  const id = await taskId(page, TASK);
  await page.goto(`/#/tasks/${id}`);
  await taskAction(page, 'pause');
  await expect(page.locator('[data-state="paused"]').first()).toBeVisible();
  await taskAction(page, 'resume');
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible();
  await expect(page.locator('[data-action="delete"]')).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Edit' })).toHaveCount(0);
  expect((await api(page, 'DELETE', `/api/v1/tasks/${id}`)).status).toBe(403);
  expect((await api(page, 'POST', '/api/v1/destinations', { name: 'x', url: 'http://x' })).status).toBe(403);
  expect((await api(page, 'PUT', '/api/v1/settings/smtp', { host: '' })).status).toBe(403);
  await page.goto('/#/settings');
  await expect(page.getByRole('tab', { name: 'Users' })).toHaveCount(0);
  await ctx.close();
});

test('admin changes a role, resets a password and removes a member', async ({ page, browser, baseURL }) => {
  await login(page);
  await page.goto('/#/settings/users');
  await page.getByLabel(`Role of ${VIEWER}`).selectOption('moderator');
  await expect(page.getByText(/Role updated|now a moderator/i).first()).toBeVisible();

  await page.getByRole('button', { name: `Set password for ${VIEWER}` }).click();
  await page.getByRole('dialog').locator('#sp-pw').fill('changed-password-456');
  await page.getByRole('dialog').getByTestId('set-password-save').click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  const { ctx, page: p } = await signInAs(browser, baseURL, VIEWER, 'changed-password-456');
  expect((await api(p, 'GET', '/api/v1/auth/me')).json.user.role).toBe('moderator');
  await ctx.close();

  await page.getByRole('button', { name: `Delete ${VIEWER}` }).click();
  const d = page.getByRole('dialog');
  const typed = d.getByRole('textbox');
  if (await typed.count()) await typed.fill(VIEWER);
  await d.getByTestId('confirm-ok').click();
  await expect(page.locator(`[data-user="${VIEWER}"]`)).toHaveCount(0);
});
