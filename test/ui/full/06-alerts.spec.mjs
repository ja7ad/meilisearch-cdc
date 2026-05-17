// Step 6: alerts by email. A rule watches task states; pausing a task in
// the GUI fires it and resuming resolves it. Both notifications are real
// emails: in Mailpit, or in ALERT_EMAIL's inbox with REAL_SMTP=1.
import { test, expect, login, waitMail, taskAction, taskId, REAL_SMTP, ALERT_EMAIL } from './helpers.mjs';

test.describe.configure({ mode: 'serial' });
const RULE = 'E2E task paused';
const TASK = 'E2E mongodb products';

test('create an email alert rule and send a test', async ({ page, request }) => {
  await login(page);
  await page.goto('/#/alerts');
  await expect(page.getByTestId('channel-gate')).toHaveCount(0); // SMTP is configured
  if (!(await page.getByTestId('rule-list').getByText(RULE).count())) {
    await page.getByTestId('new-rule').click();
    const d = page.getByRole('dialog');
    await d.locator('#ar-name').fill(RULE);
    await d.locator('#ar-type').selectOption('task_state');
    const chips = d.getByRole('group', { name: 'States' });
    for (const s of ['failed', 'held', 'paused']) {
      const chip = chips.locator(`[data-chip="${s}"]`);
      if ((await chip.getAttribute('aria-pressed')) !== 'true') await chip.click();
    }
    await d.locator('#ar-for').fill('0s');
    await d.locator('#ar-emails').fill(ALERT_EMAIL);
    await d.locator('#ar-emails').press('Enter');
    await d.locator('#ar-resolved').check();
    await d.getByTestId('rule-save').click();
    await expect(page.getByRole('dialog')).toHaveCount(0);
  }
  await expect(page.getByTestId('rule-list')).toContainText(RULE);
  await page.getByRole('button', { name: `Send test for ${RULE}` }).click();
  await expect(page.getByTestId('rule-test-results')).toContainText(ALERT_EMAIL);
  await expect(page.getByTestId('rule-test-results')).not.toContainText(/fail|error/i);
  if (!REAL_SMTP) await waitMail(request, ALERT_EMAIL, /^\[TEST · WARNING\] E2E task paused/);
});

test('pausing a task fires the alert by email; resuming resolves it', async ({ page, request }) => {
  await login(page);
  const id = await taskId(page, TASK);
  await page.goto(`/#/tasks/${id}`);
  await taskAction(page, 'pause');
  await expect(page.locator('[data-state="paused"]').first()).toBeVisible();

  await page.goto('/#/alerts');
  await expect(page.getByTestId('active-alert').filter({ hasText: TASK })).toBeVisible({ timeout: 60_000 });
  if (!REAL_SMTP) {
    const fired = await waitMail(request, ALERT_EMAIL, new RegExp(RULE));
    expect(fired.text).toContain(TASK);
  }

  await page.goto(`/#/tasks/${id}`);
  await taskAction(page, 'resume');
  await expect(page.locator('[data-state="streaming"]').first()).toBeVisible();
  await page.goto('/#/alerts');
  await expect(page.getByTestId('active-alert').filter({ hasText: TASK })).toHaveCount(0, { timeout: 60_000 });
  if (!REAL_SMTP) await waitMail(request, ALERT_EMAIL, /resolved/i);
  // The timeline records both, with their email deliveries.
  await expect(page.getByTestId('alert-timeline')).toContainText(TASK);
});
