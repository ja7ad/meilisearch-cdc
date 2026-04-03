import { test, expect, open, outbox, PUBLIC } from './fixtures.mjs';

test.describe('first run', () => {
  test.use({ role: null, mockState: 'empty' });
  test('setup creates the first admin and signs in', async ({ page, consoleErrors }) => {
    await page.goto('/#/tasks');
    await expect(page).toHaveURL(/#\/setup$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Create the first admin' })).toBeVisible();
    await page.getByTestId('setup-submit').click();
    await expect(page.getByText('Enter a valid email address')).toBeVisible();
    await page.getByLabel('Email').fill('owner@example.com');
    await page.getByLabel('Name').fill('Olive Owner');
    await page.getByLabel('Password', { exact: true }).fill('short');
    await expect(page.getByText('Too short.')).toBeVisible();
    await page.getByLabel('Password', { exact: true }).fill('correct horse battery');
    await page.getByLabel('Confirm password').fill('correct horse battery');
    await page.getByTestId('setup-submit').click();
    // a fresh install continues with the onboarding flow
    await expect(page.getByTestId('onboarding')).toBeVisible();
    await page.getByTestId('ob-dismiss').click();
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
    await expect(page.getByTestId('user-menu')).toContainText('Olive Owner');
    await page.goto('/#/setup');
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
    expect(consoleErrors).toEqual([]);
  });
});

test.describe('signed out', () => {
  test.use({ role: null });

  test('login redirects back to the requested page', async ({ page, consoleErrors }) => {
    await page.goto('/#/indexes');
    await expect(page).toHaveURL(/#\/login\?next=/);
    await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
    await page.getByLabel('Email').fill('admin@example.com');
    await page.getByLabel('Password').fill('password123');
    await page.getByTestId('login-submit').click();
    await expect(page.getByRole('heading', { level: 1, name: 'Indexes' })).toBeVisible();
    await expect(page.getByTestId('user-menu')).toContainText('Ada Admin');
    expect(consoleErrors).toEqual([]);
  });

  test('wrong password shows an inline error, then 429 with a countdown', async ({ page }) => {
    await open(page, '#/login', 'Sign in');
    await page.getByLabel('Email').fill('admin@example.com');
    await page.getByLabel('Password').fill('nope-nope');
    await page.getByTestId('login-submit').click();
    await expect(page.getByTestId('login-error')).toHaveText('Invalid email or password.');
    for (let i = 0; i < 4; i++) await page.getByTestId('login-submit').click();
    await page.getByLabel('Password').fill('password123');
    await page.getByTestId('login-submit').click({ force: true });
    await expect(page.getByText(/Too many attempts, try again in/)).toBeVisible();
    const n = Number(await page.getByTestId('retry-seconds').textContent());
    expect(n).toBeGreaterThan(0);
    await expect(page.getByTestId('login-submit')).toBeDisabled();
    await expect(page.getByTestId('login-submit')).toContainText(/Try again in \d+ s/);
  });

  test('forgot link only when SMTP is configured', async ({ page }) => {
    await open(page, '#/login', 'Sign in');
    await expect(page.getByRole('link', { name: 'Forgot password?' })).toHaveCount(0);
    await page.request.post('/__mock/smtp', { data: { configured: true } });
    await page.reload();
    await expect(page.getByRole('link', { name: 'Forgot password?' })).toBeVisible();
  });

  test('forgot + reset password flow', async ({ page }) => {
    await page.request.post('/__mock/smtp', { data: { configured: true } });
    await open(page, '#/login', 'Sign in');
    await page.getByRole('link', { name: 'Forgot password?' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Reset your password' })).toBeVisible();
    await page.getByLabel('Email').fill('mod@example.com');
    await page.getByRole('button', { name: 'Send reset link' }).click();
    await expect(page.getByTestId('forgot-sent')).toContainText('If an account exists for mod@example.com');
    const mail = (await outbox(page.request)).find((m) => m.to === 'mod@example.com');
    const hash = mail.link.slice(mail.link.indexOf('#'));
    await page.goto(`/${hash}`);
    await expect(page.getByRole('heading', { level: 1, name: 'Choose a new password' })).toBeVisible();
    await page.getByLabel('New password', { exact: true }).fill('a much better password');
    await page.getByLabel('Confirm new password').fill('a much better password');
    await page.getByTestId('reset-submit').click();
    await expect(page.getByTestId('reset-done')).toBeVisible();
    await page.getByTestId('reset-done').getByRole('link', { name: 'Sign in' }).click();
    await page.getByLabel('Email').fill('mod@example.com');
    await page.getByLabel('Password').fill('a much better password');
    await page.getByTestId('login-submit').click();
    await expect(page.getByTestId('user-menu')).toContainText('moderator');
    // the link is single use
    await page.goto(`/${hash}`);
    await page.getByLabel('New password', { exact: true }).fill('another password!');
    await page.getByLabel('Confirm new password').fill('another password!');
    await page.getByTestId('reset-submit').click();
    await expect(page.getByText(/invalid or has expired/)).toBeVisible();
  });

  test('forgot page explains when email is off', async ({ page }) => {
    await open(page, '#/forgot', 'Reset your password');
    await expect(page.getByText('Email is not configured')).toBeVisible();
  });

  test('API token sign-in is a secondary option', async ({ page }) => {
    await open(page, '#/login', 'Sign in');
    await page.getByRole('button', { name: 'Use an API token instead' }).click();
    await page.getByLabel('Admin token').fill('wrong');
    await page.getByRole('button', { name: 'Save token' }).click();
    await expect(page.getByTestId('login-error')).toContainText('rejected');
    await page.getByLabel('Admin token').fill('mock-api-token');
    await page.getByRole('button', { name: 'Save token' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
    await expect(page.getByTestId('user-menu')).toContainText('API token');
    await expect(page.getByTestId('health-pill')).toContainText('Live');
  });

  for (const p of PUBLIC) {
    test(`public page renders cleanly: ${p.name}`, async ({ page, consoleErrors }) => {
      await open(page, p.hash, p.heading);
      await page.waitForTimeout(500);
      expect(consoleErrors).toEqual([]);
    });
  }
});

test('an expired session returns to login and back', async ({ page, context }) => {
  await open(page, '#/indexes', 'Indexes');
  await context.clearCookies();
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await expect(page).toHaveURL(/#\/login\?next=%23%2Ftasks/);
  await expect(page.getByText('Session ended')).toBeVisible();
  await page.getByLabel('Email').fill('admin@example.com');
  await page.getByLabel('Password').fill('password123');
  await page.getByTestId('login-submit').click();
  await expect(page.getByRole('heading', { level: 1, name: 'Tasks' })).toBeVisible();
});

test('user menu: profile, change password, sign out', async ({ page }) => {
  await open(page, '#/overview', 'Overview');
  const menuBtn = page.getByTestId('user-menu');
  await menuBtn.click();
  const menu = page.getByRole('menu', { name: 'Account' });
  await expect(menu).toContainText('admin@example.com');
  await expect(page.getByRole('menuitem', { name: 'Profile' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(menu).toHaveCount(0);

  await menuBtn.click();
  await page.getByRole('menuitem', { name: 'Profile' }).click();
  await page.getByLabel('Display name').fill('Ada Lovelace');
  await page.getByRole('dialog').getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Profile updated')).toBeVisible();
  await expect(menuBtn).toContainText('Ada Lovelace');

  await menuBtn.click();
  await page.getByRole('menuitem', { name: 'Change password' }).click();
  await page.getByLabel('Current password').fill('wrong-password');
  await page.getByLabel('New password', { exact: true }).fill('new password 123');
  await page.getByLabel('Confirm new password').fill('new password 123');
  await page.getByTestId('change-password-save').click();
  await expect(page.getByRole('dialog').getByRole('alert')).toContainText('current password is wrong');
  await page.getByLabel('Current password').fill('password123');
  await page.getByTestId('change-password-save').click();
  await expect(page.getByText('Password changed')).toBeVisible();

  await menuBtn.click();
  await page.getByRole('menuitem', { name: 'Sign out' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
  await page.goto('/#/overview');
  await expect(page).toHaveURL(/#\/login/);
});
