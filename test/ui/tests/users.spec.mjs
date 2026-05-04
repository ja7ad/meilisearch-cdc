import { test, expect, open, newSession } from './fixtures.mjs';

const usersTable = (page) => page.getByTestId('users-table');

test('invite a user, copy the link and accept it in a fresh browser', async ({ page, browser, baseURL, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await open(page, '#/settings/users', 'Settings');
  await page.getByTestId('invite-user').click();
  const dialog = page.getByRole('dialog', { name: 'Invite user' });
  await dialog.getByLabel('Email').fill('newbie@example.com');
  await dialog.getByLabel('Role').selectOption('moderator');
  await page.getByTestId('invite-send').click();
  const result = page.getByTestId('invite-result');
  await expect(result).toContainText('Email is not configured');
  const link = await page.getByTestId('invite-link').inputValue();
  expect(link).toMatch(/#\/invite\//);
  await page.getByRole('button', { name: 'Copy invitation link' }).click();
  await expect(page.getByRole('button', { name: 'Copy invitation link' })).toContainText('Copied');
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(link);
  await page.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByTestId('invitations-table')).toContainText('newbie@example.com');

  const { context: fresh, page: guest } = await newSession(browser, baseURL, null);
  await guest.goto(link.slice(link.indexOf('/#')));
  await expect(guest.getByRole('heading', { level: 1, name: "You're invited" })).toBeVisible();
  await expect(guest.getByTestId('invite-info')).toContainText('newbie@example.com');
  await expect(guest.getByTestId('invite-info')).toContainText('moderator');
  await guest.getByLabel('Your name').fill('Nina Newbie');
  await guest.getByLabel('Password', { exact: true }).fill('nina password 1');
  await guest.getByLabel('Confirm password').fill('nina password 1');
  await guest.getByTestId('invite-submit').click();
  await expect(guest.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
  await expect(guest.getByTestId('user-menu')).toContainText('Nina Newbie');
  // used links are rejected
  await guest.goto(link.slice(link.indexOf('/#')));
  await expect(guest.getByRole('heading', { level: 1, name: 'Invitation not valid' })).toBeVisible();
  await fresh.close();

  await page.reload();
  await expect(usersTable(page)).toContainText('newbie@example.com');
  await expect(page.getByText('No pending invitations')).toBeVisible();
});

test('invitation is emailed when SMTP is configured, and can be revoked', async ({ page }) => {
  await page.request.post('/__mock/smtp', { data: { configured: true } });
  await open(page, '#/settings/users', 'Settings');
  await page.getByTestId('invite-user').click();
  await page.getByRole('dialog').getByLabel('Email').fill('mailme@example.com');
  await page.getByTestId('invite-send').click();
  await expect(page.getByTestId('invite-result')).toContainText('Email sent');
  await page.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByTestId('invitations-table')).toContainText('emailed');
  await page.getByRole('button', { name: 'Revoke invitation for mailme@example.com' }).click();
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByText('Invitation revoked')).toBeVisible();
  await expect(page.getByText('No pending invitations')).toBeVisible();
});

test('add user, change role, set password, delete; cannot delete yourself', async ({ page, browser, baseURL }) => {
  await open(page, '#/settings/users', 'Settings');
  await expect(usersTable(page).locator('tbody tr')).toHaveCount(3);
  await expect(page.getByRole('button', { name: 'Delete admin@example.com' })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Delete admin@example.com' })).toHaveAttribute('title', 'You cannot delete yourself');

  await page.getByTestId('add-user').click();
  const dialog = page.getByRole('dialog', { name: 'Add user' });
  await page.getByTestId('add-user-save').click();
  await expect(dialog.getByText('Enter a valid email address')).toBeVisible();
  await dialog.getByLabel('Email').fill('sam@example.com');
  await dialog.getByLabel('Name').fill('Sam Support');
  await dialog.getByLabel('Initial password', { exact: true }).fill('sam password 1');
  await page.getByTestId('add-user-save').click();
  await expect(page.getByText('User added')).toBeVisible();
  await expect(usersTable(page).locator('tbody tr')).toHaveCount(4);

  await page.getByLabel('Role of sam@example.com').selectOption('moderator');
  await expect(page.getByText('sam@example.com is now moderator')).toBeVisible();

  await page.getByRole('button', { name: 'Set password for sam@example.com' }).click();
  await page.getByRole('dialog').getByLabel('New password', { exact: true }).fill('sam new password');
  await page.getByTestId('set-password-save').click();
  await expect(page.getByText('Password set')).toBeVisible();

  // the new password works
  const { context: c2, page: sam } = await newSession(browser, baseURL, null);
  await sam.goto('/#/login');
  await sam.getByLabel('Email').fill('sam@example.com');
  await sam.getByLabel('Password').fill('sam new password');
  await sam.getByTestId('login-submit').click();
  await expect(sam.getByTestId('user-menu')).toContainText('moderator');
  await c2.close();

  await page.getByRole('button', { name: 'Delete sam@example.com' }).click();
  const confirm = page.getByRole('dialog');
  await expect(confirm.getByTestId('confirm-ok')).toBeDisabled();
  await confirm.getByLabel(/Type/).fill('sam@example.com');
  await confirm.getByTestId('confirm-ok').click();
  await expect(page.getByText('User deleted')).toBeVisible();
  await expect(usersTable(page).locator('tbody tr')).toHaveCount(3);
});

test('your own role is locked; the owner is marked', async ({ page }) => {
  await open(page, '#/settings/users', 'Settings');
  const own = page.getByLabel('Role of admin@example.com');
  await expect(own).toBeDisabled();
  await expect(own).toHaveAttribute('title', 'You cannot change your own role');
  await expect(own).toHaveValue('admin');
  await expect(page.locator('tr[data-user="admin@example.com"]').getByText('Owner', { exact: true })).toBeVisible();
  await expect(page.locator('tr[data-user="mod@example.com"]').getByText('Owner', { exact: true })).toHaveCount(0);
  await expect(page.getByLabel('Role of mod@example.com')).toBeEnabled();
  // the server refuses it too
  const users = await (await page.request.get('/api/v1/users')).json();
  const me = users.find((u) => u.email === 'admin@example.com');
  expect(me.owner).toBe(true);
  const r = await page.request.patch(`/api/v1/users/${me.id}`, { data: { role: 'viewer' } });
  expect(r.status()).toBe(403);
  expect((await r.json()).error).toContain('your own role');
});

test('another admin cannot change, reset or delete the primary administrator', async ({ page, browser, baseURL }) => {
  await open(page, '#/settings/users', 'Settings');
  const created = await page.request.post('/api/v1/users', { data: { email: 'second@example.com', name: 'Second Admin', role: 'admin', password: 'second password' } });
  expect(created.status()).toBe(201);
  const { context, page: second } = await newSession(browser, baseURL, null);
  await second.goto('/#/login');
  await second.getByLabel('Email').fill('second@example.com');
  await second.getByLabel('Password').fill('second password');
  await second.getByTestId('login-submit').click();
  await open(second, '#/settings/users', 'Settings');

  const ownerRole = second.getByLabel('Role of admin@example.com');
  await expect(ownerRole).toBeDisabled();
  await expect(ownerRole).toHaveAttribute('title', /primary administrator's role cannot be changed/);
  const del = second.getByRole('button', { name: 'Delete admin@example.com' });
  await expect(del).toBeDisabled();
  await expect(del).toHaveAttribute('title', /primary administrator cannot be deleted/);
  await expect(second.getByRole('button', { name: 'Set password for admin@example.com' })).toBeDisabled();
  await expect(second.getByLabel('Role of second@example.com')).toBeDisabled();
  await expect(second.getByLabel('Role of mod@example.com')).toBeEnabled();
  await expect(second.getByRole('button', { name: 'Delete mod@example.com' })).toBeEnabled();

  const users = await (await second.request.get('/api/v1/users')).json();
  const owner = users.find((u) => u.owner);
  expect(owner.email).toBe('admin@example.com');
  expect((await second.request.patch(`/api/v1/users/${owner.id}`, { data: { role: 'viewer' } })).status()).toBe(403);
  expect((await second.request.delete(`/api/v1/users/${owner.id}`)).status()).toBe(403);
  expect((await second.request.post(`/api/v1/users/${owner.id}/password`, { data: { password: 'hijacked pw' } })).status()).toBe(403);
  await context.close();
});
