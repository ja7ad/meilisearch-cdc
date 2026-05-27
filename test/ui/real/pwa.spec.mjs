// The dashboard as a progressive web app, against the real service: the
// service worker takes control, the manifest is installable and the app
// shell opens without a network.
import { test, expect } from '@playwright/test';

test('installable manifest, service worker and offline app shell', async ({ page, context }) => {
  await page.goto('/');
  const manifest = await (await page.request.get('/manifest.webmanifest')).json();
  expect(manifest).toMatchObject({ name: 'Meili CDC', display: 'standalone', start_url: './#/overview' });
  expect(manifest.icons.map((i) => i.sizes)).toEqual(expect.arrayContaining(['192x192', '512x512']));
  expect(manifest.icons.some((i) => i.purpose === 'maskable')).toBe(true);

  // The worker installs, caches the shell and controls the page after a reload.
  await page.waitForFunction(async () => !!(await navigator.serviceWorker.getRegistration())?.active, null, { timeout: 30_000 });
  await page.reload();
  await page.waitForFunction(() => !!navigator.serviceWorker.controller, null, { timeout: 30_000 });
  const caches = await page.evaluate(async () => (await window.caches.keys()));
  expect(caches.some((k) => /^cdc-shell-[0-9a-f]{12}$/.test(k))).toBe(true);
  // Live data is never cached.
  const cachedApi = await page.evaluate(async () => {
    for (const k of await window.caches.keys()) {
      for (const r of await (await window.caches.open(k)).keys()) if (new URL(r.url).pathname.startsWith('/api/')) return r.url;
    }
    return '';
  });
  expect(cachedApi).toBe('');

  // Offline: the app shell still loads from the cache.
  await context.setOffline(true);
  await page.reload();
  await expect(page).toHaveTitle(/Meili CDC/);
  const offline = page.getByTestId('boot-error');
  await expect(offline).toContainText('Cannot reach the server');
  await expect(offline).toContainText('reconnects by itself');
  // Back online: it reconnects without a click.
  await context.setOffline(false);
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible({ timeout: 15_000 });
});
