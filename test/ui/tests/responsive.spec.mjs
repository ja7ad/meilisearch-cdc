// Runs on the device projects (iPhone 13, Pixel 7, Pixel 7 landscape, iPad gen 7): every page must
// fit the viewport, keep primary actions reachable and offer touch-sized targets.
import { test, expect, open, PAGES, PUBLIC } from './fixtures.mjs';

/** Elements wider than the viewport that are not inside a deliberate scroll container. */
async function overflowing(page) {
  return page.evaluate(() => {
    const W = document.documentElement.clientWidth;
    const scrollsX = (el) => { for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) { const o = getComputedStyle(p).overflowX; if (o === 'auto' || o === 'scroll' || o === 'hidden') return true; } return false; };
    const bad = [];
    for (const el of document.querySelectorAll('#main *, .auth-card *')) {
      const r = el.getBoundingClientRect();
      if (!r.width || r.right <= W + 1 || scrollsX(el)) continue;
      if (getComputedStyle(el).position === 'fixed') continue;
      bad.push(`${el.tagName.toLowerCase()}.${String(el.className.baseVal ?? el.className).split(' ').join('.')} right=${Math.round(r.right)}`);
    }
    return { docWidth: document.documentElement.scrollWidth, W, bad: bad.slice(0, 5) };
  });
}

/** Visible buttons and button-like links in the main content smaller than 44×44. */
async function smallTargets(page) {
  return page.evaluate(() => {
    const bad = [];
    for (const el of document.querySelectorAll('#main button, #main a.btn, #main [role="tab"], .auth-card button, .auth-card a.btn')) {
      const r = el.getBoundingClientRect();
      const st = getComputedStyle(el);
      if (!r.width || !r.height || st.visibility === 'hidden' || el.closest('[hidden]') || el.disabled) continue;
      if (el.closest('.json-view, .settings-helper')) continue; // secondary controls inside dense data views
      if (r.height < 43.5 || r.width < 43.5) bad.push(`${el.tagName.toLowerCase()} "${(el.getAttribute('aria-label') || el.textContent || '').trim().slice(0, 30)}" ${Math.round(r.width)}x${Math.round(r.height)}`);
    }
    return bad.slice(0, 8);
  });
}

for (const p of PAGES) {
  test(`fits the viewport: ${p.name}`, async ({ page, consoleErrors }) => {
    await open(page, p.hash, p.heading);
    await page.waitForTimeout(700);
    const o = await overflowing(page);
    expect(o.docWidth, `document wider than ${o.W}`).toBeLessThanOrEqual(o.W);
    expect(o.bad).toEqual([]);
    expect(await smallTargets(page)).toEqual([]);
    // the page's primary action is reachable
    const primary = page.locator('#main .page-actions .btn-primary, #main .detail-head .btn-primary').first();
    if (await primary.count()) {
      await primary.scrollIntoViewIfNeeded();
      const box = await primary.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(o.W + 1);
    }
    expect(consoleErrors).toEqual([]);
  });
}

test.describe('public pages', () => {
  test.use({ role: null });
  for (const p of PUBLIC) {
    test(`fits the viewport: ${p.name}`, async ({ page }) => {
      await open(page, p.hash, p.heading);
      await page.waitForTimeout(700); // let the entrance animation finish (scale)
      const o = await overflowing(page);
      expect(o.docWidth).toBeLessThanOrEqual(o.W);
      expect(o.bad).toEqual([]);
      expect(await smallTargets(page)).toEqual([]);
    });
  }
});

test('navigation works (drawer on narrow screens)', async ({ page }) => {
  await open(page, '#/overview', 'Overview');
  const nav = page.getByRole('navigation', { name: 'Main' });
  const menu = page.getByTestId('menu-button');
  if (await menu.isVisible()) {
    await expect(nav).not.toBeInViewport();
    await menu.click();
    await expect(nav).toBeInViewport();
    const link = nav.getByRole('link', { name: 'Destinations' });
    expect((await link.boundingBox()).height).toBeGreaterThanOrEqual(44);
    await link.click();
    await expect(nav).not.toBeInViewport();
  } else {
    await nav.getByRole('link', { name: 'Destinations' }).click();
  }
  await expect(page.getByRole('heading', { level: 1, name: 'Destinations' })).toBeVisible();
});

test('modal is a bottom sheet on phones and closes', async ({ page }) => {
  await open(page, '#/indexes', 'Indexes');
  await page.getByRole('button', { name: 'Create index' }).click();
  const dialog = page.getByRole('dialog', { name: 'Create index' });
  await expect(dialog).toBeVisible();
  const vp = page.viewportSize();
  await page.waitForTimeout(400);
  const box = await dialog.boundingBox();
  expect(box.height).toBeLessThanOrEqual(vp.height);
  if (vp.width <= 640) {
    expect(Math.round(box.width)).toBe(vp.width);
    expect(Math.abs(box.y + box.height - vp.height)).toBeLessThanOrEqual(2);
  }
  await dialog.getByRole('button', { name: 'Close dialog' }).click();
  await expect(dialog).toHaveCount(0);
});

test('wizard keeps Next reachable', async ({ page }) => {
  await open(page, '#/tasks/new', 'New task');
  await page.locator('[data-connector="c_shop_pg"]').click();
  const next = page.getByTestId('wizard-next');
  await expect(next).toBeInViewport();
  await next.click();
  await expect(page.getByLabel('Name')).toBeVisible();
});

test('batch bar stays on screen', async ({ page }) => {
  await open(page, '#/tasks', 'Tasks');
  await page.getByRole('checkbox', { name: 'Select Orders' }).check();
  await expect(page.getByTestId('batch-bar')).toBeInViewport();
});
