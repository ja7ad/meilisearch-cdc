// Builds a production-like workspace on the showcase service (deploy/e2e/
// showcase.env) for screenshots: destinations, connectors for every source,
// tasks, team, alerts, MCP and SMTP. Run after `make e2e-env` and with the
// lab serving (go run ./test/ui/lab serve -seed).
//   node test/ui/showcase/setup.mjs
const BASE = process.env.SHOWCASE_URL || 'http://127.0.0.1:8781';
const LAB = process.env.LAB_URL || 'http://127.0.0.1:8799';
const ADMIN = { email: 'alex@acme.io', password: 'showcase-admin-password' };
const MEMBER_PASSWORD = 'showcase-member-password';

let cookie = '';
async function call(method, path, body, { ok = [200, 201, 202, 204] } = {}) {
  const r = await fetch(BASE + path, {
    method, headers: { 'Content-Type': 'application/json', Origin: BASE, ...(cookie ? { Cookie: cookie } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await r.text();
  if (!ok.includes(r.status)) throw new Error(`${method} ${path}: ${r.status} ${text.slice(0, 300)}`);
  if (r.headers.get('set-cookie')) cookie = r.headers.get('set-cookie').split(';')[0];
  return text ? JSON.parse(text) : null;
}
const sql = async (statement) => {
  const r = await fetch(`${LAB}/sql`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ sql: statement }) });
  if (r.status !== 204) throw new Error(`lab sql: ${await r.text()}`);
};
const step = (msg) => console.log(`• ${msg}`);

const lab = await (await fetch(`${LAB}/info`)).json();
const src = Object.fromEntries(lab.sources.map((s) => [s.source, s]));

step('sign in and name the owner');
await call('POST', '/api/v1/auth/login', ADMIN);
await call('PATCH', '/api/v1/auth/me', { name: 'Alex Morgan' });

step('business tables in PostgreSQL (customers, orders)');
await sql(`
  DROP TABLE IF EXISTS orders; DROP TABLE IF EXISTS customers;
  CREATE TABLE customers (id int PRIMARY KEY, name text, email text, company text, country text, city text, tier text,
    lifetime_value numeric(12,2), created_at timestamptz, updated_at timestamptz);
  INSERT INTO customers SELECT g,
    (ARRAY['Ava','Liam','Noah','Mia','Lucas','Emma','Arash','Sofia','Kenji','Amara','Mateo','Zoe','Omar','Lea','Yuki','Nina'])[1 + g % 16] || ' ' ||
    (ARRAY['Fischer','Rossi','Kowalski','Haddad','Tanaka','Silva','Novak','Dubois','Larsen','Okafor','Moreau','Ivanova'])[1 + (g / 16) % 12],
    'customer' || g || '@example.com',
    (ARRAY['Northwind','Contoso','Globex','Initech','Umbrella','Hooli','Vandelay','Stark'])[1 + g % 8] || ' ' || (ARRAY['GmbH','Ltd','Inc','BV','SAS'])[1 + g % 5],
    (ARRAY['Germany','Italy','Poland','France','Japan','Brazil','Netherlands','Canada','Nigeria','Spain'])[1 + g % 10],
    (ARRAY['Berlin','Milan','Warsaw','Lyon','Osaka','Recife','Utrecht','Toronto','Lagos','Valencia'])[1 + g % 10],
    (ARRAY['free','pro','business','enterprise'])[1 + g % 4], (g % 97) * 13.7, now() - (g || ' hours')::interval, now()
  FROM generate_series(1, 5000) g;
  CREATE TABLE orders (id int PRIMARY KEY, customer_id int, status text, total numeric(12,2), currency text, items int,
    channel text, placed_at timestamptz, updated_at timestamptz);
  INSERT INTO orders SELECT g, 1 + g % 5000, (ARRAY['paid','shipped','delivered','refunded','pending'])[1 + g % 5],
    10 + (g % 400) * 1.25, (ARRAY['EUR','USD','GBP'])[1 + g % 3], 1 + g % 6, (ARRAY['web','mobile','pos','marketplace'])[1 + g % 4],
    now() - (g || ' minutes')::interval, now()
  FROM generate_series(1, 20000) g;`);

step('Meilisearch destinations');
const dest = {};
for (const [name, url, desc, interval] of [
  ['Production (EU)', lab.meilisearch[0].url, 'Frankfurt cluster serving the storefront search', '10s'],
  ['Search replica (US)', lab.meilisearch[1].url, 'Read replica for the US region', '15s'],
  ['Staging', 'http://127.0.0.1:17799', 'Pre-release testing (currently offline)', '30s'],
]) {
  const d = await call('POST', '/api/v1/destinations', { name, url, apiKey: lab.meilisearch[0].apiKey, description: desc, healthCheckInterval: interval, gzip: true, tags: name === 'Staging' ? ['staging'] : ['prod'] });
  dest[name] = d.id;
}

step('connectors for every source');
const conn = {};
const connectors = [
  ['shop-db (PostgreSQL)', 'postgres', ['prod', 'eu']],
  ['legacy-erp (MySQL 8)', 'mysql', ['erp']],
  ['warehouse (MariaDB)', 'mariadb', ['erp']],
  ['catalog-cluster (MongoDB)', 'mongodb', ['prod']],
  ['marketplace (DynamoDB)', 'dynamodb', ['aws']],
  ['mobile-sync (CouchDB)', 'couchdb', ['mobile']],
  ['finance (SQL Server)', 'sqlserver', ['finance']],
  ['global-inventory (CockroachDB)', 'cockroachdb', ['global']],
  ['debezium (Kafka)', 'kafka', ['events']],
  ['price-updates (Redis Streams)', 'redis', ['realtime']],
  ['reporting-db (SQL polling)', 'sqlpoll', ['reporting']],
];
for (const [name, key, tags] of connectors) {
  const s = src[key];
  const c = await call('POST', '/api/v1/connectors', { name, type: s.type, config: s.connector, tags, description: `Production ${s.type} source` });
  conn[key] = c.id;
}
const webhookConn = await call('POST', '/api/v1/connectors', { name: 'storefront events (Webhook)', type: 'webhook', config: {}, tags: ['events'] });

step('tasks');
const settings = (extra = {}) => ({ searchableAttributes: ['name', 'brand', 'category'], filterableAttributes: ['category', 'brand', 'active'],
  sortableAttributes: ['price', 'rating', 'updated_at'], ...extra });
const tasks = [
  { name: 'Product catalog', key: 'postgres', table: 'products', uid: 'products', d: 'Production (EU)', tags: ['storefront'], settings: settings({ synonyms: { lamp: ['light'], light: ['lamp'] } }) },
  { name: 'Customers', key: 'postgres', table: 'customers', uid: 'customers', d: 'Production (EU)', tags: ['crm'],
    settings: { searchableAttributes: ['name', 'email', 'company', 'city'], filterableAttributes: ['country', 'tier'], sortableAttributes: ['lifetime_value', 'created_at'] } },
  { name: 'Orders', key: 'postgres', table: 'orders', uid: 'orders', d: 'Production (EU)', tags: ['commerce'],
    settings: { searchableAttributes: ['status', 'channel'], filterableAttributes: ['status', 'currency', 'channel', 'customer_id'], sortableAttributes: ['total', 'placed_at'] } },
  { name: 'Catalog mirror (US)', key: 'postgres', table: 'products', uid: 'products', d: 'Search replica (US)', tags: ['storefront'], settings: settings() },
  { name: 'ERP inventory', key: 'mysql', table: 'products', uid: 'erp_inventory', d: 'Production (EU)', tags: ['erp'], settings: settings() },
  { name: 'Warehouse stock', key: 'mariadb', table: 'products', uid: 'warehouse_stock', d: 'Search replica (US)', tags: ['erp'] },
  { name: 'Product reviews', key: 'mongodb', table: 'products', uid: 'reviews', d: 'Production (EU)', tags: ['ugc'] },
  { name: 'Marketplace listings', key: 'dynamodb', table: 'products', uid: 'listings', d: 'Search replica (US)', tags: ['marketplace'] },
  { name: 'Mobile offline store', key: 'couchdb', table: 'products', uid: 'mobile_store', d: 'Production (EU)', tags: ['mobile'] },
  { name: 'Finance ledger', key: 'sqlserver', table: 'products', uid: 'ledger', d: 'Search replica (US)', tags: ['finance'] },
  { name: 'Global inventory', key: 'cockroachdb', table: 'products', uid: 'global_inventory', d: 'Production (EU)', tags: ['global'] },
  { name: 'Inventory events', key: 'kafka', table: src.kafka.table, uid: 'inventory_events', d: 'Search replica (US)', tags: ['events'] },
  { name: 'Live prices', key: 'redis', table: 'products', uid: 'live_prices', d: 'Production (EU)', tags: ['realtime'] },
  { name: 'Reporting catalog', key: 'sqlpoll', table: 'catalog', uid: 'reporting_catalog', d: 'Search replica (US)', tags: ['reporting'],
    extraSource: { cursorColumn: 'updated_at', pollInterval: '1s' }, mapping: { softDeleteColumn: 'deleted', softDeleteValue: 'true' } },
];
const ids = {};
for (const t of tasks) {
  const body = {
    name: t.name, tags: t.tags, description: `${t.name} → ${t.uid}`,
    source: { connectorId: conn[t.key], table: t.table, ...(t.extraSource || {}) },
    index: { uid: t.uid, destinationId: dest[t.d], manageSettings: !!t.settings, ...(t.settings ? { settings: t.settings } : {}) },
    mapping: t.mapping || {}, sync: { flushInterval: '300ms' },
  };
  const v = await call('POST', '/api/v1/tasks?start=true', body);
  ids[t.name] = v.id;
}
await call('POST', '/api/v1/tasks?start=true', {
  name: 'Storefront events', tags: ['events'], source: { connectorId: webhookConn.id, table: 'storefront' },
  index: { uid: 'storefront_events', destinationId: dest['Production (EU)'] }, mapping: { primaryKey: ['id'] },
});

step('email (Mailpit) and alert rules');
await call('PUT', '/api/v1/settings/smtp', { host: 'localhost', port: 11025, tls: 'none', from: 'Meili CDC <alerts@acme.io>', publicUrl: 'https://cdc.acme.io' });
const rules = [
  { name: 'Pipeline failed or held', severity: 'critical', condition: { type: 'task_state', states: ['failed', 'held'] } },
  { name: 'Replication lag above 30s', severity: 'warning', condition: { type: 'lag', thresholdMs: 30000, for: '1m' } },
  { name: 'Meilisearch unreachable', severity: 'critical', condition: { type: 'meili_down', for: '0s' } },
  { name: 'Rejected documents', severity: 'warning', condition: { type: 'dlq', count: 1, window: '10m' } },
];
for (const r of rules) {
  await call('POST', '/api/v1/alerts/rules', { ...r, enabled: true, notifyResolved: true, cooldown: '30m',
    channels: { emails: ['oncall@acme.io', 'platform@acme.io'], webhookUrl: 'https://hooks.acme.io/cdc-alerts' } });
}

step('team');
for (const [email, name, role] of [['sam@acme.io', 'Sam Lee', 'admin'], ['jordan@acme.io', 'Jordan Patel', 'moderator'],
  ['taylor@acme.io', 'Taylor Kim', 'moderator'], ['riley@acme.io', 'Riley Chen', 'viewer'], ['casey@acme.io', 'Casey Novak', 'viewer']]) {
  await call('POST', '/api/v1/users', { email, name, role, password: MEMBER_PASSWORD });
}
await call('POST', '/api/v1/invitations', { email: 'morgan@acme.io', role: 'viewer' });

step('MCP');
const mcp = await call('GET', '/api/v1/settings/mcp');
await call('PUT', '/api/v1/settings/mcp', { enabled: true, policy: mcp.policy });
for (const [name, role] of [['Claude Desktop (on-call)', 'moderator'], ['Analytics agent', 'viewer'], ['Release bot', 'admin']]) {
  await call('POST', '/api/v1/mcp/tokens', { name, role, expiresIn: '2160h' });
}

console.log(JSON.stringify({ destinations: dest, tasks: ids }, null, 2));
