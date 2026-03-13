// Mock meilisearch-cdc API for developing and testing the dashboard without
// the Go backend. No dependencies: `node test/ui/mock-server.mjs`.
//
// Implements docs/api.md with in-memory fake data. Extra endpoints (not part
// of the real API, used only by the UI tests):
//   POST /__mock/reset[?empty=1]  restore the initial data set (empty=1: no users, setup required)
//   POST /__mock/login?role=admin log in as the seeded user with that role (sets the session cookie)
//   POST /__mock/smtp {"configured"} quickly enable/disable SMTP
//   POST /__mock/alert {"ruleId","taskId","state"} fire or resolve an alert (SSE "alert" event)
//   GET  /__mock/outbox           emails "sent" by the mock (with reset/invite links)
// Seeded users (password "password123"): admin@example.com (the primary administrator), mod@example.com, viewer@example.com.
// The bearer API token is MOCK_ADMIN_TOKEN (default "mock-api-token").
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createAccounts, ROLE_RANK } from './mock-accounts.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const WEB_ROOT = path.resolve(here, '../../web');
const PORT = Number(process.env.PORT || 8790);
const adminToken = process.env.MOCK_ADMIN_TOKEN || 'mock-api-token';

// ---------------------------------------------------------------- helpers
let seed = 42;
const rnd = () => ((seed = (seed * 1664525 + 1013904223) % 4294967296) / 4294967296);
const pick = (arr) => arr[Math.floor(rnd() * arr.length)];
const clamp = (v, a, b) => Math.max(a, Math.min(b, v));
const iso = (ms = Date.now()) => new Date(ms).toISOString();
const ZERO_TIME = '0001-01-01T00:00:00Z';
const MASK = '••••••';
const clone = (v) => JSON.parse(JSON.stringify(v));

function redactDSN(dsn) {
  if (!dsn) return '';
  let out = dsn.replace(/^([a-zA-Z0-9+.-]+:\/\/[^:/@]+:)([^@]*)(@)/, `$1${MASK}$3`);
  if (out !== dsn) return out;
  out = dsn.replace(/^([^:/@]+:)([^@]*)(@tcp\(|@unix\(|@\()/, `$1${MASK}$3`);
  if (out !== dsn) return out;
  return dsn.replace(/(password\s*=\s*)('[^']*'|\S+)/i, `$1${MASK}`);
}

// ---------------------------------------------------------------- schemas
const SCHEMAS = {
  products: [
    ['id', 'integer', false, true], ['sku', 'text'], ['name', 'text'], ['description', 'text', true],
    ['price', 'numeric'], ['category', 'text'], ['brand', 'text', true], ['in_stock', 'boolean'],
    ['stock', 'integer'], ['rating', 'double precision', true], ['tags', 'text[]', true],
    ['lat', 'double precision', true], ['lng', 'double precision', true],
    ['updated_at', 'timestamptz'], ['deleted_at', 'timestamptz', true],
  ],
  orders: [
    ['id', 'bigint', false, true], ['customer_id', 'bigint'], ['status', 'varchar(32)'], ['total', 'decimal(12,2)'],
    ['currency', 'char(3)'], ['items', 'json', true], ['created_at', 'datetime'], ['updated_at', 'datetime'],
  ],
  users: [
    ['_id', 'objectId', false, true], ['email', 'string'], ['name', 'string'], ['country', 'string', true],
    ['plan', 'string'], ['active', 'bool'], ['signup_at', 'date'],
  ],
  'dbserver1.inventory.events': [
    ['id', 'int64', false, true], ['type', 'string'], ['payload', 'struct', true], ['occurred_at', 'int64'],
  ],
  inventory: [
    ['pk', 'S', false, true], ['sk', 'S', false, true], ['warehouse', 'S'], ['qty', 'N'], ['updated_at', 'S'],
  ],
  legacy_items: [
    ['id', 'INTEGER', false, true], ['title', 'TEXT'], ['body', 'TEXT', true], ['modified_seq', 'INTEGER'],
  ],
  articles: [
    ['id', 'int', false, true], ['title', 'varchar(255)'], ['author', 'varchar(128)'], ['body', 'longtext', true],
    ['published', 'tinyint(1)'], ['published_at', 'datetime', true],
  ],
};
function schemaFor(table) {
  const cols = SCHEMAS[table] || [
    ['id', 'integer', false, true], ['name', 'text'], ['status', 'text', true], ['created_at', 'timestamptz'], ['updated_at', 'timestamptz'],
  ];
  return {
    columns: cols.map(([name, type, nullable = false, pk = false]) => ({ name, type, nullable, primaryKey: pk })),
    primaryKey: cols.filter((c) => c[3]).map((c) => c[0]),
    inferred: ['users', 'dbserver1.inventory.events', 'inventory'].includes(table),
    rowCount: { products: 1248320, orders: 382114, users: 96211, articles: 4120, legacy_items: 18822 }[table] ?? -1,
  };
}

const WORDS = ['aurora', 'basil', 'cobalt', 'delta', 'ember', 'fjord', 'granite', 'harbor', 'indigo', 'juniper', 'kelp', 'lumen', 'meadow', 'nimbus', 'onyx', 'pebble', 'quartz', 'rowan', 'sierra', 'tundra'];
const CATS = ['Audio', 'Books', 'Cameras', 'Garden', 'Kitchen', 'Outdoor', 'Sports', 'Toys'];
const BRANDS = ['Acme', 'Northwind', 'Globex', 'Initech', 'Umbrella', 'Hooli'];
function hash(i, salt = 0) { let x = (i + 1) * 2654435761 + salt * 97; x ^= x >>> 13; x = Math.imul(x, 1274126177); return (x >>> 0) / 4294967296; }
function fakeValue(col, i) {
  const h = hash(i, col.name.length);
  const n = col.name;
  if (col.primaryKey && /int|N|S|objectId/i.test(col.type)) return col.type === 'objectId' ? `65f${(100000 + i).toString(16)}a1b2c3d4e5f6` : col.type === 'S' ? `ITEM#${1000 + i}` : i + 1;
  if (n === 'email') return `${WORDS[i % 20]}.${WORDS[(i * 7) % 20]}@example.com`;
  if (n === 'name' || n === 'title') return `${WORDS[i % 20][0].toUpperCase()}${WORDS[i % 20].slice(1)} ${WORDS[(i * 3 + 5) % 20]} ${n === 'title' ? 'notes' : CATS[i % 8].toLowerCase()}`;
  if (n === 'description' || n === 'body') return h < 0.04 ? null : `A ${WORDS[(i * 5) % 20]} ${WORDS[(i * 11) % 20]} crafted for everyday ${WORDS[(i * 13) % 20]} use.`;
  if (n === 'category') return CATS[i % CATS.length];
  if (n === 'brand') return h < 0.08 ? null : BRANDS[i % BRANDS.length];
  if (n === 'sku') return `SKU-${(10000 + i * 7).toString(36).toUpperCase()}`;
  if (n === 'price' || n === 'total') return Math.round((5 + h * 495) * 100) / 100;
  if (n === 'rating') return Math.round((2.5 + h * 2.5) * 10) / 10;
  if (/bool|tinyint/.test(col.type)) return h > 0.25;
  if (n === 'tags') return [WORDS[i % 20], WORDS[(i + 4) % 20]];
  if (n === 'lat') return Math.round((48.85 + (h - 0.5) * 2) * 1e5) / 1e5;
  if (n === 'lng') return Math.round((2.35 + (hash(i, 3) - 0.5) * 2) * 1e5) / 1e5;
  if (n === 'deleted_at' || n === 'published_at') return h < 0.9 ? null : iso(Date.UTC(2025, 0, 1) + i * 3600e3);
  if (/_at$/.test(n)) return iso(Date.UTC(2025, 5, 1) + i * 3600e3);
  if (n === 'status') return pick2(['pending', 'paid', 'shipped', 'refunded'], i);
  if (n === 'currency') return pick2(['EUR', 'USD', 'GBP'], i);
  if (n === 'country') return pick2(['FR', 'DE', 'US', 'IR', 'JP', 'BR'], i);
  if (n === 'plan') return pick2(['free', 'pro', 'team'], i);
  if (n === 'warehouse') return pick2(['ams-1', 'fra-2', 'iad-1'], i);
  if (/int|N|numeric|decimal/i.test(col.type)) return Math.floor(h * 500);
  if (/json|struct/.test(col.type)) return { v: 1, ref: WORDS[i % 20] };
  return `${WORDS[(i * 7) % 20]}-${i}`;
}
function pick2(arr, i) { return arr[i % arr.length]; }

function transformRow(task, row) {
  const m = task.mapping || {};
  const pkCols = m.primaryKey?.length ? m.primaryKey : schemaFor(task.source.table).primaryKey;
  const pkField = m.primaryKeyField || 'id';
  const doc = {};
  doc[pkField] = pkCols.map((c) => row[c]).join('-');
  for (const [k, v] of Object.entries(row)) {
    if (m.include?.length && !m.include.includes(k)) continue;
    if (m.exclude?.includes(k)) continue;
    if (pkCols.includes(k) && !m.keepKeyColumns && k !== pkField) continue;
    const name = m.rename?.[k] || k;
    if (name === pkField) continue;
    doc[name] = v;
  }
  if (m.geo?.lat && m.geo?.lng && row[m.geo.lat] != null) {
    doc._geo = { lat: row[m.geo.lat], lng: row[m.geo.lng] };
    delete doc[m.rename?.[m.geo.lat] || m.geo.lat];
    delete doc[m.rename?.[m.geo.lng] || m.geo.lng];
  }
  if (m.timeFormat === 'unix' || m.timeFormat === 'unix_ms') {
    for (const [k, v] of Object.entries(doc)) {
      if (typeof v === 'string' && /^\d{4}-\d\d-\d\dT/.test(v)) doc[k] = m.timeFormat === 'unix' ? Math.floor(Date.parse(v) / 1000) : Date.parse(v);
    }
  }
  Object.assign(doc, m.static || {});
  return doc;
}

// ---------------------------------------------------------------- state
const MEILI_DEFAULT_SETTINGS = {
  displayedAttributes: ['*'], searchableAttributes: ['*'], filterableAttributes: [], sortableAttributes: [],
  rankingRules: ['words', 'typo', 'proximity', 'attribute', 'sort', 'exactness'], stopWords: [], nonSeparatorTokens: [],
  separatorTokens: [], dictionary: [], synonyms: {}, distinctAttribute: null, proximityPrecision: 'byWord',
  typoTolerance: { enabled: true, minWordSizeForTypos: { oneTypo: 5, twoTypos: 9 }, disableOnWords: [], disableOnAttributes: [] },
  faceting: { maxValuesPerFacet: 100, sortFacetValuesBy: { '*': 'alpha' } }, pagination: { maxTotalHits: 1000 },
  embedders: {}, searchCutoffMs: null, localizedAttributes: null, facetSearch: true, prefixSearch: 'indexingTime',
};

let S; // whole mutable state
function baseTask(id, name, source, index, extra = {}) {
  const t = {
    id, name, description: extra.description || '', tags: extra.tags || [],
    source, index: { uid: index, manageSettings: !!extra.settings, writeMode: 'replace', ...(extra.settings ? { settings: extra.settings } : {}) },
    mapping: { primaryKeyField: 'id', keepKeyColumns: false, timeFormat: 'rfc3339', ...(extra.mapping || {}) },
    sync: {
      mode: extra.mode || 'snapshot_stream', batchSize: 1000, flushInterval: '1s', maxInflight: 4,
      retry: { maxAttempts: 8, initialBackoff: '200ms', maxBackoff: '30s' },
      safety: { maxDeleteRatio: 0.2, minDocsForDeleteGuard: 1000, allowTruncate: false, zeroDowntimeResync: true, dumpBeforeResync: false },
    },
    desiredState: extra.desired || 'running',
    createdAt: iso(Date.now() - 86400e3 * (3 + id.length)), updatedAt: iso(Date.now() - 3600e3 * id.length),
  };
  return t;
}
function baseStatus(id, state, o = {}) {
  const now = Date.now();
  return {
    taskId: id, state, message: o.message || '', error: o.error || '',
    startedAt: o.started === false ? ZERO_TIME : iso(now - 3600e3 * 5), updatedAt: iso(now),
    lastEventAt: o.started === false ? ZERO_TIME : iso(now - 1500), lagMs: o.lagMs ?? 0, rate: o.rate ?? 0,
    checkpoint: o.checkpoint ?? '', snapshot: o.snapshot || { done: 0, total: 0 },
    counters: { upserted: 0, deleted: 0, events: 0, batches: 0, errors: 0, retries: 0, dlq: 0, coalesced: 0, ...(o.counters || {}) },
    attempt: o.attempt || 0, inflight: o.inflight || 0,
  };
}

/** Zero-configuration state: no destinations, connectors or tasks (used with /__mock/reset?empty=1). */
function emptyState() {
  initState();
  Object.assign(S, { tasks: [], statuses: {}, connectors: [], destinations: [], indexes: {}, destIndexes: {}, destMeiliTasks: {}, meiliTasks: [], dlq: {}, diffs: {}, logs: {}, metrics: {} });
}

function initState() {
  seed = 42;
  const tasks = [
    baseTask('products-pg', 'Products catalog', { type: 'postgres', dsn: 'postgres://cdc:s3cret@db.internal:5432/shop?sslmode=disable', schema: 'public', table: 'products', slot: 'meili_products', publication: 'meili_products' }, 'products', {
      description: 'Main storefront catalog, streamed from the primary Postgres cluster.', tags: ['shop', 'prod'],
      settings: { filterableAttributes: ['category', 'brand', 'in_stock'], sortableAttributes: ['price', 'rating'], searchableAttributes: ['name', 'description', 'brand'] },
      mapping: { softDeleteColumn: 'deleted_at', geo: { lat: 'lat', lng: 'lng' }, exclude: ['sku'] },
    }),
    baseTask('orders-mysql', 'Orders', { type: 'mysql', dsn: 'cdc:hunter2@tcp(mysql.internal:3306)/shop', table: 'orders', serverId: 4012 }, 'orders', { tags: ['shop', 'prod'], mapping: { timeFormat: 'unix' } }),
    baseTask('users-mongo', 'Users directory', { type: 'mongodb', dsn: 'mongodb://cdc:pw@mongo-0.internal:27017/?replicaSet=rs0', database: 'app', table: 'users' }, 'users', { tags: ['crm'], mapping: { primaryKey: ['_id'], exclude: ['email'] } }),
    baseTask('events-kafka', 'Inventory events', { type: 'kafka', brokers: ['kafka-1:9092', 'kafka-2:9092'], groupId: 'meili-cdc', table: 'dbserver1.inventory.events' }, 'events', { tags: ['analytics'], mode: 'stream' }),
    baseTask('stock-dynamo', 'Warehouse stock', { type: 'dynamodb', region: 'eu-west-1', accessKeyId: 'AKIAEXAMPLE', secretAccessKey: 'wJalrXUtnFEMI/K7MDENG', table: 'inventory' }, 'inventory', { tags: ['ops'], mapping: { primaryKey: ['pk', 'sk'] } }),
    baseTask('legacy-poll', 'Legacy items', { type: 'sqlpoll', driver: 'sqlite', dsn: 'file:/data/legacy.db', table: 'legacy_items', cursorColumn: 'modified_seq', pollInterval: '2s' }, 'legacy_items', { tags: ['legacy'], desired: 'stopped' }),
    baseTask('articles-maria', 'Blog articles', { type: 'mariadb', dsn: 'blog:pw@tcp(maria.internal:3306)/blog', table: 'articles' }, 'articles', { tags: ['content'], mapping: { softDeleteColumn: 'published', softDeleteValue: '0' } }),
  ];
  const statuses = {
    'products-pg': baseStatus('products-pg', 'streaming', { rate: 420, lagMs: 180, checkpoint: '0/1A3F2B80', snapshot: { done: 1248320, total: 1248320 }, inflight: 2, counters: { upserted: 1305871, deleted: 4212, events: 1310083, batches: 1422, coalesced: 1880, dlq: 4, errors: 4 } }),
    'orders-mysql': baseStatus('orders-mysql', 'snapshotting', { rate: 2400, lagMs: 0, checkpoint: 'mysql-bin.000042:1820', snapshot: { done: 141000, total: 382114 }, inflight: 4, counters: { upserted: 141000, events: 141000, batches: 141 } }),
    'users-mongo': baseStatus('users-mongo', 'paused', { message: 'paused by operator', checkpoint: '826553A1000000012B022C0100296E5A1004', snapshot: { done: 96211, total: 96211 }, counters: { upserted: 99830, deleted: 211, events: 100041, batches: 140 } }),
    'events-kafka': baseStatus('events-kafka', 'retrying', { error: 'kafka: client has run out of available brokers to talk to (Is your cluster reachable?)', attempt: 3, rate: 0, lagMs: 42000, checkpoint: 'dbserver1.inventory.events/0:88213', counters: { upserted: 52210, deleted: 1022, events: 53232, batches: 600, errors: 19, retries: 31, dlq: 12 } }),
    'stock-dynamo': baseStatus('stock-dynamo', 'failed', { error: 'ResourceNotFoundException: Requested resource not found: Stream for table inventory is disabled', counters: { upserted: 8100, events: 8100, batches: 9, errors: 1, dlq: 2 } }),
    'legacy-poll': baseStatus('legacy-poll', 'stopped', { started: false, checkpoint: '18822', counters: { upserted: 18822, events: 18822, batches: 19 } }),
    'articles-maria': baseStatus('articles-maria', 'held', { message: 'safety guard: flush would delete 38% of the index (max 20%)', error: 'delete ratio 0.38 exceeds maxDeleteRatio 0.20', checkpoint: 'mariadb-bin.000007:55120', counters: { upserted: 4120, deleted: 12, events: 5700, batches: 12 } }),
  };
  const indexes = {};
  const now = Date.now();
  const mkIndex = (uid, pk, docs, settings = {}, sample = null) => {
    indexes[uid] = { uid, primaryKey: pk, createdAt: iso(now - 86400e3 * 30), updatedAt: iso(now - 60e3), numberOfDocuments: docs, isIndexing: false, settings: { ...clone(MEILI_DEFAULT_SETTINGS), ...settings }, sample, deleted: new Set() };
  };
  mkIndex('products', 'id', 1250108, { filterableAttributes: ['category', 'brand', 'in_stock'], sortableAttributes: ['price', 'rating'], searchableAttributes: ['name', 'description', 'brand'] }, 'products');
  mkIndex('orders', 'id', 141000, {}, 'orders');
  mkIndex('users', 'id', 99619, {}, 'users');
  mkIndex('events', 'id', 51188, {}, 'dbserver1.inventory.events');
  mkIndex('inventory', 'id', 8100, {}, 'inventory');
  mkIndex('legacy_items', 'id', 18822, {}, 'legacy_items');
  mkIndex('articles', 'id', 4108, { filterableAttributes: ['author'] }, 'articles');
  mkIndex('movies', 'id', 31944, { filterableAttributes: ['genres', 'year'], sortableAttributes: ['year'] }, 'movies');
  indexes.products.isIndexing = true;
  // EU replica destination holds the users index (task users-mongo writes there) and a products copy
  const euIndexes = { users: indexes.users, products: { ...clone({ ...indexes.products, deleted: [] }), deleted: new Set(), uid: 'products', numberOfDocuments: 1249002, isIndexing: false, sample: 'products' } };
  delete indexes.users;
  tasks.find((t) => t.id === 'users-mongo').index.destinationId = 'eu-replica';
  const mkDest = (id, name, url, extra = {}) => ({ id, name, description: extra.description || '', url, apiKey: 'masterKey-' + id, gzip: true, timeout: '30s', healthCheckInterval: extra.interval || '30s', isDefault: !!extra.isDefault, managedByEnv: !!extra.managedByEnv, tags: extra.tags || [], createdAt: iso(now - 50 * 86400e3), updatedAt: iso(now - 3 * 86400e3) });
  const destinations = [
    mkDest('default', 'Production', 'https://meili.prod.example.com', { isDefault: true, tags: ['prod'], description: 'Primary search cluster serving the storefront.' }),
    mkDest('eu-replica', 'EU replica', 'https://meili.eu.example.com', { interval: '5s', tags: ['prod', 'eu'], description: 'Read replica close to EU customers.' }),
    mkDest('staging', 'Staging', 'http://meili.staging.internal:7700', { interval: '15s', tags: ['staging'] }),
  ];

  const metrics = {};
  for (const t of tasks) {
    const st = statuses[t.id];
    const pts = [];
    let up = st.counters.upserted - 300 * st.rate * 2, del = st.counters.deleted;
    for (let i = 300; i > 0; i--) {
      const active = ['streaming', 'snapshotting'].includes(st.state) || (t.id === 'events-kafka' && i > 60);
      const base = t.id === 'events-kafka' ? 160 : st.rate;
      const r = active ? Math.max(0, base * (0.75 + 0.5 * Math.sin(i / 17) * 0.5 + rnd() * 0.25)) : 0;
      up += r * 2; del += active ? r * 0.01 : 0;
      pts.push({ t: now - i * 2000, rate: Math.round(r * 10) / 10, lagMs: active ? Math.round(160 + 90 * Math.sin(i / 11) + rnd() * 40) : (t.id === 'events-kafka' ? 42000 - i * 50 : 0), upserted: Math.max(0, Math.round(up)), deleted: Math.round(del) });
    }
    metrics[t.id] = pts;
  }

  const logs = {};
  const L = (id, level, message, agoS) => (logs[id] ||= []).push({ time: iso(now - agoS * 1000), taskId: id, level, message });
  L('products-pg', 'info', 'task started (mode snapshot_stream)', 18000);
  L('products-pg', 'info', 'snapshot finished: 1,248,320 rows in 6m12s', 17600);
  L('products-pg', 'info', 'streaming from LSN 0/19FF0000', 17590);
  L('products-pg', 'warn', 'meilisearch rejected 4 documents (invalid_document_geo_field), sent to DLQ', 4200);
  for (let i = 30; i > 0; i--) L('products-pg', i % 7 ? 'info' : 'debug', i % 7 ? `flushed batch of ${400 + i * 13} documents (meili task ${90210 + i})` : 'coalesced 12 updates for hot rows', i * 9);
  L('orders-mysql', 'info', 'task started (mode snapshot_stream)', 120);
  L('orders-mysql', 'info', 'snapshot started: ~382,114 rows', 118);
  L('users-mongo', 'info', 'paused by operator', 900);
  L('events-kafka', 'error', 'kafka: client has run out of available brokers to talk to', 30);
  L('events-kafka', 'warn', 'retrying in 3.2s (attempt 3/8)', 28);
  L('stock-dynamo', 'error', 'ResourceNotFoundException: Stream for table inventory is disabled', 7200);
  L('articles-maria', 'warn', 'safety guard: flush would delete 38% of the index (max 20%); task held', 2400);

  const dlq = {};
  const D = (taskId, n, err, code) => {
    dlq[taskId] ||= [];
    for (let i = 0; i < n; i++) dlq[taskId].push({ id: `${taskId}-dlq-${i + 1}`, taskId, docId: String(1000 + i * 17), op: i % 5 === 4 ? 'delete' : 'upsert', document: { id: 1000 + i * 17, name: `Item ${i}`, _geo: { lat: 'abc', lng: 2.35 } }, error: err, code, attempts: 1 + (i % 3), createdAt: iso(now - (n - i) * 600e3) });
  };
  D('products-pg', 4, 'The `_geo` field in the document with the id: `1000` contains the following unexpected fields: `lat` must be a number.', 'invalid_document_geo_field');
  D('events-kafka', 12, 'Document identifier `evt/ü` is invalid. A document identifier can be of type integer or string, only composed of alphanumeric characters (a-z A-Z 0-9), hyphens (-) and underscores (_).', 'invalid_document_id');
  D('stock-dynamo', 2, 'Payload too large: the payload exceeds 100MB.', 'payload_too_large');

  const diffs = {
    'products-pg': { taskId: 'products-pg', status: 'done', error: '', startedAt: iso(now - 3600e3), finishedAt: iso(now - 3590e3), sourceCount: 1248320, indexCount: 1248318, missing: 3, extra: 1, mismatched: 2, inSync: false,
      samples: { missing: ['10442', '88120', '120009'], extra: ['999999'], mismatched: [
        { id: '4410', fields: [{ field: 'price', source: 19.99, index: 24.99 }, { field: 'in_stock', source: false, index: true }] },
        { id: '7781', fields: [{ field: 'name', source: 'Cobalt kettle 1.7L', index: 'Cobalt kettle' }] },
      ] } },
    'legacy-poll': { taskId: 'legacy-poll', status: 'done', error: '', startedAt: iso(now - 86400e3), finishedAt: iso(now - 86390e3), sourceCount: 18822, indexCount: 18822, missing: 0, extra: 0, mismatched: 0, inSync: true, samples: { missing: [], extra: [], mismatched: [] } },
  };

  let uid = 0;
  const meiliTasks = [];
  const types = ['documentAdditionOrUpdate', 'documentAdditionOrUpdate', 'documentAdditionOrUpdate', 'documentDeletion', 'settingsUpdate', 'indexCreation'];
  const idxs = ['products', 'orders', 'users', 'events', 'inventory', 'movies'];
  for (let i = 0; i < 90; i++) {
    const type = pick(types); const indexUid = pick(idxs);
    const enq = now - (90 - i) * 45e3;
    const status = i > 86 ? 'enqueued' : i === 86 ? 'processing' : rnd() < 0.06 ? 'failed' : 'succeeded';
    meiliTasks.push(mkMeiliTask(uid++, indexUid, type, status, enq, indexUid !== 'movies' && type.startsWith('document') ? `cdc:${tasks.find((t) => t.index.uid === indexUid)?.id || indexUid}` : null));
  }
  const C = (id, name, type, config, tags, description = '') => ({ id, name, description, type, tags, config, createdAt: iso(now - 30 * 86400e3), updatedAt: iso(now - 4 * 86400e3), lastTest: null });
  const connectors = [
    C('c_shop_pg', 'Shop Postgres (primary)', 'postgres', { dsn: 'postgres://cdc:s3cret@db.internal:5432/shop?sslmode=disable' }, ['shop', 'prod'], 'Primary storefront cluster, logical replication enabled.'),
    C('c_shop_mysql', 'Shop MySQL', 'mysql', { dsn: 'cdc:hunter2@tcp(mysql.internal:3306)/shop' }, ['shop']),
    C('c_kafka', 'Analytics Kafka', 'kafka', { brokers: ['kafka-1:9092', 'kafka-2:9092'] }, ['analytics'], 'Debezium topics from the inventory database.'),
  ];
  connectors.push(
    C('c_app_mongo', 'App MongoDB', 'mongodb', { dsn: 'mongodb://cdc:pw@mongo-0.internal:27017/?replicaSet=rs0', database: 'app' }, ['crm']),
    C('c_inventory_kafka', 'Inventory Debezium', 'kafka', { brokers: ['kafka-1:9092', 'kafka-2:9092'] }, ['analytics']),
    C('c_aws_dynamo', 'AWS eu-west-1', 'dynamodb', { region: 'eu-west-1', accessKeyId: 'AKIAEXAMPLE', secretAccessKey: 'wJalrXUtnFEMI/K7MDENG' }, ['ops']),
    C('c_legacy_sqlite', 'Legacy SQLite', 'sqlpoll', { driver: 'sqlite', dsn: 'file:/data/legacy.db' }, ['legacy']),
    C('c_blog_maria', 'Blog MariaDB', 'mariadb', { dsn: 'blog:pw@tcp(maria.internal:3306)/blog' }, ['content']),
  );
  const byTask = { 'products-pg': 'c_shop_pg', 'orders-mysql': 'c_shop_mysql', 'users-mongo': 'c_app_mongo', 'events-kafka': 'c_inventory_kafka', 'stock-dynamo': 'c_aws_dynamo', 'legacy-poll': 'c_legacy_sqlite', 'articles-maria': 'c_blog_maria' };
  for (const t of tasks) t.source.connectorId = byTask[t.id];
  S = { tasks, statuses, indexes, metrics, logs, dlq, diffs, meiliTasks, nextMeiliUid: uid, tick: 0, connectors, destinations, single: false,
    destIndexes: { default: indexes, 'eu-replica': euIndexes, staging: {} }, destMeiliTasks: { default: meiliTasks, 'eu-replica': meiliTasks.filter((m) => ['users', 'products'].includes(m.indexUid)).slice(-12), staging: [] },
    experimental: { metrics: false, logsRoute: false, editDocumentsByFunction: false, containsFilter: true, network: false, getTaskDocumentsRoute: false, compositeEmbedders: false, chatCompletions: false, multimodal: false, vectorStoreSetting: false } };
}
function mkMeiliTask(uid, indexUid, type, status, enqMs = Date.now(), customMetadata = null) {
  const dur = 12 + Math.floor(rnd() * 900);
  const done = status === 'succeeded' || status === 'failed' || status === 'canceled';
  const details = type === 'documentAdditionOrUpdate' ? { receivedDocuments: 1000, indexedDocuments: status === 'succeeded' ? 1000 : null }
    : type === 'documentDeletion' ? { providedIds: 12, deletedDocuments: status === 'succeeded' ? 12 : null }
      : type === 'settingsUpdate' ? { filterableAttributes: ['category'] } : type === 'indexCreation' ? { primaryKey: 'id' } : {};
  return {
    uid, batchUid: done ? uid : null, indexUid, status, type, canceledBy: null, details,
    error: status === 'failed' ? { message: 'Index `' + indexUid + '`: The `_geo` field in the document with the id: `4410` is not an object.', code: 'invalid_document_geo_field', type: 'invalid_request', link: 'https://docs.meilisearch.com/errors#invalid_document_geo_field' } : null,
    duration: done ? `PT${(dur / 1000).toFixed(3)}S` : null, enqueuedAt: iso(enqMs),
    startedAt: status === 'enqueued' ? null : iso(enqMs + 40), finishedAt: done ? iso(enqMs + 40 + dur) : null,
    customMetadata,
  };
}
initState();

// ---------------------------------------------------------------- views
const defaultDest = () => S.destinations.find((d) => d.isDefault) || null;
const destOf = (t) => (t.index && t.index.destinationId) || (defaultDest() ? defaultDest().id : '');
const view = (t) => { const v = { ...redactTask(t), status: S.statuses[t.id] }; v.index.destinationId = destOf(t); return v; };
const destHealthy = (d) => !/staging|fail/.test(d.url);
const durMs = (v) => {
  if (!v) return 30000;
  let ms = 0; const re = /(\d+(?:\.\d+)?)(ms|s|m|h)/g; let m;
  while ((m = re.exec(v))) ms += Number(m[1]) * { ms: 1, s: 1000, m: 60000, h: 3600000 }[m[2]];
  return ms || 30000;
};
/** Background health checker state per destination (consecutive failures, last healthy, schedule). */
function hstate(d) {
  S.hstate ||= {};
  if (!S.hstate[d.id]) {
    const now = Date.now(); const ok = destHealthy(d);
    S.hstate[d.id] = { checkedAt: now - 4000, nextCheckAt: now - 4000 + durMs(d.healthCheckInterval), consecutiveFailures: ok ? 0 : 7, lastHealthyAt: ok ? now - 4000 : now - 3 * 3600e3, latencyMs: d.id === 'eu-replica' ? 41 : 6 };
  }
  return S.hstate[d.id];
}
function runCheck(d) {
  const hs = hstate(d); const now = Date.now(); const ok = destHealthy(d);
  Object.assign(hs, { checkedAt: now, nextCheckAt: now + durMs(d.healthCheckInterval), consecutiveFailures: ok ? 0 : hs.consecutiveFailures + 1, lastHealthyAt: ok ? now : hs.lastHealthyAt,
    latencyMs: ok ? Math.round((d.id === 'eu-replica' ? 35 : 4) + rnd() * 12) : 0 });
  return destHealth(d);
}
function destHealth(d) {
  const hs = hstate(d);
  const common = { checkedAt: iso(hs.checkedAt), nextCheckAt: iso(hs.nextCheckAt), consecutiveFailures: hs.consecutiveFailures, ...(hs.lastHealthyAt ? { lastHealthyAt: iso(hs.lastHealthyAt) } : {}) };
  if (!destHealthy(d)) return { healthy: false, version: '', indexes: 0, documents: 0, databaseSize: 0, error: `dial tcp ${d.url.replace(/^https?:\/\//, '')}: connect: connection refused`, latencyMs: 0, ...common };
  const ix = Object.values(S.destIndexes[d.id] || {});
  return { healthy: true, version: d.id === 'eu-replica' ? '1.21.0' : '1.22.1', indexes: ix.length, documents: ix.reduce((a, i) => a + i.numberOfDocuments, 0), databaseSize: d.id === 'default' ? 5_812_420_608 : 812_004_352, error: '', latencyMs: hs.latencyMs, ...common };
}
function destView(d) {
  const usedBy = S.tasks.filter((t) => destOf(t) === d.id).map((t) => {
    const st = S.statuses[t.id] || {};
    return { id: t.id, name: t.name, state: st.state || 'stopped', indexUid: t.index.uid, lagMs: st.lagMs || 0, rate: st.rate || 0, lastEventAt: st.lastEventAt || ZERO_TIME, lastSyncAt: st.lastEventAt || ZERO_TIME, error: st.error || '' };
  });
  const times = usedBy.map((u) => u.lastSyncAt).filter((x) => x && !x.startsWith('0001')).sort();
  const sync = { tasks: usedBy.length, running: usedBy.filter((u) => ACTIVE.has(u.state)).length, failing: usedBy.filter((u) => ['failed', 'held', 'retrying'].includes(u.state)).length,
    lastSyncAt: times.length ? times[times.length - 1] : ZERO_TIME, maxLagMs: Math.max(0, ...usedBy.filter((u) => ACTIVE.has(u.state)).map((u) => u.lagMs)), rate: Math.round(usedBy.reduce((a, u) => a + u.rate, 0) * 10) / 10 };
  return { ...d, apiKey: d.apiKey ? MASK : '', usedBy, sync, health: destHealth(d) };
}
function testDestination(d) {
  if (!d.url || !/^https?:\/\/\S+$/.test(d.url)) return { ok: false, error: 'url must be an http(s) URL', version: '', latencyMs: 0, indexes: 0 };
  if (!destHealthy(d)) return { ok: false, error: `dial tcp ${d.url.replace(/^https?:\/\//, '')}: connect: connection refused`, version: '', latencyMs: 0, indexes: 0 };
  if (d.apiKey === 'wrong') return { ok: false, error: 'The provided API key is invalid.', version: '', latencyMs: 12, indexes: 0 };
  return { ok: true, error: '', version: '1.22.1', latencyMs: 9 + Math.round(rnd() * 20), indexes: Object.keys(S.destIndexes[d.id] || {}).length };
}
async function destinationsApi(req, res, parts, M) {
  const [, b, c] = parts;
  if (!b && M === 'GET') return send(res, 200, S.destinations.map(destView));
  if (b === 'test' && M === 'POST') {
    const x = await readBody(req) || {};
    const prev = x.id && S.destinations.find((d) => d.id === x.id);
    await new Promise((r) => setTimeout(r, 250));
    return send(res, 200, testDestination({ ...x, apiKey: x.apiKey === MASK && prev ? prev.apiKey : x.apiKey, id: x.id || '' }));
  }
  if (!b && M === 'POST') {
    if (S.single) return err(res, 409, 'pinned to a single destination by MEILI_URL');
    const x = await readBody(req) || {};
    const e = [];
    if (!String(x.name || '').trim()) e.push('name is required');
    if (!/^https?:\/\/\S+$/.test(x.url || '')) e.push('url must be an http(s) URL');
    if (e.length) return err(res, 400, 'invalid destination', e);
    const id = `${String(x.name).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 30) || 'meili'}-${Math.random().toString(36).slice(2, 5)}`;
    if (x.healthCheckInterval && (durMs(x.healthCheckInterval) < 5000 || durMs(x.healthCheckInterval) > 3600000)) return err(res, 400, 'invalid destination', ['healthCheckInterval must be between 5s and 1h']);
    const d = { id, name: x.name.trim(), description: x.description || '', url: x.url.trim(), apiKey: x.apiKey || '', gzip: x.gzip !== false, timeout: x.timeout || '30s', healthCheckInterval: x.healthCheckInterval || '30s', isDefault: !S.destinations.length, managedByEnv: false, tags: x.tags || [], createdAt: iso(), updatedAt: iso() };
    S.destinations.push(d); S.destIndexes[id] = {}; S.destMeiliTasks[id] = [];
    return send(res, 201, destView(d));
  }
  const d = S.destinations.find((y) => y.id === b);
  if (!d) return err(res, 404, `destination ${b} not found`);
  if (!c && M === 'GET') return send(res, 200, destView(d));
  if (!c && M === 'PUT') {
    const x = await readBody(req) || {};
    if (d.managedByEnv && ((x.url && x.url !== d.url) || (x.apiKey && x.apiKey !== MASK))) return err(res, 409, 'url and api key are managed by MEILI_URL / MEILI_API_KEY');
    if (!String(x.name || '').trim()) return err(res, 400, 'invalid destination', ['name is required']);
    if (x.healthCheckInterval && (durMs(x.healthCheckInterval) < 5000 || durMs(x.healthCheckInterval) > 3600000)) return err(res, 400, 'invalid destination', ['healthCheckInterval must be between 5s and 1h']);
    if (x.healthCheckInterval) { d.healthCheckInterval = x.healthCheckInterval; hstate(d).nextCheckAt = hstate(d).checkedAt + durMs(x.healthCheckInterval); }
    Object.assign(d, { name: x.name.trim(), description: x.description || '', url: d.managedByEnv ? d.url : (x.url || d.url), apiKey: x.apiKey === MASK || x.apiKey === undefined ? d.apiKey : x.apiKey, gzip: x.gzip !== false, timeout: x.timeout || d.timeout, tags: x.tags || [], updatedAt: iso() });
    return send(res, 200, destView(d));
  }
  if (!c && M === 'DELETE') {
    if (d.isDefault) return send(res, 409, { error: 'the default destination cannot be deleted', usedBy: [] });
    const used = destView(d).usedBy;
    if (used.length) return send(res, 409, { error: `destination is used by ${used.length} task${used.length > 1 ? 's' : ''}`, usedBy: used });
    S.destinations = S.destinations.filter((y) => y !== d);
    return send(res, 204);
  }
  if (c === 'default' && M === 'POST') {
    if (S.single) return err(res, 409, 'pinned to a single destination by MEILI_URL');
    for (const y of S.destinations) y.isDefault = y === d;
    return send(res, 200, destView(d));
  }
  if (c === 'test' && M === 'POST') { await new Promise((r) => setTimeout(r, 250)); return send(res, 200, testDestination(d)); }
  if (c === 'health' && parts[3] === 'check' && M === 'POST') {
    await new Promise((r) => setTimeout(r, 150));
    const h = runCheck(d);
    broadcast('destination.health', { id: d.id, health: h });
    return send(res, 200, h);
  }
  return err(res, 404, 'not found');
}
const SETTING_KEYS = new Set(Object.keys(MEILI_DEFAULT_SETTINGS));
const badSettingsKey = (st) => st && typeof st === 'object' ? Object.keys(st).find((k) => !SETTING_KEYS.has(k)) : null;
function taskSettings(t) {
  const ix = (S.destIndexes[destOf(t)] || {})[t.index.uid];
  const desired = t.index.settings || {};
  const live = ix ? ix.settings : null;
  const changes = Object.entries(desired).filter(([k, v]) => JSON.stringify(live ? live[k] ?? null : null) !== JSON.stringify(v)).map(([key, v]) => ({ key, current: live ? live[key] ?? null : null, desired: v }));
  return { indexUid: t.index.uid, manageSettings: !!t.index.manageSettings, desired, live, indexExists: !!ix, changes, inSync: !!ix && !changes.length };
}
function redactTask(t) {
  const c = clone(t);
  c.source.dsn = redactDSN(c.source.dsn);
  if (c.source.secretAccessKey) c.source.secretAccessKey = MASK;
  if (!c.source.dsn) delete c.source.dsn;
  return c;
}
const ACTIVE = new Set(['starting', 'snapshotting', 'streaming', 'resyncing', 'retrying']);
function overview() {
  const byState = {};
  const totals = { upserted: 0, deleted: 0, events: 0, batches: 0, errors: 0, retries: 0, dlq: 0, coalesced: 0 };
  let rate = 0;
  for (const t of S.tasks) {
    const st = S.statuses[t.id];
    byState[st.state] = (byState[st.state] || 0) + 1;
    for (const k of Object.keys(totals)) totals[k] += st.counters[k] || 0;
    rate += st.rate;
  }
  totals.dlq = Object.values(S.dlq).reduce((a, l) => a + l.length, 0);
  const idx = Object.values(S.indexes);
  return {
    tasks: { total: S.tasks.length, byState }, totals, rate: Math.round(rate * 10) / 10,
    meilisearch: defaultDest() ? { healthy: destHealthy(defaultDest()), version: '1.22.1', indexes: idx.length, documents: idx.reduce((a, i) => a + i.numberOfDocuments, 0), databaseSize: 5_812_420_608 } : { healthy: false, version: '', indexes: 0, documents: 0, databaseSize: 0 },
    destinations: S.destinations.map((d) => { const h = destHealth(d); return { id: d.id, name: d.name, isDefault: d.isDefault, healthy: h.healthy, version: h.version, indexes: h.indexes, documents: h.documents, error: h.error }; }),
    sources: ['postgres', 'mysql', 'mariadb', 'mongodb', 'dynamodb', 'kafka', 'sqlpoll'],
  };
}

// ---------------------------------------------------------------- connectors
const CONN_FIELDS = {
  postgres: ['dsn'], mysql: ['dsn', 'database'], mariadb: ['dsn', 'database'], mongodb: ['dsn', 'database'], dynamodb: ['region', 'endpoint', 'accessKeyId', 'secretAccessKey'],
  kafka: ['brokers'], sqlpoll: ['driver', 'dsn'], couchdb: ['dsn'], sqlserver: ['dsn'], cockroachdb: ['dsn'], redis: ['dsn'], webhook: [],
};
const TABLES = {
  postgres: [['products', 'public', 1248320], ['product_variants', 'public', 3812004], ['categories', 'public', 214], ['brands', 'public', 96], ['orders', 'public', 382114], ['customers', 'public', 96211], ['reviews', 'public', 912442], ['inventory_view', 'public', -1, 'view'], ['audit_log', 'ops', 9120044]],
  mysql: [['orders', 'shop', 382114], ['order_items', 'shop', 1192004], ['customers', 'shop', 96211], ['payments', 'shop', 380022], ['coupons', 'shop', 1200]],
  mongodb: [['users', 'app', 96211, 'collection'], ['sessions', 'app', 1200400, 'collection'], ['profiles', 'app', 88021, 'collection']],
  dynamodb: [['inventory', '', -1], ['carts', '', -1]],
  sqlpoll: [['legacy_items', '', 18822], ['legacy_users', '', 4012]],
  mariadb: [['articles', 'blog', 4120], ['authors', 'blog', 88], ['comments', 'blog', 91200]],
  kafka: [['dbserver1.inventory.events', '', -1, 'topic'], ['dbserver1.inventory.products', '', -1, 'topic'], ['dbserver1.inventory.customers', '', -1, 'topic']],
};
function connTables(c, q = '') {
  const base = TABLES[c.type] || [['items', '', 1000], ['users', '', 200]];
  return base.map(([name, schema, rows, kind]) => ({ name, schema, kind: kind || (c.type === 'mongodb' ? 'collection' : c.type === 'kafka' ? 'topic' : 'table'), rows }))
    .filter((t) => !q || t.name.toLowerCase().includes(q.toLowerCase()));
}
function connView(c) {
  const config = clone(c.config);
  if (config.dsn) config.dsn = redactDSN(config.dsn);
  if (config.secretAccessKey) config.secretAccessKey = MASK;
  const { lastTest, ...rest } = c;
  return { ...rest, config, usedBy: S.tasks.filter((t) => t.source.connectorId === c.id).map((t) => ({ id: t.id, name: t.name, state: S.statuses[t.id]?.state || 'stopped' })) };
}
function cleanConfig(type, cfg = {}, prev = {}) {
  const out = {};
  for (const f of CONN_FIELDS[type] || []) {
    let v = cfg[f];
    if (f === 'dsn' && typeof v === 'string' && v.includes(MASK) && prev.dsn && redactDSN(prev.dsn) === v) v = prev.dsn;
    if (f === 'secretAccessKey' && v === MASK) v = prev.secretAccessKey;
    if (v !== undefined && v !== '' && !(Array.isArray(v) && !v.length)) out[f] = v;
  }
  return out;
}
function validateConnector(c) {
  const e = [];
  if (!String(c.name || '').trim()) e.push('name is required');
  if (!CONN_FIELDS[c.type]) e.push(`unsupported type "${c.type || ''}"`);
  const cfg = c.config || {};
  if (['postgres', 'mysql', 'mariadb', 'mongodb', 'sqlpoll', 'couchdb', 'sqlserver', 'cockroachdb', 'redis'].includes(c.type) && !cfg.dsn) e.push('config.dsn is required');
  if (c.type === 'dynamodb' && !cfg.region) e.push('config.region is required');
  if (c.type === 'kafka' && !(cfg.brokers || []).length) e.push('config.brokers is required');
  return e;
}
function testConnector(c) {
  const cfg = c.config || {};
  const bad = /fail|refused/.test(cfg.dsn || '') || /fail/.test((cfg.brokers || []).join());
  if (bad) return { ok: false, error: 'dial tcp 10.0.0.12:5432: connect: connection refused', latencyMs: 0, version: '', tables: [] };
  const version = { postgres: 'PostgreSQL 16.4', mysql: 'MySQL 8.0.39', mariadb: 'MariaDB 11.4.2', mongodb: 'MongoDB 7.0.12', kafka: 'Kafka 3.7 (3 brokers)', dynamodb: 'DynamoDB', sqlserver: 'SQL Server 2022', cockroachdb: 'CockroachDB v24.2', redis: 'Redis 7.4', couchdb: 'CouchDB 3.3.3' }[c.type] || '';
  return { ok: true, error: '', latencyMs: 8 + Math.round(rnd() * 30), version, tables: connTables(c) };
}
async function connectorsApi(req, res, parts, M, q) {
  const [, b, c] = parts;
  if (!b && M === 'GET') return send(res, 200, S.connectors.map(connView));
  if (!b && M === 'POST') {
    const x = await readBody(req) || {};
    const errs = validateConnector(x);
    if (errs.length) return err(res, 400, 'invalid connector', errs);
    const id = x.id && /^[a-zA-Z0-9_-]{1,64}$/.test(x.id) ? x.id : `c_${String(x.name).toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_|_$/g, '').slice(0, 30)}_${Math.random().toString(36).slice(2, 5)}`;
    if (S.connectors.some((y) => y.id === id)) return err(res, 409, `connector ${id} already exists`);
    const conn = { id, name: x.name.trim(), description: x.description || '', type: x.type, tags: x.tags || [], config: cleanConfig(x.type, x.config), createdAt: iso(), updatedAt: iso() };
    S.connectors.push(conn);
    return send(res, 201, connView(conn));
  }
  if (b === 'test' && M === 'POST') {
    const x = await readBody(req) || {};
    const prev = x.id ? S.connectors.find((y) => y.id === x.id) : null;
    await new Promise((r) => setTimeout(r, 300));
    return send(res, 200, testConnector({ ...x, config: cleanConfig(x.type, x.config, prev ? prev.config : {}) }));
  }
  const conn = S.connectors.find((y) => y.id === b);
  if (!conn) return err(res, 404, `connector ${b} not found`);
  if (!c && M === 'GET') return send(res, 200, connView(conn));
  if (!c && M === 'PUT') {
    const x = await readBody(req) || {};
    x.type = conn.type;
    const merged = { ...x, config: cleanConfig(conn.type, x.config, conn.config) };
    const errs = validateConnector(merged);
    if (errs.length) return err(res, 400, 'invalid connector', errs);
    Object.assign(conn, { name: x.name.trim(), description: x.description || '', tags: x.tags || [], config: merged.config, updatedAt: iso() });
    return send(res, 200, connView(conn));
  }
  if (!c && M === 'DELETE') {
    const used = connView(conn).usedBy;
    if (used.length) return send(res, 409, { error: `connector is used by ${used.length} task${used.length > 1 ? 's' : ''}`, usedBy: used });
    S.connectors = S.connectors.filter((y) => y !== conn);
    return send(res, 204);
  }
  if (c === 'test' && M === 'POST') { await new Promise((r) => setTimeout(r, 300)); return send(res, 200, testConnector(conn)); }
  if (c === 'tables' && M === 'GET') return send(res, 200, connTables(conn, q.get('q') || ''));
  return err(res, 404, 'not found');
}
/** Merge a connector's connection settings into a task source (connectorId + table level fields). */
function resolveSource(src) {
  if (!src || !src.connectorId) return null;
  const conn = S.connectors.find((y) => y.id === src.connectorId);
  if (!conn) return `connector ${src.connectorId} not found`;
  const table = {};
  for (const [k, v] of Object.entries(src)) if (!(CONN_FIELDS[conn.type] || []).includes(k) || k === 'database') table[k] = v;
  Object.assign(src, clone(conn.config), table, { type: conn.type, connectorId: conn.id });
  if (!src.database && conn.config.database) src.database = conn.config.database;
  return null;
}

function validateTask(t) {
  const errs = [];
  if (t.id && !/^[a-zA-Z0-9_-]{1,64}$/.test(t.id)) errs.push('id must match ^[a-zA-Z0-9_-]{1,64}$');
  if (!String(t.name || '').trim()) errs.push('name is required');
  if (!/^[a-zA-Z0-9_-]{1,400}$/.test(t.index?.uid || '')) errs.push('index.uid is required and may only contain a-z A-Z 0-9 - _');
  const s = t.source || {};
  if (!s.table) errs.push('source.table is required');
  if (['postgres', 'mysql', 'mariadb', 'mongodb'].includes(s.type) && !s.dsn) errs.push('source.dsn is required');
  if (s.type === 'mongodb' && !s.database) errs.push('source.database is required for mongodb');
  if (s.type === 'dynamodb' && !s.region) errs.push('source.region is required for dynamodb');
  if (s.type === 'kafka' && !(s.brokers || []).length) errs.push('source.brokers is required for kafka');
  if (s.type === 'sqlpoll' && (!s.dsn || !s.driver)) errs.push('source.dsn and source.driver are required for sqlpoll');
  if (!['postgres', 'mysql', 'mariadb', 'mongodb', 'dynamodb', 'kafka', 'sqlpoll', 'couchdb', 'sqlserver', 'cockroachdb', 'redis', 'webhook'].includes(s.type)) errs.push(`unsupported source type "${s.type || ''}"`);
  const r = t.sync?.safety?.maxDeleteRatio;
  if (r != null && (r < 0 || r > 1)) errs.push('sync.safety.maxDeleteRatio must be between 0 and 1');
  return errs;
}
function applyDefaults(t) {
  t.source ||= {}; t.tags ||= []; t.mapping ||= {}; t.sync ||= {}; t.index ||= {};
  t.mapping.primaryKeyField ||= 'id'; t.mapping.timeFormat ||= 'rfc3339';
  t.index.writeMode ||= 'replace'; t.index.manageSettings = !!t.index.manageSettings;
  t.sync.mode ||= 'snapshot_stream'; t.sync.batchSize ||= 1000; t.sync.flushInterval ||= '1s'; t.sync.maxInflight ||= 4;
  t.sync.retry = { maxAttempts: 8, initialBackoff: '200ms', maxBackoff: '30s', ...(t.sync.retry || {}) };
  t.sync.safety = { maxDeleteRatio: 0, minDocsForDeleteGuard: 1000, allowTruncate: false, zeroDowntimeResync: false, dumpBeforeResync: false, ...(t.sync.safety || {}) };
  if (t.source.type === 'postgres') t.source.schema ||= 'public';
  t.desiredState ||= 'stopped';
  return t;
}
function ensureIndex(uid, destId) {
  const store = destId ? (S.destIndexes[destId] ||= {}) : S.indexes;
  if (!store[uid]) store[uid] = { uid, primaryKey: 'id', createdAt: iso(), updatedAt: iso(), numberOfDocuments: 0, isIndexing: false, settings: clone(MEILI_DEFAULT_SETTINGS), sample: null, deleted: new Set() };
}

// ---------------------------------------------------------------- SSE
const clients = new Set();
function broadcast(event, data) {
  const msg = `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
  for (const res of clients) res.write(msg);
}
const extraConfig = () => ({
  singleDestination: !!S.single,
  onboarding: { destinations: S.destinations.length, connectors: S.connectors.length, tasks: S.tasks.length, complete: S.destinations.length > 0 && S.tasks.length > 0 },
});
const accounts = createAccounts({ extraConfig: () => extraConfig(), send: (...a) => send(...a), err: (...a) => err(...a), readBody: (r) => readBody(r), broadcast: (e, d) => broadcast(e, d), tasks: () => S.tasks, adminToken });

function log(taskId, level, message) {
  const e = { time: iso(), taskId, level, message };
  (S.logs[taskId] ||= []).push(e);
  if (S.logs[taskId].length > 500) S.logs[taskId].shift();
  broadcast('log', e);
}
function setState(id, state, extra = {}) {
  const st = S.statuses[id];
  Object.assign(st, { state, updatedAt: iso() }, extra);
  if (!ACTIVE.has(state)) { st.rate = 0; st.inflight = 0; }
  broadcast('status', st);
}

function tick() {
  S.tick++;
  const now = Date.now();
  for (const t of S.tasks) {
    const st = S.statuses[t.id];
    if (!ACTIVE.has(st.state)) continue;
    if (st.state === 'starting') {
      const fresh = st.snapshot.total === 0 || st.snapshot.done < st.snapshot.total;
      if (t.sync.mode !== 'stream' && fresh) {
        const rc = schemaFor(t.source.table).rowCount;
        st.snapshot = { done: st.snapshot.done || 0, total: rc > 0 ? rc : 50000 };
        st.state = 'snapshotting'; log(t.id, 'info', `snapshot started: ~${st.snapshot.total.toLocaleString('en')} rows`);
      } else { st.state = 'streaming'; log(t.id, 'info', 'streaming changes'); }
      st.startedAt = iso(); st.attempt = 0; st.error = '';
    } else if (st.state === 'snapshotting' || st.state === 'resyncing') {
      const step = Math.max(1, Math.round(st.snapshot.total * (0.006 + rnd() * 0.006)));
      st.snapshot.done = Math.min(st.snapshot.total, st.snapshot.done + step);
      st.rate = Math.round(step * (0.9 + rnd() * 0.2)); st.counters.upserted += step; st.counters.events += step; st.counters.batches += Math.ceil(step / 1000);
      st.inflight = 4; st.lagMs = 0;
      if (st.snapshot.done >= st.snapshot.total) { st.state = 'streaming'; log(t.id, 'info', `snapshot finished: ${st.snapshot.total.toLocaleString('en')} rows`); }
    } else if (st.state === 'streaming') {
      const base = t.id === 'products-pg' ? 420 : 150;
      st.rate = Math.round(clamp(st.rate + (rnd() - 0.5) * base * 0.35, base * 0.35, base * 1.7) * 10) / 10;
      const up = Math.round(st.rate * 0.97), del = Math.round(st.rate * 0.03);
      st.counters.upserted += up; st.counters.deleted += del; st.counters.events += up + del; st.counters.batches += 1;
      if (rnd() < 0.3) st.counters.coalesced += Math.floor(rnd() * 8);
      st.lagMs = Math.round(clamp(st.lagMs + (rnd() - 0.5) * 120, 40, 900)); st.inflight = 1 + Math.floor(rnd() * 3);
      st.lastEventAt = iso();
      if (/^\d+\/[0-9A-F]+$/.test(st.checkpoint)) { const [a, b] = st.checkpoint.split('/'); st.checkpoint = `${a}/${(parseInt(b, 16) + up * 64).toString(16).toUpperCase()}`; }
      else if (/:\d+$/.test(st.checkpoint)) st.checkpoint = st.checkpoint.replace(/(\d+)$/, (m) => String(Number(m) + up));
      else st.checkpoint = String(Number(st.checkpoint || 0) + up);
      if (S.tick % 3 === 0) log(t.id, 'info', `flushed batch of ${up * 3} documents (meili task ${S.nextMeiliUid})`);
      if (S.tick % 4 === 0) {
        S.meiliTasks.push(mkMeiliTask(S.nextMeiliUid++, t.index.uid, 'documentAdditionOrUpdate', 'succeeded', now - 300, `cdc:${t.id}`));
        if (S.meiliTasks.length > 400) S.meiliTasks.splice(0, 50);
      }
      if (S.indexes[t.index.uid]) S.indexes[t.index.uid].numberOfDocuments += up - del;
    } else if (st.state === 'retrying') {
      if (S.tick % 3 === 0) {
        st.attempt = st.attempt >= 6 ? 1 : st.attempt + 1; st.counters.retries += 1; st.lagMs += 3000;
        log(t.id, 'warn', `retrying in ${(0.2 * 2 ** st.attempt).toFixed(1)}s (attempt ${st.attempt}/${t.sync.retry.maxAttempts})`);
      }
    }
    st.updatedAt = iso();
    broadcast('status', st);
  }
  if (S.tick % 2 === 0) {
    for (const t of S.tasks) {
      const st = S.statuses[t.id];
      const m = S.metrics[t.id];
      m.push({ t: now, rate: st.rate, lagMs: st.lagMs, upserted: st.counters.upserted, deleted: st.counters.deleted });
      if (m.length > 300) m.shift();
    }
  }
  for (const d of Object.values(S.diffs)) {
    if (d.status === 'running' && now - Date.parse(d.startedAt) > 2500) finishDiff(d);
  }
  for (const mt of S.meiliTasks) {
    if (mt.status === 'processing' && rnd() < 0.15) Object.assign(mt, { status: 'succeeded', finishedAt: iso(), duration: 'PT0.412S', batchUid: mt.uid });
  }
  for (const d of S.destinations) {
    if (Date.now() >= hstate(d).nextCheckAt) broadcast('destination.health', { id: d.id, health: runCheck(d) });
  }
  broadcast('overview', overview());
}
setInterval(tick, 1000);
setInterval(() => { for (const res of clients) res.write(': ping\n\n'); }, 15000);

function finishDiff(d) {
  const t = S.tasks.find((x) => x.id === d.taskId);
  const ix = t ? (S.destIndexes[destOf(t)] || {})[t.index.uid] : null;
  const n = ix?.numberOfDocuments || 0;
  const bad = t?.id === 'legacy-poll' || n === 0 ? 0 : 1;
  Object.assign(d, {
    status: 'done', finishedAt: iso(), sourceCount: n + 2 * bad, indexCount: n, missing: 2 * bad, extra: 0, mismatched: bad, inSync: !bad,
    samples: { missing: bad ? ['1201', '1307'] : [], extra: [], mismatched: bad ? [{ id: '1450', fields: [{ field: 'name', source: 'Granite lamp', index: 'Granite lamp (old)' }, { field: 'stock', source: 4, index: 9 }] }] : [] },
  });
}

// ---------------------------------------------------------------- search & docs
function docAt(ix, i) {
  if (ix.sample === 'movies') {
    const genres = [['Drama'], ['Comedy', 'Romance'], ['Action', 'Thriller'], ['Animation', 'Family'], ['Science Fiction']];
    return { id: i + 1, title: `${WORDS[i % 20][0].toUpperCase()}${WORDS[i % 20].slice(1)} of the ${WORDS[(i * 7 + 3) % 20]}`, genres: genres[i % 5], year: 1960 + (i * 7) % 64, overview: `A story about ${WORDS[(i * 3) % 20]} and ${WORDS[(i * 11) % 20]}.` };
  }
  const t = S.tasks.find((x) => x.index.uid === ix.uid);
  if (!ix.sample || !t) return { id: i + 1, value: `${WORDS[i % 20]}-${i}` };
  const sch = schemaFor(ix.sample);
  const row = Object.fromEntries(sch.columns.map((c) => [c.name, fakeValue(c, i)]));
  return transformRow(t, row);
}
function matchFilter(doc, filter) {
  if (!filter) return true;
  return filter.split(/\s+AND\s+/i).every((cl) => {
    const m = cl.trim().match(/^([\w.]+)\s*(=|!=|>=|<=|>|<)\s*(.+)$/);
    if (!m) throw Object.assign(new Error(`Was expecting an operation \`=\`, \`!=\`, \`>=\`, \`>\`, \`<=\`, \`<\` at \`${cl}\`.`), { code: 'invalid_search_filter' });
    let [, f, op, v] = m; v = v.replace(/^["']|["']$/g, '');
    const dv = doc[f]; const vals = Array.isArray(dv) ? dv : [dv];
    return vals.some((x) => {
      const num = Number(v); const isNum = !Number.isNaN(num) && typeof x === 'number';
      const a = isNum ? x : String(x); const b = isNum ? num : v;
      return op === '=' ? a == b : op === '!=' ? a != b : op === '>' ? a > b : op === '<' ? a < b : op === '>=' ? a >= b : a <= b;
    });
  });
}
function search(ix, body) {
  const started = performance.now();
  const q = String(body.q || '').toLowerCase().trim();
  const pool = Math.min(ix.numberOfDocuments, 600);
  let hits = [];
  for (let i = 0; i < pool; i++) {
    const d = docAt(ix, i);
    if (q && !q.split(/\s+/).every((w) => JSON.stringify(d).toLowerCase().includes(w))) continue;
    if (!matchFilter(d, body.filter)) continue;
    hits.push(d);
  }
  for (const s of [...(body.sort || [])].reverse()) {
    const [f, dir] = s.split(':');
    hits.sort((a, b) => (a[f] > b[f] ? 1 : a[f] < b[f] ? -1 : 0) * (dir === 'desc' ? -1 : 1));
  }
  const total = q || body.filter ? hits.length : ix.numberOfDocuments;
  const limit = body.limit ?? 20, offset = body.offset ?? 0;
  const facetDistribution = {};
  for (const f of body.facets || []) {
    facetDistribution[f] = {};
    for (const h of hits) for (const v of [].concat(h[f] ?? [])) facetDistribution[f][v] = (facetDistribution[f][v] || 0) + 1;
  }
  return { hits: hits.slice(offset, offset + limit), query: body.q || '', processingTimeMs: Math.max(1, Math.round(performance.now() - started)), limit, offset, estimatedTotalHits: total, ...(body.facets?.length ? { facetDistribution } : {}) };
}

// ---------------------------------------------------------------- http
function send(res, code, body) {
  if (code === 204) { res.writeHead(204); return res.end(); }
  res.writeHead(code, { 'content-type': 'application/json', 'cache-control': 'no-store' });
  res.end(JSON.stringify(body));
}
const err = (res, code, message, details) => send(res, code, details ? { error: message, details } : { error: message });
async function readBody(req) {
  let raw = '';
  for await (const c of req) raw += c;
  if (!raw) return undefined;
  try { return JSON.parse(raw); } catch { const e = new Error('invalid JSON body'); e.status = 400; throw e; }
}
const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.svg': 'image/svg+xml', '.json': 'application/json', '.txt': 'text/plain; charset=utf-8', '.png': 'image/png', '.ico': 'image/x-icon' };
function serveStatic(req, res, pathname) {
  const p = pathname === '/' ? '/index.html' : decodeURIComponent(pathname);
  const file = path.resolve(WEB_ROOT, '.' + p);
  if (!file.startsWith(WEB_ROOT) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) { res.writeHead(404); return res.end('not found'); }
  res.writeHead(200, { 'content-type': MIME[path.extname(file)] || 'application/octet-stream', 'cache-control': 'no-store' });
  fs.createReadStream(file).pipe(res);
}

const TASK_ACTIONS = new Set(['start', 'stop', 'pause', 'resume', 'restart', 'resync', 'reset-checkpoint']);
function doAction(t, action) {
  const st = S.statuses[t.id];
  const active = ACTIVE.has(st.state);
  switch (action) {
    case 'start':
      if (active) throw Object.assign(new Error('task is already running'), { status: 409 });
      t.desiredState = 'running'; setState(t.id, 'starting', { error: '', message: '', attempt: 0 }); log(t.id, 'info', 'task started'); break;
    case 'stop': t.desiredState = 'stopped'; setState(t.id, 'stopped', { message: 'stopped by operator' }); log(t.id, 'info', 'task stopped'); break;
    case 'pause':
      if (!active) throw Object.assign(new Error('task is not running'), { status: 409 });
      t.desiredState = 'paused'; setState(t.id, 'paused', { message: 'paused by operator' }); log(t.id, 'info', 'paused by operator'); break;
    case 'resume':
      if (st.state !== 'paused') throw Object.assign(new Error('task is not paused'), { status: 409 });
      t.desiredState = 'running'; setState(t.id, 'streaming', { message: '' }); st.rate = 150; log(t.id, 'info', 'resumed'); break;
    case 'restart': t.desiredState = 'running'; setState(t.id, 'starting', { error: '', message: '' }); log(t.id, 'info', 'task restarted'); break;
    case 'resync':
      t.desiredState = 'running'; setState(t.id, 'resyncing', { error: '', message: t.sync.safety.zeroDowntimeResync ? `rebuilding into ${t.index.uid}__shadow` : 'resync in progress', snapshot: { done: 0, total: Math.max(5000, S.indexes[t.index.uid]?.numberOfDocuments || 5000) } });
      log(t.id, 'info', 'resync started'); break;
    case 'reset-checkpoint':
      if (active) throw Object.assign(new Error('stop the task before resetting its checkpoint'), { status: 409 });
      st.checkpoint = ''; st.snapshot = { done: 0, total: 0 }; broadcast('status', st); log(t.id, 'warn', 'checkpoint reset'); break;
  }
  t.updatedAt = iso();
}

function schemaReport(t) {
  const sch = schemaFor(t.source.table);
  const ix = S.indexes[t.index.uid];
  const st = ix?.settings || MEILI_DEFAULT_SETTINGS;
  const m = t.mapping;
  const pkCols = m.primaryKey?.length ? m.primaryKey : sch.primaryKey;
  const fields = []; const issues = [];
  for (const c of sch.columns) {
    if (pkCols.includes(c.name) && !m.keepKeyColumns) continue;
    const excluded = (m.include?.length && !m.include.includes(c.name)) || m.exclude?.includes(c.name);
    const name = m.rename?.[c.name] || c.name;
    const settings = [];
    if (st.filterableAttributes?.includes(name)) settings.push('filterable');
    if (st.sortableAttributes?.includes(name)) settings.push('sortable');
    if (st.searchableAttributes?.includes(name) || st.searchableAttributes?.includes('*')) settings.push('searchable');
    if (st.displayedAttributes?.includes(name) || st.displayedAttributes?.includes('*')) settings.push('displayed');
    const fi = [];
    if (excluded) fi.push('excluded by mapping');
    const coverage = excluded ? 0 : c.nullable ? 0.62 + (c.name.length % 4) * 0.09 : 1;
    if (!excluded && c.nullable && coverage < 0.75) fi.push(`only ${Math.round(coverage * 100)}% of documents have this field`);
    if (/timestamp|datetime|date/.test(c.type) && settings.includes('sortable') && m.timeFormat === 'rfc3339') fi.push('dates are strings with timeFormat rfc3339: use unix to sort or filter by range');
    fields.push({ name, sourceColumn: c.name, sourceType: c.type, inSource: true, inIndex: !excluded, coverage, settings, issues: fi });
  }
  fields.unshift({ name: m.primaryKeyField || 'id', sourceColumn: pkCols.join('+'), sourceType: 'primary key', inSource: true, inIndex: true, coverage: 1, settings: ['displayed'], issues: [] });
  for (const f of [...(st.filterableAttributes || []), ...(st.sortableAttributes || [])]) {
    if (!fields.some((x) => x.name === f)) { fields.push({ name: f, sourceColumn: '', sourceType: '', inSource: false, inIndex: false, coverage: 0, settings: ['filterable'], issues: ['configured in settings but not produced by the mapping'] }); issues.push({ level: 'warning', message: `"${f}" is in index settings but no source column maps to it` }); }
  }
  const pkOk = (ix?.primaryKey || 'id') === (m.primaryKeyField || 'id');
  if (!pkOk) issues.push({ level: 'error', message: `index primary key "${ix?.primaryKey}" differs from mapping.primaryKeyField "${m.primaryKeyField}"` });
  if (fields.some((f) => f.issues.some((i) => i.startsWith('dates')))) issues.push({ level: 'warning', message: 'some date fields are sortable but written as strings' });
  if (!t.index.manageSettings) issues.push({ level: 'info', message: 'settings are not managed by this task; drift is not corrected automatically' });
  return { taskId: t.id, indexUid: t.index.uid, primaryKey: { source: pkCols, field: m.primaryKeyField || 'id', index: ix?.primaryKey || '', ok: pkOk }, fields, issues, checkedAt: iso() };
}

async function api(req, res, url) {
  const p = url.pathname.replace(/^\/api\/v1/, '') || '/';
  const parts = p.split('/').filter(Boolean).map(decodeURIComponent);
  const q = url.searchParams;
  const M = req.method;

  // ---- accounts: public endpoints, authentication, role checks
  if (parts[0] === 'auth' && parts[1] !== 'me' && await accounts.publicAuth(req, res, parts, M)) return;
  const user = accounts.authenticate(req, url, parts[0] === 'events');
  if (!user) return err(res, 401, 'not authenticated');
  const need = accounts.requiredRole(M, parts);
  if (ROLE_RANK[user.role] < ROLE_RANK[need]) return err(res, 403, `requires role ${need}`);
  if (await accounts.handle(req, res, parts, M, url, user)) return;
  if (M !== 'GET' && ['tasks', 'indexes', 'meili'].includes(parts[0]) && !(parts[2] === 'search' || parts[3] === 'preview' || parts[1] === 'test')) {
    const action = { tasks: 'task', indexes: 'index', meili: 'meili' }[parts[0]] + '.' + (parts.length > 2 ? parts.slice(2).join('.') : parts[1] && parts.length === 2 && M !== 'POST' ? M.toLowerCase() : parts[1] || 'create');
    accounts.audit(user, action, parts[1] || '', '', req);
  }

  if (p === '/events' && M === 'GET') {
    res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store', connection: 'keep-alive' });
    res.write(': connected\n\n');
    res.write(`event: overview\ndata: ${JSON.stringify(overview())}\n\n`);
    for (const st of Object.values(S.statuses)) res.write(`event: status\ndata: ${JSON.stringify(st)}\n\n`);
    clients.add(res);
    req.on('close', () => clients.delete(res));
    return;
  }
  if (p === '/health') {
    const d = q.get('destination') ? S.destinations.find((x) => x.id === q.get('destination')) : defaultDest();
    if (!d) return send(res, 200, { status: 'ok', meilisearch: { status: 'unconfigured', version: '' } });
    const h = destHealth(d);
    return send(res, 200, { status: 'ok', meilisearch: { status: h.healthy ? 'available' : 'unavailable', version: h.version, error: h.error } });
  }
  if (p === '/overview') return send(res, 200, overview());

  // ---- destinations
  if (parts[0] === 'destinations') return destinationsApi(req, res, parts, M);

  // ---- every instance at once: GET /indexes?destination=*
  if (p === '/indexes' && M === 'GET' && q.get('destination') === '*') {
    return send(res, 200, S.destinations.map((d) => {
      const ok = destHealthy(d);
      const ix = Object.values(S.destIndexes[d.id] || {});
      return { destinationId: d.id, destinationName: d.name, isDefault: d.isDefault, healthy: ok, ...(ok ? {} : { error: destHealth(d).error }),
        indexes: ok ? ix.map((i) => ({ uid: i.uid, primaryKey: i.primaryKey, createdAt: i.createdAt, updatedAt: i.updatedAt, numberOfDocuments: i.numberOfDocuments, isIndexing: i.isIndexing, managedBy: S.tasks.filter((t) => t.index.uid === i.uid && destOf(t) === d.id).map((t) => t.id), destinationId: d.id })) : [] };
    }));
  }

  // ---- indexes and Meilisearch endpoints are scoped to ?destination= (default when omitted)
  if (parts[0] === 'indexes' || parts[0] === 'meili') {
    const dest = q.get('destination') ? S.destinations.find((d) => d.id === q.get('destination')) : defaultDest();
    if (!dest) return err(res, q.get('destination') ? 404 : 409, q.get('destination') ? `destination ${q.get('destination')} not found` : 'no Meilisearch destination is configured');
    if (!destHealthy(dest)) return err(res, 502, `meilisearch ${dest.name} is unreachable: ${destHealth(dest).error}`);
    const saved = [S.indexes, S.meiliTasks];
    S.indexes = S.destIndexes[dest.id] ||= {}; S.meiliTasks = S.destMeiliTasks[dest.id] ||= [];
    S.scopeDest = dest.id;
    try { return await scopedApi(req, res, parts, M, q, url, p); } finally { [S.indexes, S.meiliTasks] = saved; S.scopeDest = null; }
  }

  // ---- connectors
  if (parts[0] === 'connectors') return connectorsApi(req, res, parts, M, q);

  // ---- tasks
  if (parts[0] === 'tasks') {
    if (parts.length === 1 && M === 'GET') {
      const state = q.get('state'), qq = (q.get('q') || '').toLowerCase(), tag = q.get('tag');
      return send(res, 200, S.tasks.filter((t) => (!state || S.statuses[t.id].state === state) && (!tag || t.tags.includes(tag)) && (!qq || `${t.id} ${t.name} ${t.index.uid} ${t.source.table}`.toLowerCase().includes(qq))).map(view));
    }
    if (parts.length === 1 && M === 'POST') {
      const body = await readBody(req) || {};
      const rerr = resolveSource(body.source);
      if (rerr) return err(res, 400, rerr);
      applyDefaults(body);
      const errs = validateTask(body);
      if (errs.length) return err(res, 400, 'invalid task', errs);
      if (badSettingsKey(body.index.settings)) return err(res, 422, `invalid index settings: unknown field \`${badSettingsKey(body.index.settings)}\``);
      if (body.index.destinationId && !S.destinations.some((d) => d.id === body.index.destinationId)) return err(res, 400, `destination ${body.index.destinationId} not found`);
      body.id ||= `${(body.name || 'task').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 40) || 'task'}-${Math.random().toString(36).slice(2, 6)}`;
      if (S.tasks.some((t) => t.id === body.id)) return err(res, 409, `task ${body.id} already exists`);
      body.createdAt = body.updatedAt = iso();
      S.tasks.push(body);
      S.statuses[body.id] = baseStatus(body.id, 'stopped', { started: false });
      S.metrics[body.id] = []; S.logs[body.id] = [];
      ensureIndex(body.index.uid, destOf(body));
      log(body.id, 'info', 'task created');
      broadcast('task', { id: body.id, action: 'created' });
      if (q.get('start') === 'true') doAction(body, 'start');
      return send(res, 201, view(body));
    }
    if (parts[1] === 'batch' && M === 'POST') {
      const { ids = [], action } = await readBody(req) || {};
      if (action === 'delete' && ROLE_RANK[user.role] < ROLE_RANK.admin) return err(res, 403, 'requires role admin');
      if (!TASK_ACTIONS.has(action) && action !== 'delete') return err(res, 400, `unknown action "${action}"`);
      const results = ids.map((id) => {
        const t = S.tasks.find((x) => x.id === id);
        if (!t) return { id, ok: false, error: 'task not found' };
        try {
          if (action === 'delete') { S.tasks = S.tasks.filter((x) => x.id !== id); delete S.statuses[id]; broadcast('task', { id, action: 'deleted' }); }
          else { doAction(t, action); broadcast('task', { id, action: 'updated' }); }
          return { id, ok: true, error: '' };
        } catch (e) { return { id, ok: false, error: e.message }; }
      });
      return send(res, 200, { results });
    }
    if (parts[1] === 'test' && M === 'POST') {
      const raw = await readBody(req) || { source: {} };
      const rerr = resolveSource(raw.source);
      if (rerr) return send(res, 200, { ok: false, error: rerr, schema: null, sample: [], primaryKey: '' });
      const t = applyDefaults(raw);
      await new Promise((r) => setTimeout(r, 450));
      if (!t.source?.table) return send(res, 200, { ok: false, error: 'source.table is required', schema: null, sample: [], primaryKey: '' });
      if (/fail|refused/.test(t.source.dsn || '') || /fail/.test((t.source.brokers || []).join())) return send(res, 200, { ok: false, error: 'dial tcp 10.0.0.12:5432: connect: connection refused', schema: null, sample: [], primaryKey: '' });
      const schema = schemaFor(t.source.table);
      const sample = [0, 1, 2, 3, 4].map((i) => transformRow(t, Object.fromEntries(schema.columns.map((c) => [c.name, fakeValue(c, i)]))));
      return send(res, 200, { ok: true, error: '', schema, sample, primaryKey: t.mapping.primaryKeyField || 'id' });
    }
    if (parts[1] === 'export' && M === 'GET') return send(res, 200, S.tasks.map(redactTask));
    if (parts[1] === 'import' && M === 'POST') {
      const list = await readBody(req);
      if (!Array.isArray(list)) return err(res, 400, 'expected an array of tasks');
      let created = 0; const errors = [];
      for (const raw of list) {
        const t = applyDefaults(clone(raw));
        const e = validateTask(t);
        if (t.id && S.tasks.some((x) => x.id === t.id)) e.push(`task ${t.id} already exists`);
        if (e.length) { errors.push(`${t.id || t.name || 'task'}: ${e.join('; ')}`); continue; }
        t.id ||= `imported-${Math.random().toString(36).slice(2, 8)}`; t.createdAt = t.updatedAt = iso(); t.desiredState = 'stopped';
        S.tasks.push(t); S.statuses[t.id] = baseStatus(t.id, 'stopped', { started: false }); S.metrics[t.id] = []; created++;
        broadcast('task', { id: t.id, action: 'created' });
      }
      return send(res, 200, { created, errors });
    }
    const t = S.tasks.find((x) => x.id === parts[1]);
    if (!t) return err(res, 404, `task ${parts[1]} not found`);
    if (parts.length === 2) {
      if (M === 'GET') return send(res, 200, view(t));
      if (M === 'PUT') {
        const raw = await readBody(req) || {};
        const rerr = resolveSource(raw.source);
        if (rerr) return err(res, 400, rerr);
        const body = applyDefaults(raw);
        body.id = t.id;
        const errs = validateTask(body);
        if (errs.length) return err(res, 400, 'invalid task', errs);
        if (badSettingsKey(body.index.settings)) return err(res, 422, `invalid index settings: unknown field \`${badSettingsKey(body.index.settings)}\``);
        if (body.source.dsn?.includes(MASK) && redactDSN(t.source.dsn) === body.source.dsn) body.source.dsn = t.source.dsn;
        if (body.source.secretAccessKey === MASK) body.source.secretAccessKey = t.source.secretAccessKey;
        Object.assign(t, body, { createdAt: t.createdAt, updatedAt: iso() });
        ensureIndex(t.index.uid);
        if (ACTIVE.has(S.statuses[t.id].state)) doAction(t, 'restart');
        log(t.id, 'info', 'task updated');
        broadcast('task', { id: t.id, action: 'updated' });
        return send(res, 200, view(t));
      }
      if (M === 'DELETE') {
        const di = q.get('deleteIndex');
        if (di && di !== t.index.uid) return err(res, 400, 'deleteIndex must equal the index uid');
        S.tasks = S.tasks.filter((x) => x !== t); delete S.statuses[t.id];
        if (di) delete S.indexes[di];
        broadcast('task', { id: t.id, action: 'deleted' });
        return send(res, 204);
      }
    }
    const sub = parts[2];
    if (TASK_ACTIONS.has(sub) && M === 'POST') {
      try { doAction(t, sub); } catch (e) { return err(res, e.status || 400, e.message); }
      broadcast('task', { id: t.id, action: 'updated' });
      return send(res, 200, view(t));
    }
    if (sub === 'settings' && !parts[3] && M === 'GET') return send(res, 200, taskSettings(t));
    if (sub === 'settings' && parts[3] === 'apply' && M === 'POST') {
      const destIx = S.destIndexes[destOf(t)] ||= {};
      if (!destIx[t.index.uid]) destIx[t.index.uid] = { uid: t.index.uid, primaryKey: 'id', createdAt: iso(), updatedAt: iso(), numberOfDocuments: 0, isIndexing: false, settings: clone(MEILI_DEFAULT_SETTINGS), sample: null, deleted: new Set() };
      Object.assign(destIx[t.index.uid].settings, clone(t.index.settings || {}));
      log(t.id, 'info', 'index settings applied');
      return send(res, 200, taskSettings(t));
    }
    if (sub === 'logs') return send(res, 200, (S.logs[t.id] || []).slice(-Number(q.get('limit') || 200)));
    if (sub === 'metrics') return send(res, 200, { points: S.metrics[t.id] || [] });
    if (sub === 'schema-check') return send(res, 200, schemaReport(t));
    if (sub === 'diff' && parts.length === 3) {
      if (M === 'POST') {
        await readBody(req);
        S.diffs[t.id] = { taskId: t.id, status: 'running', error: '', startedAt: iso(), finishedAt: ZERO_TIME, sourceCount: 0, indexCount: 0, missing: 0, extra: 0, mismatched: 0, inSync: false, samples: { missing: [], extra: [], mismatched: [] } };
        return send(res, 202, { status: 'running' });
      }
      return send(res, 200, S.diffs[t.id] || { taskId: t.id, status: 'none' });
    }
    if (sub === 'diff' && parts[3] === 'repair' && M === 'POST') {
      const b = await readBody(req) || {};
      const d = S.diffs[t.id];
      if (!d) return err(res, 404, 'no diff has been run for this task');
      const upserted = (b.missing ? d.missing : 0) + (b.mismatched ? d.mismatched : 0); const deleted = b.extra ? d.extra : 0;
      if (b.missing) { d.missing = 0; d.samples.missing = []; }
      if (b.mismatched) { d.mismatched = 0; d.samples.mismatched = []; }
      if (b.extra) { d.extra = 0; d.samples.extra = []; }
      d.inSync = !d.missing && !d.extra && !d.mismatched;
      log(t.id, 'info', `diff repair: ${upserted} upserted, ${deleted} deleted`);
      return send(res, 200, { upserted, deleted });
    }
    if (sub === 'dlq') {
      const list = S.dlq[t.id] || [];
      if (M === 'GET' && parts.length === 3) return send(res, 200, list.slice(0, Number(q.get('limit') || 100)));
      const b = await readBody(req) || {};
      const ids = b.ids || [];
      const sel = ids.length ? list.filter((e) => ids.includes(e.id)) : list;
      S.dlq[t.id] = list.filter((e) => !sel.includes(e));
      if (parts[3] === 'replay' && M === 'POST') {
        const failed = sel.filter((e) => e.code === 'invalid_document_id').length;
        S.dlq[t.id].push(...sel.filter((e) => e.code === 'invalid_document_id').map((e) => ({ ...e, attempts: e.attempts + 1 })));
        return send(res, 200, { replayed: sel.length - failed, failed });
      }
      if (M === 'DELETE') return send(res, 200, { deleted: sel.length });
    }
    return err(res, 404, 'not found');
  }

  return err(res, 404, `no route for ${M} ${p}`);
}

async function scopedApi(req, res, parts, M, q, url, p) {
  // ---- indexes
  if (parts[0] === 'indexes') {
    const managed = (uid) => S.tasks.filter((t) => t.index.uid === uid && destOf(t) === S.scopeDest).map((t) => t.id);
    const enqueue = (indexUid, type, status = 'succeeded') => { const mt = mkMeiliTask(S.nextMeiliUid++, indexUid, type, status); S.meiliTasks.push(mt); return mt.uid; };
    if (parts.length === 1 && M === 'GET') return send(res, 200, Object.values(S.indexes).map((i) => ({ uid: i.uid, primaryKey: i.primaryKey, createdAt: i.createdAt, updatedAt: i.updatedAt, numberOfDocuments: i.numberOfDocuments, isIndexing: i.isIndexing, managedBy: managed(i.uid), destinationId: S.scopeDest })));
    if (parts.length === 1 && M === 'POST') {
      const b = await readBody(req) || {};
      if (!/^[a-zA-Z0-9_-]{1,400}$/.test(b.uid || '')) return err(res, 400, 'invalid index uid');
      if (S.indexes[b.uid]) return err(res, 409, `index ${b.uid} already exists`);
      ensureIndex(b.uid); S.indexes[b.uid].primaryKey = b.primaryKey || null;
      return send(res, 200, { taskUid: enqueue(b.uid, 'indexCreation') });
    }
    if (parts[1] === 'swap' && M === 'POST') {
      const b = await readBody(req) || {};
      const A = S.indexes[b.a], B = S.indexes[b.b];
      if (!A) return err(res, 404, `index ${b.a} not found`);
      if (b.rename) {
        if (B) return err(res, 409, `index ${b.b} already exists`);
        delete S.indexes[b.a]; S.indexes[b.b] = { ...A, uid: b.b };
      } else {
        if (!B) return err(res, 404, `index ${b.b} not found`);
        S.indexes[b.a] = { ...B, uid: b.a }; S.indexes[b.b] = { ...A, uid: b.b };
      }
      return send(res, 200, { taskUid: enqueue(null, 'indexSwap') });
    }
    const ix = S.indexes[parts[1]];
    if (!ix) return err(res, 404, `index ${parts[1]} not found`);
    const sub = parts[2];
    if (!sub) {
      if (M === 'GET') {
        const fd = {}; const d0 = docAt(ix, 0);
        for (const k of Object.keys(d0)) fd[k] = Math.round(ix.numberOfDocuments * (k === ix.primaryKey ? 1 : 0.7 + hash(k.length, 9) * 0.3));
        return send(res, 200, { uid: ix.uid, primaryKey: ix.primaryKey, createdAt: ix.createdAt, updatedAt: ix.updatedAt, stats: { numberOfDocuments: ix.numberOfDocuments, rawDocumentDbSize: ix.numberOfDocuments * 812, avgDocumentSize: 812, isIndexing: ix.isIndexing, numberOfEmbeddings: 0, numberOfEmbeddedDocuments: 0, fieldDistribution: fd }, managedBy: managed(ix.uid) });
      }
      if (M === 'DELETE') {
        if (q.get('confirm') !== ix.uid) return err(res, 400, 'confirm must equal the index uid');
        const running = S.tasks.filter((t) => t.index.uid === ix.uid && ACTIVE.has(S.statuses[t.id].state));
        if (running.length) return err(res, 409, `task ${running[0].id} is writing to this index; stop it first`);
        delete S.indexes[ix.uid];
        return send(res, 200, { taskUid: enqueue(ix.uid, 'indexDeletion') });
      }
    }
    if (sub === 'settings') {
      if (parts[3] === 'preview' && M === 'POST') {
        const b = await readBody(req) || {};
        const changes = Object.entries(b).filter(([k, v]) => JSON.stringify(ix.settings[k] ?? null) !== JSON.stringify(v)).map(([key, desired]) => ({ key, current: ix.settings[key] ?? null, desired }));
        return send(res, 200, { changes });
      }
      if (parts[3] === 'reset' && M === 'POST') { ix.settings = clone(MEILI_DEFAULT_SETTINGS); return send(res, 200, { taskUid: enqueue(ix.uid, 'settingsUpdate') }); }
      if (M === 'GET') return send(res, 200, ix.settings);
      if (M === 'PATCH') {
        const b = await readBody(req) || {};
        const unknown = Object.keys(b).find((k) => !(k in MEILI_DEFAULT_SETTINGS));
        await new Promise((r) => setTimeout(r, 300));
        if (unknown) { const uid = enqueue(ix.uid, 'settingsUpdate', 'failed'); return send(res, 200, { taskUid: uid, status: 'failed', error: `Unknown field \`${unknown}\`: expected one of \`displayedAttributes\`, \`searchableAttributes\`, \`filterableAttributes\`, ...` }); }
        Object.assign(ix.settings, b); ix.updatedAt = iso();
        return send(res, 200, { taskUid: enqueue(ix.uid, 'settingsUpdate'), status: 'succeeded', error: '' });
      }
    }
    if (sub === 'primary-key' && M === 'PUT') {
      const b = await readBody(req) || {};
      if (ix.numberOfDocuments > 0) return err(res, 409, 'the primary key cannot be changed while the index contains documents');
      ix.primaryKey = b.primaryKey; return send(res, 200, { taskUid: enqueue(ix.uid, 'indexUpdate') });
    }
    if (sub === 'search' && M === 'POST') {
      const b = await readBody(req) || {};
      try { return send(res, 200, search(ix, b)); } catch (e) { return err(res, 400, e.message); }
    }
    if (sub === 'documents' && M === 'GET') {
      const limit = Number(q.get('limit') || 20), offset = Number(q.get('offset') || 0);
      const results = [];
      for (let i = offset; i < Math.min(ix.numberOfDocuments, offset + limit); i++) results.push(docAt(ix, i));
      return send(res, 200, { results, total: ix.numberOfDocuments, limit, offset });
    }
    if (sub === 'compact' && M === 'POST') return send(res, 200, { taskUid: enqueue(ix.uid, 'indexCompaction') });
    return err(res, 404, 'not found');
  }

  // ---- meili instance
  if (parts[0] === 'meili') {
    if (parts[1] === 'tasks' && parts.length === 2 && M === 'GET') {
      const list = (k) => (q.get(k) || '').split(',').filter(Boolean);
      const st = list('status'), iu = list('indexUid'), ty = list('type');
      const limit = Number(q.get('limit') || 50); const from = q.has('from') && q.get('from') !== '' ? Number(q.get('from')) : Infinity;
      const all = [...S.meiliTasks].reverse().filter((t) => (!st.length || st.includes(t.status)) && (!iu.length || iu.includes(t.indexUid)) && (!ty.length || ty.includes(t.type)));
      const page = all.filter((t) => t.uid <= from);
      const results = page.slice(0, limit);
      return send(res, 200, { results, total: all.length, limit, from: results[0]?.uid ?? null, next: page[limit]?.uid ?? null });
    }
    if (parts[1] === 'tasks' && parts[2] === 'cancel' && M === 'POST') {
      const { uids = [] } = await readBody(req) || {};
      const cu = S.nextMeiliUid++;
      for (const t of S.meiliTasks) if (uids.includes(t.uid) && ['enqueued', 'processing'].includes(t.status)) Object.assign(t, { status: 'canceled', canceledBy: cu, finishedAt: iso(), duration: 'PT0S' });
      S.meiliTasks.push({ ...mkMeiliTask(cu, null, 'taskCancelation', 'succeeded'), details: { matchedTasks: uids.length, canceledTasks: uids.length, originalFilter: `?uids=${uids.join(',')}` } });
      return send(res, 200, { taskUid: cu });
    }
    if (parts[1] === 'stats') {
      const ixs = Object.fromEntries(Object.values(S.indexes).map((i) => [i.uid, { numberOfDocuments: i.numberOfDocuments, isIndexing: i.isIndexing, fieldDistribution: {} }]));
      return send(res, 200, { databaseSize: 5_812_420_608, usedDatabaseSize: 4_901_002_240, lastUpdate: iso(Date.now() - 4000), indexes: ixs });
    }
    if ((parts[1] === 'dumps' || parts[1] === 'snapshots') && M === 'POST') {
      const uid = S.nextMeiliUid++; S.meiliTasks.push(mkMeiliTask(uid, null, parts[1] === 'dumps' ? 'dumpCreation' : 'snapshotCreation', 'enqueued'));
      return send(res, 200, { taskUid: uid });
    }
    if (parts[1] === 'experimental-features') {
      if (M === 'PATCH') { const b = await readBody(req) || {}; for (const [k, v] of Object.entries(b)) if (k in S.experimental) S.experimental[k] = !!v; }
      return send(res, 200, S.experimental);
    }
  }
  return err(res, 404, `no route for ${M} ${p}`);
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  try {
    if (url.pathname === '/__mock/reset' && req.method === 'POST') {
      const empty = url.searchParams.get('empty') === '1';
      if (empty) emptyState(); else initState();
      accounts.init({ empty });
      return send(res, 200, { ok: true });
    }
    if (url.pathname === '/__mock/single' && req.method === 'POST') {
      // MEILI_URL mode: one destination, managed by the environment
      S.single = url.searchParams.get('on') !== '0';
      if (S.single) {
        S.destinations = S.destinations.filter((d) => d.isDefault);
        if (!S.destinations.length) S.destinations.push({ id: 'default', name: 'default', description: '', url: 'http://meilisearch:7700', apiKey: 'env', gzip: true, timeout: '30s', isDefault: true, managedByEnv: true, tags: [], createdAt: iso(), updatedAt: iso() });
        Object.assign(S.destinations[0], { managedByEnv: true, url: 'http://meilisearch:7700' });
        for (const t of S.tasks) if (t.index.destinationId && t.index.destinationId !== S.destinations[0].id) t.index.destinationId = '';
      }
      return send(res, 200, { ok: true });
    }
    if (url.pathname === '/__mock/login' && req.method === 'POST') {
      const role = url.searchParams.get('role') || 'admin';
      const u = accounts.state().users.find((x) => x.role === role);
      if (!u) return err(res, 404, `no seeded ${role}`);
      accounts.newSession(res, u);
      return send(res, 200, { user: accounts.pub(u) });
    }
    if (url.pathname === '/__mock/smtp' && req.method === 'POST') {
      const on = (await readBody(req))?.configured !== false;
      Object.assign(accounts.state().smtp, on ? { host: 'smtp.example.com', port: 587, username: 'cdc', password: 'secret', from: 'cdc@example.com', tls: 'starttls' } : { host: '' });
      return send(res, 200, { ok: true });
    }
    if (url.pathname === '/__mock/telegram' && req.method === 'POST') {
      const on = (await readBody(req))?.configured !== false;
      Object.assign(accounts.state().telegram, on ? { botToken: '123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw', botName: 'meili_cdc_bot' } : { botToken: '', botName: '' });
      return send(res, 200, { ok: true });
    }
    if (url.pathname === '/__mock/alert' && req.method === 'POST') return send(res, 200, accounts.emitAlert(await readBody(req) || {}));
    if (url.pathname === '/__mock/outbox' && req.method === 'GET') return send(res, 200, accounts.state().outbox);
    if (url.pathname.startsWith('/api/v1')) return await api(req, res, url);
    if (url.pathname === '/metrics') { res.writeHead(200, { 'content-type': 'text/plain' }); return res.end('# mock\n'); }
    return serveStatic(req, res, url.pathname);
  } catch (e) {
    if (!res.headersSent) err(res, e.status || 500, e.message);
  }
});
server.listen(PORT, () => console.log(`mock meilisearch-cdc listening on http://localhost:${PORT}`));
