// Service worker of the dashboard: the app shell (index.html and assets)
// works offline and opens instantly; live data is never cached. The server
// fills in BUILD with a hash of the embedded files, so every release gets
// a new cache and the old one is removed.
const BUILD = '__BUILD__';
const CACHE = `cdc-shell-${BUILD}`;
const SHELL = ['./', 'index.html', 'manifest.webmanifest', 'assets/app.js', 'assets/styles.css', 'assets/theme-init.js',
  'assets/vendor/htm-preact.js', 'assets/favicon.svg', 'assets/icons/icon-192.png'];
// Requests that must always reach the server.
const LIVE = /^\/(api|mcp|metrics|healthz|debug)(\/|$)/;

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)));
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    for (const key of await caches.keys()) if (key.startsWith('cdc-shell-') && key !== CACHE) await caches.delete(key);
    await self.clients.claim();
  })());
});

// The page asks a waiting worker to take over after the user accepts an update.
self.addEventListener('message', (event) => {
  if (event.data === 'skip-waiting') self.skipWaiting();
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== 'GET' || url.origin !== self.location.origin || LIVE.test(url.pathname)) return;

  // Pages: network first (fresh index.html), the cached shell when offline.
  if (req.mode === 'navigate') {
    event.respondWith((async () => {
      try {
        const res = await fetch(req);
        const c = await caches.open(CACHE);
        c.put('index.html', res.clone());
        return res;
      } catch {
        return (await caches.match('index.html')) || new Response('Offline', { status: 503, headers: { 'Content-Type': 'text/plain' } });
      }
    })());
    return;
  }

  // Static files: from the cache, refreshed in the background.
  event.respondWith((async () => {
    const c = await caches.open(CACHE);
    const hit = await c.match(req);
    const update = fetch(req).then((res) => {
      if (res.ok) c.put(req, res.clone());
      return res;
    }).catch(() => hit);
    return hit || update;
  })());
});
