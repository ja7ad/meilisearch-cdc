// Steady, production-like change traffic for the showcase: order status
// changes and new orders, customer updates (PostgreSQL) and storefront
// events pushed to the webhook task (a few malformed ones end in the DLQ).
//   node test/ui/showcase/traffic.mjs [seconds]
const BASE = process.env.SHOWCASE_URL || 'http://127.0.0.1:8781';
const LAB = process.env.LAB_URL || 'http://127.0.0.1:8799';
const seconds = Number(process.argv[2] || 600);

const login = await fetch(`${BASE}/api/v1/auth/login`, { method: 'POST', headers: { 'Content-Type': 'application/json', Origin: BASE },
  body: JSON.stringify({ email: 'alex@acme.io', password: 'showcase-admin-password' }) });
const cookie = login.headers.get('set-cookie').split(';')[0];
const tasks = await (await fetch(`${BASE}/api/v1/tasks`, { headers: { Cookie: cookie } })).json();
const webhook = tasks.find((t) => t.name === 'Storefront events');
const sql = (statement) => fetch(`${LAB}/sql`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ sql: statement }) });
const ingest = (body) => fetch(`${BASE}/api/v1/ingest/${webhook.id}`, { method: 'POST', headers: { 'Content-Type': 'application/x-ndjson', Cookie: cookie, Origin: BASE }, body });

let nextOrder = 20001;
let nextEvent = 1;
const kinds = ['page_view', 'add_to_cart', 'checkout', 'search', 'wishlist'];
const end = Date.now() + seconds * 1000;
let tick = 0;
while (Date.now() < end) {
  tick++;
  // Busier every few seconds, like real traffic.
  const burst = 1 + Math.round(2 * (1 + Math.sin(tick / 15)));
  const from = 1 + Math.floor(Math.random() * 19000);
  await sql(`UPDATE orders SET status = (ARRAY['paid','shipped','delivered'])[1 + (id + ${tick}) % 3], updated_at = now()
    WHERE id BETWEEN ${from} AND ${from + 40 * burst}`);
  await sql(`INSERT INTO orders SELECT g, 1 + g % 5000, 'pending', 20 + (g % 300) * 1.5, 'EUR', 1 + g % 4, 'web', now(), now()
    FROM generate_series(${nextOrder}, ${nextOrder + 4 * burst}) g`);
  nextOrder += 4 * burst + 1;
  if (tick % 3 === 0) {
    const c = 1 + Math.floor(Math.random() * 4990);
    await sql(`UPDATE customers SET lifetime_value = lifetime_value + 19.9, updated_at = now() WHERE id BETWEEN ${c} AND ${c + 10}`);
  }
  const events = [];
  for (let i = 0; i < 10 * burst; i++, nextEvent++) {
    events.push(JSON.stringify({ op: 'upsert', row: { id: nextEvent, type: kinds[nextEvent % kinds.length], session: `s-${nextEvent % 977}`,
      path: ['/', '/search', '/p/lamp-40', '/cart', '/checkout'][nextEvent % 5], at: new Date().toISOString() } }));
  }
  // Rarely, a client sends a malformed embedding: Meilisearch rejects it, the DLQ keeps it.
  if (tick % 40 === 7) events.push(JSON.stringify({ op: 'upsert', row: { id: nextEvent++, type: 'search', _vectors: 'not-a-vector' } }));
  await ingest(events.join('\n'));
  await new Promise((r) => setTimeout(r, 200));
}
