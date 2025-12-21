// Progressive web app: registers the service worker (offline app shell),
// offers updates when a new release is deployed, and exposes the browser's
// install prompt to the user menu.
import { useEffect, useState } from '../vendor/htm-preact.js';
import { toast } from './ui.js';

let deferred = null; // the beforeinstallprompt event, kept until used
const subs = new Set();
const emit = () => subs.forEach((f) => f(!!deferred));

/** Registers sw.js once the page has loaded. Browsers allow it on https and localhost only. */
export function registerServiceWorker() {
  if (!('serviceWorker' in navigator) || !window.isSecureContext) return;
  window.addEventListener('load', async () => {
    let reg;
    try { reg = await navigator.serviceWorker.register('sw.js', { scope: './' }); } catch { return; }
    let accepted = false; // set when the user clicks Reload on the update toast
    // A new release: offer to switch once its worker is installed.
    const offer = (worker) => {
      if (!worker || !navigator.serviceWorker.controller) return; // first install: nothing to update
      toast({ tone: 'info', title: 'Update available', message: 'A new version of the dashboard is ready.', timeout: 0,
        action: { label: 'Reload', onClick: () => { accepted = true; worker.postMessage('skip-waiting'); } } });
    };
    if (reg.waiting) offer(reg.waiting);
    reg.addEventListener('updatefound', () => {
      const w = reg.installing;
      w && w.addEventListener('statechange', () => { if (w.state === 'installed') offer(w); });
    });
    // Reload only for an update the user accepted: the first install also
    // changes the controller (clients.claim) and must not reload the page.
    navigator.serviceWorker.addEventListener('controllerchange', () => { if (accepted) { accepted = false; location.reload(); } });
    // Look for a new release when the tab comes back.
    document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') reg.update().catch(() => {}); });
  });
  window.addEventListener('beforeinstallprompt', (e) => { e.preventDefault(); deferred = e; emit(); });
  window.addEventListener('appinstalled', () => {
    deferred = null; emit();
    toast({ title: 'Installed', message: 'Meili CDC is now an app on this device.' });
  });
}

/** True while the browser offers installation. */
export function useInstallable() {
  const [can, setCan] = useState(!!deferred);
  useEffect(() => { subs.add(setCan); return () => subs.delete(setCan); }, []);
  return can;
}

export async function promptInstall() {
  if (!deferred) return;
  const e = deferred;
  deferred = null; emit();
  e.prompt();
  await e.userChoice.catch(() => {});
}
