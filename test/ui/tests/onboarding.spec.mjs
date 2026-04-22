import { test, expect } from './fixtures.mjs';

test.describe('zero configuration', () => {
  test.use({ role: null, mockState: 'empty' });

  test('setup → onboarding: destination, connector, first sync', async ({ page, consoleErrors }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Create the first admin' })).toBeVisible();
    await page.getByLabel('Email').fill('owner@example.com');
    await page.getByLabel('Password', { exact: true }).fill('owner password 1');
    await page.getByLabel('Confirm password').fill('owner password 1');
    await page.getByTestId('setup-submit').click();

    const ob = page.getByTestId('onboarding');
    await expect(ob).toBeVisible();
    await expect(ob.getByRole('heading', { name: 'Connect Meilisearch' })).toBeVisible();
    await ob.getByLabel('URL').fill('http://localhost:7700');
    await ob.getByLabel('API key').fill('masterKey');
    await ob.getByRole('button', { name: 'Test connection' }).click();
    await expect(page.getByTestId('ob-dest-test')).toContainText('v1.22.1');
    await page.getByTestId('ob-dest-save').click();

    await expect(ob.getByRole('heading', { name: 'Connect a data source' })).toBeVisible();
    await ob.locator('[data-source="postgres"]').click();
    await ob.getByLabel('Connector name').fill('Shop DB');
    await ob.getByLabel('Connection string (DSN)').fill('postgres://cdc:pw@db:5432/shop');
    await ob.getByRole('button', { name: 'Test connection' }).click();
    await expect(page.getByTestId('ob-conn-test')).toContainText('9 tables');
    await page.getByTestId('ob-conn-save').click();

    await expect(ob.getByRole('heading', { name: 'Create your first sync' })).toBeVisible();
    await ob.getByRole('group', { name: 'Tables' }).getByRole('button', { name: /^categories/ }).click();
    await page.getByTestId('ob-open-wizard').click();
    await expect(page.getByRole('heading', { level: 1, name: 'New task' })).toBeVisible();
    await expect(page.getByTestId('wizard-connector')).toContainText('Shop DB');
    await expect(page.getByRole('textbox', { name: 'Table', exact: true })).toHaveValue('categories');
    expect(consoleErrors).toEqual([]);
  });

  test('skip and dismiss; checklist stays on the overview', async ({ page }) => {
    await page.goto('/');
    await page.getByLabel('Email').fill('owner@example.com');
    await page.getByLabel('Password', { exact: true }).fill('owner password 1');
    await page.getByLabel('Confirm password').fill('owner password 1');
    await page.getByTestId('setup-submit').click();
    await expect(page.getByTestId('onboarding')).toBeVisible();
    await page.getByTestId('ob-skip').click();
    await expect(page.getByRole('heading', { name: 'Connect a data source' })).toBeVisible();
    await expect(page.locator('.ob-step.is-done')).toHaveCount(1);
    await page.getByTestId('ob-dismiss').click();
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
    await expect(page.getByTestId('destinations-strip')).toContainText('No Meilisearch destination');
    // dismissed: no redirect on reload, and the checklist was hidden too
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
    await expect(page.getByTestId('getting-started')).toHaveCount(0);
    // empty states point to the next action
    await page.goto('/#/indexes');
    await expect(page.getByRole('link', { name: 'Add a Meilisearch destination' })).toBeVisible();
  });

  test('checklist shows progress until complete', async ({ page }) => {
    await page.goto('/');
    await page.getByLabel('Email').fill('owner@example.com');
    await page.getByLabel('Password', { exact: true }).fill('owner password 1');
    await page.getByLabel('Confirm password').fill('owner password 1');
    await page.getByTestId('setup-submit').click();
    await expect(page.getByTestId('onboarding')).toBeVisible();
    await page.goto('/#/overview');
    const gs = page.getByTestId('getting-started');
    await expect(gs).toContainText('0/3');
    await gs.getByRole('link', { name: 'Add destination' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Destinations' })).toBeVisible();
  });
});
