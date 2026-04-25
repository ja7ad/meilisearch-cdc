// Visual review helper: SCREENSHOTS=1 npx playwright test screenshots
// Saves every page in light + dark at desktop width, and light + dark at 375px.
import { test, PAGES, PUBLIC, open } from './fixtures.mjs';

test.skip(!process.env.SCREENSHOTS, 'set SCREENSHOTS=1 to capture screenshots');

const VIEWS = [
  { tag: 'desktop', viewport: { width: 1360, height: 900 } },
  { tag: 'mobile', viewport: { width: 390, height: 844 } },
  ...(process.env.SHOT_EXTRA ? [{ tag: 'tablet', viewport: { width: 768, height: 1024 } }, { tag: 'landscape', viewport: { width: 844, height: 390 } }] : []),
];
const only = process.env.SHOT_PAGES ? process.env.SHOT_PAGES.split(',') : null;

for (const v of VIEWS) {
  for (const theme of ['light', 'dark']) {
    for (const p of PAGES.filter((x) => !only || only.includes(x.name))) {
      test(`${p.name} ${theme} ${v.tag}`, async ({ page }) => {
        await page.setViewportSize(v.viewport);
        await page.addInitScript((t) => localStorage.setItem('cdc.theme', t), theme);
        await open(page, p.hash, p.heading);
        await page.waitForTimeout(2200);
        await page.screenshot({ path: `screenshots/${p.name}-${theme}-${v.tag}.png`, fullPage: true });
      });
    }
  }
}

// Secondary states (desktop, both themes)
const EXTRA = [
  { name: 'task-logs', hash: '#/tasks/products-pg/logs', heading: 'Products catalog' },
  { name: 'task-schema', hash: '#/tasks/products-pg/schema', heading: 'Products catalog' },
  { name: 'task-diff', hash: '#/tasks/products-pg/diff', heading: 'Products catalog' },
  { name: 'task-dlq', hash: '#/tasks/events-kafka/dlq', heading: 'Inventory events' },
  { name: 'task-failed', hash: '#/tasks/events-kafka', heading: 'Inventory events' },
  { name: 'index-settings', hash: '#/indexes/products/settings', heading: 'products',
    setup: async (page) => {
      const ta = page.getByTestId('index-settings-json');
      const cur = JSON.parse(await ta.inputValue());
      cur.filterableAttributes.push('price'); cur.stopWords = ['the'];
      await ta.fill(JSON.stringify(cur, null, 2));
      await page.getByTestId('preview-settings').click();
      await page.getByTestId('settings-changes').waitFor();
    } },
  { name: 'wizard-mapping', hash: '#/tasks/new', heading: 'New task',
    setup: async (page) => {
      await page.locator('[data-connector="c_shop_pg"]').click();
      await page.getByTestId('wizard-next').click();
      await page.getByLabel('Name').fill('Catalog');
      await page.getByRole('textbox', { name: 'Table', exact: true }).fill('products');
      await page.getByTestId('test-connection').click();
      await page.getByTestId('test-result').waitFor();
      await page.waitForTimeout(600);
      await page.screenshot({ path: `screenshots/wizard-connection-${page.__theme}-desktop.png`, fullPage: true });
      await page.getByTestId('wizard-next').click();
    } },
  { name: 'wizard-index', hash: '#/tasks/new', heading: 'New task',
    setup: async (page) => {
      await page.locator('[data-connector="c_shop_pg"]').click();
      await page.getByTestId('wizard-next').click();
      await page.getByLabel('Name').fill('Catalog');
      await page.getByRole('textbox', { name: 'Table', exact: true }).fill('products');
      await page.getByTestId('test-connection').click();
      await page.getByTestId('test-result').waitFor();
      for (let i = 0; i < 2; i++) await page.getByTestId('wizard-next').click();
      await page.getByRole('checkbox', { name: 'Filter: category' }).check();
    } },
  { name: 'wizard-sync', hash: '#/tasks/new', heading: 'New task',
    setup: async (page) => {
      await page.locator('[data-connector="c_app_mongo"]').click();
      await page.getByTestId('wizard-next').click();
      await page.getByLabel('Name').fill('Users');
      await page.getByLabel('Collection').fill('users');
      for (let i = 0; i < 3; i++) await page.getByTestId('wizard-next').click();
    } },
  { name: 'wizard-review', hash: '#/tasks/new', heading: 'New task',
    setup: async (page) => {
      await page.locator('[data-connector="c_kafka"]').click();
      await page.getByTestId('wizard-next').click();
      await page.getByLabel('Name').fill('Events');
      await page.getByLabel('Topic').fill('db.events');
      for (let i = 0; i < 4; i++) await page.getByTestId('wizard-next').click();
    } },
];
for (const theme of ['light', 'dark']) {
  for (const p of EXTRA.filter((x) => !only || only.includes(x.name))) {
    test(`${p.name} ${theme} desktop`, async ({ page }) => {
      page.__theme = theme;
      await page.setViewportSize({ width: 1360, height: 900 });
      await page.addInitScript((t) => localStorage.setItem('cdc.theme', t), theme);
      await open(page, p.hash, p.heading);
      await page.waitForTimeout(800);
      if (p.setup) await p.setup(page);
      await page.waitForTimeout(1500);
      await page.screenshot({ path: `screenshots/${p.name}-${theme}-desktop.png`, fullPage: true });
    });
  }
}

// Public pages (no session)
test.describe('public', () => {
  test.use({ role: null });
  for (const v of VIEWS) {
    for (const theme of ['light', 'dark']) {
      for (const p of [...PUBLIC, { name: 'invite', hash: null, heading: "You're invited" }].filter((x) => !only || only.includes(x.name))) {
        test(`${p.name} ${theme} ${v.tag}`, async ({ page }) => {
          let hash = p.hash;
          if (!hash) {
            // log in as admin through the API, create an invitation, log out again
            await page.request.post('/__mock/login?role=admin');
            const r = await (await page.request.post('/api/v1/invitations', { data: { email: 'new.person@example.com', role: 'moderator' } })).json();
            await page.request.post('/api/v1/auth/logout');
            hash = r.link.slice(r.link.indexOf('#'));
          }
          await page.setViewportSize(v.viewport);
          await page.addInitScript((t) => localStorage.setItem('cdc.theme', t), theme);
          await open(page, hash, p.heading);
          await page.waitForTimeout(900);
          await page.screenshot({ path: `screenshots/${p.name}-${theme}-${v.tag}.png`, fullPage: true });
        });
      }
    }
  }
});
test.describe('setup', () => {
  test.use({ role: null, mockState: 'empty' });
  for (const theme of ['light', 'dark']) {
    test(`setup ${theme} desktop`, async ({ page }) => {
      await page.addInitScript((t) => localStorage.setItem('cdc.theme', t), theme);
      await open(page, '#/setup', 'Create the first admin');
      await page.waitForTimeout(900);
      await page.screenshot({ path: `screenshots/setup-${theme}-desktop.png`, fullPage: true });
    });
  }
});
