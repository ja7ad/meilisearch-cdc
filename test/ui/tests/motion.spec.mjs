import { test, expect, open } from './fixtures.mjs';

const dur = (page, sel, pseudo) => page.evaluate(([s, ps]) => {
  const el = document.querySelector(s);
  return el ? getComputedStyle(el, ps || null).animationDuration : null;
}, [sel, pseudo]);

test.describe('reduced motion', () => {
  test.use({ reducedMotion: 'reduce' });
  test('non-essential animation is disabled', async ({ page }) => {
    await open(page, '#/overview', 'Overview');
    expect(parseFloat(await dur(page, '.page'))).toBeLessThan(0.01);
    const ping = await page.evaluate(() => getComputedStyle(document.querySelector('.badge.is-live .dot'), '::after').display);
    expect(ping).toBe('none');
    expect(parseFloat(await dur(page, '.task-card'))).toBeLessThan(0.01);
    // counters jump straight to the live value instead of tweening
    const kpi = page.getByTestId('kpi-tasks').locator('.kpi-value');
    await expect(kpi).toHaveText('7');
  });
});

test.describe('default motion', () => {
  test.use({ reducedMotion: 'no-preference' });
  test('page transitions and live pulses animate', async ({ page }) => {
    await open(page, '#/overview', 'Overview');
    expect(parseFloat(await dur(page, '.page'))).toBeGreaterThan(0.2);
    expect(parseFloat(await dur(page, '.badge.is-live .dot', '::after'))).toBeGreaterThan(1);
  });
});
