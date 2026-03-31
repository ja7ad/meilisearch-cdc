import { test, expect, open, PAGES } from './fixtures.mjs';

// Smoke check: every interactive element exposes an accessible name.
async function unnamed(page) {
  return page.evaluate(() => {
    const visible = (el) => !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length) && getComputedStyle(el).visibility !== 'hidden';
    const named = (el) => {
      if (el.getAttribute('aria-label')?.trim()) return true;
      const lb = el.getAttribute('aria-labelledby');
      if (lb && lb.split(' ').some((id) => document.getElementById(id)?.textContent.trim())) return true;
      if (el.labels && [...el.labels].some((l) => l.textContent.trim())) return true;
      if (el.getAttribute('title')?.trim()) return true;
      if (['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName)) return false;
      return !!el.textContent.trim();
    };
    const bad = [];
    for (const el of document.querySelectorAll('button, a[href], input:not([type=hidden]), select, textarea, [role=button], [role=tab], [role=switch], svg[role=img]')) {
      if (el.tagName === 'INPUT' && el.type === 'file') { if (!el.getAttribute('aria-label')) bad.push('file input'); continue; }
      if (!visible(el)) continue;
      if (!named(el)) bad.push(el.outerHTML.slice(0, 140));
    }
    return bad;
  });
}

for (const p of PAGES) {
  test(`accessible names: ${p.name}`, async ({ page }) => {
    await open(page, p.hash, p.heading);
    await page.waitForTimeout(800);
    expect(await unnamed(page)).toEqual([]);
    // one h1, landmark regions present
    await expect(page.getByRole('heading', { level: 1 })).toHaveCount(1);
    await expect(page.getByRole('main')).toBeVisible();
    await expect(page.getByRole('navigation', { name: 'Main' })).toHaveCount(1);
  });
}

test('modals: focus moves in, Escape closes, focus returns', async ({ page }) => {
  await open(page, '#/indexes', 'Indexes');
  const trigger = page.getByRole('button', { name: 'Create index' });
  await trigger.click();
  const dialog = page.getByRole('dialog', { name: 'Create index' });
  await expect(dialog).toBeVisible();
  await expect(dialog).toHaveAttribute('aria-modal', 'true');
  await expect(page.getByLabel('Index uid')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(trigger).toBeFocused();
});

test('keyboard focus is visible', async ({ page }) => {
  await open(page, '#/overview', 'Overview');
  await page.keyboard.press('Tab');
  const outline = await page.evaluate(() => getComputedStyle(document.activeElement).outlineStyle);
  expect(outline).not.toBe('none');
});

test('modal overlay covers the whole viewport including sidebar and header', async ({ page }) => {
  for (const [hash, heading, open_] of [
    ['#/indexes', 'Indexes', (p) => p.getByRole('button', { name: 'Create index' }).click()],
    ['#/destinations', 'Destinations', (p) => p.getByTestId('new-destination').click()],
    ['#/connectors', 'Connectors', (p) => p.getByTestId('new-connector').click()],
  ]) {
    await open(page, hash, heading);
    await page.waitForTimeout(450);
    await open_(page);
    await expect(page.getByRole('dialog')).toBeVisible();
    await page.waitForTimeout(300);
    const r = await page.evaluate(() => {
      const b = document.querySelector('.modal-backdrop').getBoundingClientRect();
      const atSidebar = document.elementFromPoint(20, 300);
      const atHeader = document.elementFromPoint(window.innerWidth - 30, 20);
      const dlg = document.querySelector('.modal').getBoundingClientRect();
      return { x: b.x, y: b.y, w: b.width, h: b.height, W: innerWidth, H: innerHeight,
        sidebar: atSidebar && atSidebar.classList.contains('modal-backdrop'), header: atHeader && atHeader.classList.contains('modal-backdrop'),
        dlgBottom: dlg.bottom, inOverlayRoot: !!document.querySelector('#overlay-root .modal-root') };
    });
    expect(r).toEqual(expect.objectContaining({ x: 0, y: 0, w: r.W, h: r.H, sidebar: true, header: true, inOverlayRoot: true }));
    expect(r.dlgBottom).toBeLessThanOrEqual(r.H);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
  }
});
