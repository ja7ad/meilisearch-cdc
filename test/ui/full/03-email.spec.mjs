// Step 3: email delivery from Settings → Email. Mailpit by default; with
// REAL_SMTP=1 the SMTP server you saved in the dashboard is used as is.
import { test, expect, login, lab, waitMail, REAL_SMTP, ALERT_EMAIL } from './helpers.mjs';

test('configure SMTP and send a test email', async ({ page, request }) => {
  const info = await lab(request);
  await login(page);
  await page.goto('/#/settings/email');
  const form = page.getByTestId('smtp-form');
  if (!REAL_SMTP) {
    await form.locator('#sm-host').fill(info.mailpit.smtpHost);
    await form.locator('#sm-port').fill(String(info.mailpit.smtpPort));
    await form.locator('#sm-tls').selectOption('none');
    await form.locator('#sm-from').fill('Meili CDC <cdc@example.com>');
    await form.locator('#sm-url').fill('http://127.0.0.1:8780');
    await page.getByTestId('smtp-save').click();
    await expect(page.getByTestId('notify-status')).toContainText(/Configured/i);
  }
  await page.locator('#sm-to').fill(ALERT_EMAIL);
  await page.getByTestId('smtp-test').click();
  await expect(page.getByText(/Test email sent|sent to/i).first()).toBeVisible({ timeout: 60_000 });
  if (!REAL_SMTP) {
    const mail = await waitMail(request, ALERT_EMAIL, /test/i);
    expect(mail.text).toBeTruthy();
  }
});
