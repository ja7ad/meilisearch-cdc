import { test, expect, open } from './fixtures.mjs';

test('SMTP: save, keep masked password, send test email', async ({ page }) => {
  await open(page, '#/settings/email', 'Settings');
  const form = page.getByTestId('smtp-form');
  await expect(form.getByTestId('notify-status')).toHaveText('Not configured');
  await expect(page.getByTestId('smtp-test')).toBeDisabled();
  await form.getByLabel('Host').fill('smtp.example.com');
  await form.getByLabel('Username').fill('cdc');
  await form.getByLabel('Password').fill('s3cret');
  await form.getByLabel('From address').fill('cdc@example.com');
  await form.getByLabel('Encryption').selectOption('tls');
  await form.getByLabel('Public URL').fill('https://cdc.example.com');
  await page.getByTestId('smtp-save').click();
  await expect(page.getByText('Email settings saved')).toBeVisible();
  await expect(form.getByTestId('notify-status')).toContainText('Configured');
  await expect(form.getByLabel('Password')).toHaveValue('••••••');

  await page.reload();
  await expect(page.getByTestId('smtp-form').getByLabel('Encryption')).toHaveValue('tls');
  await page.getByLabel('Recipient').fill('not-an-email');
  await page.getByTestId('smtp-test').click();
  await expect(page.getByText('550 invalid recipient')).toBeVisible();
  await page.getByLabel('Recipient').fill('me@example.com');
  await page.getByTestId('smtp-test').click();
  await expect(page.getByText('A test email was sent to me@example.com.')).toBeVisible();
  const box = await (await page.request.get('/__mock/outbox')).json();
  expect(box.some((m) => m.to === 'me@example.com')).toBe(true);

  // the login page now offers password reset
  await page.request.post('/api/v1/auth/logout');
  await page.goto('/#/login');
  await expect(page.getByRole('link', { name: 'Forgot password?' })).toBeVisible();
});

test('Telegram: invalid token is rejected, valid one shows the bot, test message', async ({ page }) => {
  await open(page, '#/settings/telegram', 'Settings');
  await expect(page.getByText('@BotFather')).toBeVisible();
  const form = page.getByTestId('telegram-form');
  await form.getByLabel('Bot token').fill('not-a-token');
  await page.getByTestId('telegram-save').click();
  await expect(form.getByText(/Telegram rejected the token/)).toBeVisible();
  await form.getByLabel('Bot token').fill('123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw');
  await page.getByTestId('telegram-save').click();
  await expect(page.getByText('Connected to @meili_cdc_bot')).toBeVisible();
  await expect(form.getByTestId('notify-status')).toHaveText(/@meili_cdc_bot/);
  await page.getByLabel('Chat id').fill('nope');
  await page.getByTestId('telegram-test').click();
  await expect(page.getByText('Bad Request: chat not found')).toBeVisible();
  await page.getByLabel('Chat id').fill('-1001234567890');
  await page.getByTestId('telegram-test').click();
  await expect(page.getByText('A test message was sent to chat -1001234567890.')).toBeVisible();
});

test('SMTP: auto encryption by default, port and encryption stay in sync, readable errors', async ({ page }) => {
  await open(page, '#/settings/email', 'Settings');
  const form = page.getByTestId('smtp-form');
  await expect(form.getByLabel('Encryption')).toHaveValue('auto');
  await form.getByLabel('Encryption').selectOption('starttls');
  await expect(form.getByLabel('Port')).toHaveValue('587');
  await form.getByLabel('Port').fill('465');
  await expect(form.getByLabel('Encryption')).toHaveValue('tls');
  await expect(page.getByTestId('port-note')).toHaveText('Port 465 uses TLS — switched encryption.');
  await form.getByLabel('Encryption').selectOption('starttls');
  await expect(form.getByLabel('Port')).toHaveValue('587');
  await form.getByLabel('Host').fill('smtp.fail.example.com');
  await form.getByLabel('From address').fill('cdc@example.com');
  await page.getByTestId('smtp-save').click();
  await expect(page.getByText('Email settings saved')).toBeVisible();
  await page.getByLabel('Recipient').fill('me@example.com');
  await page.getByTestId('smtp-test').click();
  await expect(page.getByTestId('smtp-connecting')).toContainText('Connecting to smtp.fail.example.com:587');
  const err = page.getByTestId('test-error');
  await expect(err).toContainText('port 587 usually needs STARTTLS');
  await err.getByText('Technical details').click();
  await expect(err.locator('pre')).toContainText('i/o timeout');
});
