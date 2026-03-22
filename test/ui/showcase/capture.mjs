// Captures the dashboard of the showcase service into docs/img/raw (2x
// screenshots). Run after setup.mjs, with traffic.mjs running for a few
// minutes so charts have history.
//   node test/ui/showcase/capture.mjs [only-name ...]
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, devices } from '../node_modules/playwright/index.mjs';

const BASE = process.env.SHOWCASE_URL || 'http://127.0.0.1:8781';
const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');
const OUT = path.join(ROOT, 'docs/img/raw');
const only = process.argv.slice(2);
fs.mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch();

async function session(opts) {
  // Reduced motion: entrance animations (cards revealed on scroll) render at once.
  const ctx = await browser.newContext({ baseURL: BASE, reducedMotion: 'reduce', ...opts });
  const page = await ctx.newPage();
  await page.goto('/#/login');
  await page.getByLabel('Email').fill('alex@acme.io');
  await page.getByLabel('Password').fill('showcase-admin-password');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((u) => !u.hash.startsWith('#/login'));
  await page.evaluate(() => { try { localStorage.setItem(`cdc.onboarding.dismissed.${''}`, '1'); } catch { /* ignore */ } });
  const dismiss = page.getByTestId('ob-dismiss');
  if (await dismiss.isVisible({ timeout: 2500 }).catch(() => false)) await dismiss.click();
  // Toasts (sign-in, actions) would cover the content.
  await ctx.addInitScript(() => {
    const hide = () => { const s = document.createElement('style'); s.textContent = '.toaster{display:none!important}'; document.head.appendChild(s); };
    if (document.head) hide(); else document.addEventListener('DOMContentLoaded', hide);
  });
  await page.addStyleTag({ content: '.toaster{display:none!important}' });
  return { ctx, page };
}

const ids = async (page) => page.evaluate(async () => {
  const j = (u) => fetch(u).then((r) => r.json());
  const [tasks, conns, dests] = await Promise.all([j('/api/v1/tasks'), j('/api/v1/connectors'), j('/api/v1/destinations')]);
  const t = (n) => tasks.find((x) => x.name === n).id;
  return {
    catalog: t('Product catalog'), customers: t('Customers'), events: t('Storefront events'),
    pg: conns.find((c) => c.name.startsWith('shop-db')).id,
    eu: dests.find((d) => d.name === 'Production (EU)').id,
  };
});

async function shot(page, name, hash, prepare, { fullPage = false } = {}) {
  if (only.length && !only.some((o) => name.includes(o))) return;
  if (hash !== null) await page.goto(`/#/${hash}`);
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(1200);
  if (prepare) await prepare(page);
  await page.waitForTimeout(900); // let entrance animations settle
  await page.mouse.move(0, 0);
  await page.screenshot({ path: path.join(OUT, `${name}.png`), fullPage });
  console.log(`✓ ${name}`);
}

// ---------------------------------------------------------------- desktop, light
{
  const { ctx, page } = await session({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: 'light' });
  const id = await ids(page);
  await shot(page, '01-overview', 'overview');
  await shot(page, '02-tasks', 'tasks');
  await shot(page, '03-task-detail', `tasks/${id.customers}`);
  await shot(page, '04-wizard-connector', 'tasks/new');
  await shot(page, '05-wizard-source', 'tasks/new', async (p) => {
    await p.locator('[data-connector]', { hasText: 'shop-db' }).click();
    await p.getByTestId('wizard-next').click();
    await p.getByRole('textbox', { name: 'Name', exact: true }).fill('Customer search (APAC)');
    await p.locator('[data-table="customers"]').click();
    await p.getByTestId('test-connection').click();
    await p.getByTestId('test-result').waitFor();
  });
  await shot(page, '06-wizard-mapping', null, async (p) => {
    await p.getByTestId('wizard-next').click();
    await p.getByTestId('mapping-columns').waitFor();
    await p.getByRole('checkbox', { name: 'Exclude email' }).check();
    await p.getByLabel('Rename lifetime_value').fill('ltv');
  });
  await shot(page, '07-wizard-index-settings', null, async (p) => {
    await p.getByTestId('wizard-next').click();
    await p.getByLabel('Index uid').fill('customers_apac');
    await p.getByRole('checkbox', { name: 'Filter: country' }).check();
    await p.getByRole('checkbox', { name: 'Filter: tier' }).check();
    await p.getByRole('checkbox', { name: 'Sort: created_at' }).check();
    await p.getByText('Manage index settings').click();
    await p.locator('[data-destination-card]').first().scrollIntoViewIfNeeded();
  });
  await shot(page, '08-schema-diff', `tasks/${id.catalog}/diff`, async (p) => {
    await p.getByTestId('run-diff').click();
    await p.getByTestId('diff-tab').getByText(/In sync|Out of sync/).first().waitFor({ timeout: 60_000 });
  });
  await shot(page, '09-connectors', 'connectors');
  await shot(page, '10-connector-detail', `connectors/${id.pg}`, async (p) => {
    await p.getByTestId('connector-detail-test').click();
    await p.getByTestId('connector-test').waitFor();
  });
  await shot(page, '11-destinations', 'destinations');
  await shot(page, '12-destination-detail', `destinations/${id.eu}`);
  await shot(page, '13-indexes', 'indexes');
  await shot(page, '14-search', `indexes/products?destination=${id.eu}`, async (p) => {
    await p.getByTestId('search-q').fill('light');
    await p.getByTestId('search-filter').fill('category = garden');
    await p.getByTestId('search-hits').locator('.hit').first().waitFor();
  });
  await shot(page, '15-index-settings', `indexes/products/settings?destination=${id.eu}`);
  await shot(page, '16-meili-tasks', 'meili-tasks');
  await shot(page, '17-alerts', 'alerts');
  await shot(page, '18-users', 'settings/users');
  await shot(page, '19-mcp', 'settings/mcp');
  await shot(page, '20-email', 'settings/email');
  await shot(page, '24-dlq', `tasks/${id.events}/dlq`);
  await ctx.close();
}

// ---------------------------------------------------------------- desktop, dark
{
  const { ctx, page } = await session({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: 'dark' });
  await shot(page, '21-dark', 'overview');
  await ctx.close();
}

// ---------------------------------------------------------------- phone
{
  // The whole phone screen (390x844 at 2x), not just Safari's viewport, so it
  // fills a phone frame.
  const phone = { ...devices['iPhone 13'], viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 };
  delete phone.defaultBrowserType;
  const { ctx, page } = await session({ ...phone, colorScheme: 'light' });
  const id = await ids(page);
  await shot(page, 'mobile-overview', 'overview');
  await shot(page, 'mobile-task', `tasks/${id.customers}`);
  await shot(page, 'mobile-wizard', 'tasks/new');
  await shot(page, 'mobile-indexes', 'indexes');
  await ctx.close();
}

await browser.close();
