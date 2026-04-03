import { test, expect, open } from './fixtures.mjs';

test('lists rules with summaries and firing alerts', async ({ page, consoleErrors }) => {
  await open(page, '#/alerts', 'Alerts');
  await expect(page.getByTestId('active-alert')).toHaveCount(2);
  await expect(page.getByTestId('nav-badge-alerts')).toHaveText('2');
  await expect(page.locator('[data-rule="r_failed"] [data-testid="rule-summary"]')).toHaveText('Task failed/held for 1m on all tasks · notify 2 emails, 1 Telegram chat');
  await expect(page.locator('[data-rule="r_lag"] [data-testid="rule-summary"]')).toContainText('on Products catalog');
  await expect(page.locator('[data-rule="r_meili"] [data-testid="rule-summary"]')).toHaveText('Meilisearch down for 1m on Production, EU replica · notify 1 email');
  await expect(page.getByTestId('alert-timeline')).toContainText('EU replica is reachable again');
  await expect(page.getByTestId('alert-timeline').locator('.tl-item')).toHaveCount(7);
  await page.getByRole('group', { name: 'Event state' }).getByRole('button', { name: 'Resolved' }).click();
  await expect(page.getByTestId('alert-timeline').locator('.tl-item')).toHaveCount(3);
  expect(consoleErrors).toEqual([]);
});

test('rule CRUD, enable toggle and test send', async ({ page }) => {
  await page.request.post('/__mock/smtp', { data: { configured: true } });
  await open(page, '#/alerts', 'Alerts');
  await expect(page.getByTestId('channel-gate')).toHaveCount(0);
  await page.getByTestId('new-rule').click();
  const dialog = page.getByRole('dialog', { name: 'New alert rule' });
  await page.getByTestId('rule-save').click();
  await expect(dialog.getByText('Give the rule a name')).toBeVisible();
  await expect(dialog.getByText(/Add at least one recipient/)).toBeVisible();
  // Telegram is not configured: shown as a compact disabled row
  await expect(dialog.getByTestId('channel-off-telegram')).toContainText('not configured');
  await expect(dialog.getByLabel('Telegram chat ids')).toHaveCount(0);
  await dialog.getByLabel('Name', { exact: true }).fill('Orders lag');
  await dialog.getByLabel('Condition').selectOption('lag');
  await dialog.getByLabel('Lag threshold (seconds)').fill('30');
  await dialog.getByText('All tasks', { exact: true }).click();
  await dialog.getByRole('group', { name: 'Tasks' }).getByRole('button', { name: 'Orders' }).click();
  await dialog.getByRole('group', { name: 'Tasks' }).getByRole('button', { name: 'Products catalog' }).click();
  await dialog.getByText('Critical', { exact: true }).click();
  await dialog.getByLabel('Email recipients').fill('bad address');
  await dialog.getByLabel('Email recipients').press('Enter');
  await expect(dialog.getByText('is not an email address')).toBeVisible();
  await dialog.getByLabel('Email recipients').fill('dba@example.com');
  await dialog.getByLabel('Email recipients').press('Enter');
  await page.getByTestId('rule-save').click();
  await expect(page.getByText('Rule created')).toBeVisible();
  const card = page.locator('[data-rule]').filter({ hasText: 'Orders lag' });
  await expect(card.getByTestId('rule-summary')).toHaveText('Lag over 30.0 s for 1m on Orders · notify 1 email');

  await card.getByRole('button', { name: 'Send test for Orders lag' }).click();
  await expect(card.getByTestId('rule-test-results')).toContainText('dba@example.com');
  await expect(page.getByText('Test notification sent')).toBeVisible();

  await card.getByRole('switch', { name: 'Enable rule Orders lag' }).uncheck();
  await expect(page.getByText('Orders lag disabled')).toBeVisible();
  await expect(card).toHaveClass(/is-disabled/);

  await card.getByRole('button', { name: 'Edit rule Orders lag' }).click();
  const edit = page.getByRole('dialog', { name: 'Edit alert rule' });
  await expect(edit.getByLabel('Lag threshold (seconds)')).toHaveValue('30');
  await edit.getByLabel('Name', { exact: true }).fill('Orders lag (prod)');
  await page.getByTestId('rule-save').click();
  await expect(page.getByText('Rule saved')).toBeVisible();

  const edited = page.locator('[data-rule]').filter({ hasText: 'Orders lag (prod)' });
  await edited.getByRole('button', { name: 'Delete rule Orders lag (prod)' }).click();
  await page.getByRole('dialog').getByTestId('confirm-ok').click();
  await expect(page.getByText('Rule deleted')).toBeVisible();
  await expect(edited).toHaveCount(0);
});

test('without SMTP or Telegram the page asks for a channel first', async ({ page }) => {
  await open(page, '#/alerts', 'Alerts');
  const gate = page.getByTestId('channel-gate');
  await expect(gate).toContainText('Set up a notification channel first');
  await expect(page.getByTestId('new-rule')).toHaveCount(0);
  await expect(page.locator('[data-rule="r_failed"] [data-testid="rule-undeliverable"]')).toHaveText(['Email not configured', 'Telegram not configured']);
  // enabling a rule is refused by the server and explained
  await page.getByRole('switch', { name: 'Enable rule New dead letters' }).check();
  await expect(page.getByRole('alert').filter({ hasText: 'notification channel' })).toBeVisible();
  await expect(page.getByRole('switch', { name: 'Enable rule New dead letters' })).not.toBeChecked();
  await gate.getByRole('link', { name: /Connect a Telegram bot/ }).click();
  await expect(page).toHaveURL(/#\/settings\/telegram$/);
  const form = page.getByTestId('telegram-form');
  await form.getByLabel('Bot token').fill('123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw');
  await page.getByTestId('telegram-save').click();
  await expect(page.getByText('Connected to @meili_cdc_bot')).toBeVisible();
  await page.goto('/#/alerts');
  await expect(page.getByTestId('channel-gate')).toHaveCount(0);
  await expect(page.getByTestId('new-rule')).toBeVisible();
});

test('configuring a channel with no rules suggests creating one', async ({ page }) => {
  for (const id of ['r_failed', 'r_lag', 'r_dlq', 'r_meili']) await page.request.delete(`/api/v1/alerts/rules/${id}`);
  await open(page, '#/settings/telegram', 'Settings');
  await page.getByTestId('telegram-form').getByLabel('Bot token').fill('123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw');
  await page.getByTestId('telegram-save').click();
  await page.getByRole('link', { name: 'Create an alert rule' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Alerts' })).toBeVisible();
});

test('a firing alert shows a toast, the sidebar badge and the task badge', async ({ page }) => {
  await open(page, '#/tasks/events-kafka', 'Inventory events');
  await expect(page.getByTestId('nav-badge-alerts')).toHaveText('2');
  await expect(page.getByTestId('task-alert-badge')).toHaveCount(0);
  await page.request.post('/__mock/alert', { data: { ruleId: 'r_failed', taskId: 'events-kafka', state: 'firing', message: 'Inventory events is retrying for 5m' } });
  await expect(page.getByRole('alert').filter({ hasText: 'Alert: Task failed or held' })).toContainText('Inventory events is retrying for 5m');
  await expect(page.getByTestId('nav-badge-alerts')).toHaveText('3');
  await expect(page.getByTestId('task-alert-badge')).toHaveText('1 alert firing');
  await page.request.post('/__mock/alert', { data: { ruleId: 'r_failed', taskId: 'events-kafka', state: 'resolved' } });
  await expect(page.getByTestId('nav-badge-alerts')).toHaveText('2');
  await expect(page.getByTestId('task-alert-badge')).toHaveCount(0);

  await open(page, '#/overview', 'Overview');
  await expect(page.getByTestId('kpi-alerts')).toContainText('2');
  await expect(page.getByTestId('kpi-alerts')).toContainText('2 critical');
  await page.getByTestId('kpi-alerts').click();
  await expect(page.getByRole('heading', { level: 1, name: 'Alerts' })).toBeVisible();
});
