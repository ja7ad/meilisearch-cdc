// Turns the raw captures in docs/img/raw into numbered 1920x1080 slides in
// docs/img (headline, subline, the screen in a window or phone frame) and
// writes docs/screenshots.md from the same list.
//   node test/ui/showcase/frame.mjs
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { chromium } from '../node_modules/playwright/index.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(HERE, '../../..');
const RAW = path.join(ROOT, 'docs/img/raw');
const OUT = path.join(ROOT, 'docs/img');
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'cdc-frame-'));

// n, file, window title, headline, subline, longer explanation (screenshots.md)
const SLIDES = [
  ['01', 'overview', 'Overview', 'Every pipeline, live', 'Throughput, documents, alerts and every Meilisearch instance on one screen',
    'The overview shows how many tasks run, the combined throughput, documents written, firing alerts and dead letters. Each Meilisearch instance has a health strip, the throughput chart and activity feed update live over server-sent events, and every pipeline has a card with its state, rate and lag.'],
  ['02', 'tasks', 'Tasks', 'All your syncs in one list', 'Search, filter by source, state or tag, and act on many tasks at once',
    'The task list shows every pipeline with its source, target index and instance, live state, rate sparkline and lag. Filters and search narrow it down; selecting several tasks opens a batch bar to start, pause, resume, restart, resync or delete them together.'],
  ['03', 'task-detail', 'Task · Customers', 'Live counters, rate and lag', 'Upserts, deletes, batches, retries and dead letters, updated in real time',
    'A task page shows its counters, documents per second and replication lag over time, the checkpoint and runtime details. Tabs hold the activity log, schema check, diff, dead letter queue, index settings and the full configuration; the header has the lifecycle actions.'],
  ['04', 'wizard-connector', 'New task', 'Start from a saved connector', 'Connections live in connectors — a task only picks one',
    'Creating a task starts by choosing a connector. Credentials are stored once, encrypted, in the connector and never appear in the task wizard. A new connector can be added from a side panel without leaving the wizard.'],
  ['05', 'wizard-source', 'New task · Source', 'Pick a table, preview real rows', 'Discovery lists the tables; a test reads the schema and sample documents',
    'The source step lists the tables the connector can see. Testing the connection reads the table schema and transforms a few real rows into the documents that will be sent to Meilisearch.'],
  ['06', 'wizard-mapping', 'New task · Mapping', 'Shape every document', 'Include, exclude and rename columns, composite keys, soft deletes, geo and static fields',
    'The mapping step decides how rows become documents: which columns to keep, renames, the primary key (several columns build a composite id), time formats, soft delete columns, geo coordinates and static fields.'],
  ['07', 'wizard-index-settings', 'New task · Index', 'Index settings as you create', 'Pick the instance, filterable and sortable fields and any Meilisearch setting',
    'The index step picks the Meilisearch instance and index, the write mode and the index settings. Quick toggles make fields searchable, filterable or sortable; the full editor covers every setting. Managed settings are applied when the task starts and watched for drift.'],
  ['08', 'schema-diff', 'Task · Diff', 'Prove the index is in sync', 'A document-by-document diff with one-click repair',
    'The diff reads every id from the source and the index and compares documents field by field. Missing, extra and mismatched documents are listed and can be repaired in one click; the schema check compares source columns with index fields and settings.'],
  ['09', 'connectors', 'Connectors', 'Eleven sources, one place', 'PostgreSQL, MySQL, MariaDB, MongoDB, DynamoDB, CouchDB, SQL Server, CockroachDB, Kafka, Redis and more',
    'Connectors are reusable, encrypted source connections. The list shows each connector with its host, the result of its last connection test and the tasks that use it.'],
  ['10', 'connector-detail', 'Connector · shop-db', 'See what a connector sees', 'Test the connection, browse tables with row counts, start a task from any table',
    'A connector page tests the connection (server version and latency), lists the tables, collections or topics it can read with their row counts, and starts a task from any of them. Secrets are masked.'],
  ['11', 'destinations', 'Destinations', 'Many Meilisearch instances', 'Background health checks, latency, failures and the tasks writing to each',
    'Every Meilisearch instance is a destination with its own health check interval. Cards show version, documents, indexes and latency, consecutive failures for an unreachable instance, the running tasks and the last sync.'],
  ['12', 'destination-detail', 'Destination · Production (EU)', 'Everything about one instance', 'Health, synced tasks, indexes, task queue, backups and experimental features',
    'A destination page has live health with a "Check now" button, the sync summary and the tasks writing to it, and tabs for its indexes, Meilisearch task queue, dumps and snapshots, experimental features and settings.'],
  ['13', 'indexes', 'Indexes', 'Indexes, grouped by instance', 'Documents, primary keys and the tasks that own each index',
    'The indexes page groups indexes per Meilisearch instance, with document counts, primary keys, indexing state and the tasks that manage each one. Indexes can be created, swapped, compacted and deleted here.'],
  ['14', 'search', 'Index · products', 'A search playground on live data', 'Query, filter and sort to check relevance as data changes',
    'Each index has a search playground that queries Meilisearch directly with filters, sort and limits, a documents browser and its settings, so the effect of a sync or a setting is visible right away.'],
  ['15', 'index-settings', 'Index · settings', 'Every Meilisearch setting', 'Form and JSON editors with a preview of exactly what changes',
    'The settings editor covers searchable, displayed, filterable (including granular features), sortable and distinct attributes, ranking rules, typo tolerance, synonyms, stop words, dictionaries, faceting, pagination, embedders and more. Changes are previewed before they are applied.'],
  ['16', 'meili-tasks', 'Meilisearch tasks', 'The Meilisearch task queue', 'CDC writes are tagged, so every task traces back to its pipeline',
    'The Meilisearch task queue of each instance, filterable by status, index and type, with durations and errors. Writes made by the service carry custom metadata naming the pipeline; stuck tasks can be cancelled.'],
  ['17', 'alerts', 'Alerts', 'Alerts that reach you', 'Failures, lag, dead letters and outages — by email, Telegram or webhook',
    'Alert rules watch task states, replication lag, error bursts, dead letters, stalled snapshots and unreachable instances. Firing alerts are shown live and delivered by email, Telegram and webhook, with cooldowns, reminders and resolve notifications.'],
  ['18', 'users', 'Settings · Users', 'Your team, your roles', 'Viewer, moderator and admin, invitations by email and a protected owner',
    'Team members have a role: viewers look, moderators operate tasks, admins configure everything. Invitations are emailed when SMTP is set up; the primary administrator cannot be demoted or removed from the dashboard.'],
  ['19', 'mcp', 'Settings · MCP', 'Built for AI agents', 'An MCP server with per-role tool access and revocable tokens',
    'The built-in Model Context Protocol server lets AI assistants inspect and operate pipelines. Each bearer token has a role, tools and resources are allowed per role, every call is checked and mutating calls are audited.'],
  ['20', 'email', 'Settings · Email', 'Email in minutes', 'SMTP with automatic TLS, test sends and readable errors',
    'SMTP settings for invitations, password resets and alerts, with automatic TLS selection by port and a test email that explains failures.'],
  ['21', 'dark', 'Overview · dark', 'Easy on the eyes', 'Every screen in light and dark themes',
    'The whole dashboard follows the system theme or a manual choice.'],
  ['22', 'mobile', null, 'Fits in your pocket', 'Fully responsive: check pipelines from your phone',
    'Every page works on a phone: tables become cards, dialogs become bottom sheets and actions stay within reach.'],
  ['23', 'cli', null, 'An admin CLI on the server', 'Reset passwords, manage users and purge data, even while the service runs',
    'The same binary has an admin CLI for operators. It talks to the running service through a private socket in the data directory (or opens the store when the service is stopped) to list users, reset a forgotten password, change roles, transfer ownership, revoke sessions and tokens and purge data.'],
  ['24', 'dlq', 'Task · DLQ', "Bad documents don't stop the sync", 'Rejected documents are isolated in a dead letter queue to replay or discard',
    'When Meilisearch rejects a document, the batch is bisected so only the offending documents go to the dead letter queue, with the error; the rest of the pipeline keeps flowing. Entries can be inspected, replayed or discarded.'],
];

const url = (f) => pathToFileURL(f).href;
const css = `
  *{box-sizing:border-box;margin:0}
  body{width:1920px;height:1080px;overflow:hidden;font-family:Inter,-apple-system,"Segoe UI",Roboto,sans-serif;color:#fff;
    background:radial-gradient(1200px 700px at 85% -10%,rgba(255,92,170,.32),transparent 60%),
      radial-gradient(1000px 700px at -10% 110%,rgba(108,71,255,.35),transparent 60%),linear-gradient(160deg,#1b1630 0%,#231b45 55%,#2c1a46 100%)}
  header{text-align:center;padding-top:62px}
  h1{font-size:68px;font-weight:800;letter-spacing:-.025em;line-height:1.05}
  p.sub{margin-top:18px;font-size:30px;color:#d8d2f4;font-weight:450}
  .stage{position:absolute;left:0;right:0;top:250px;display:flex;justify-content:center}
  .win{width:1270px;border-radius:16px;overflow:hidden;background:#fff;box-shadow:0 40px 90px -20px rgba(10,5,30,.75),0 0 0 1px rgba(255,255,255,.08)}
  .bar{height:40px;background:#f4f2fa;display:flex;align-items:center;padding:0 16px;gap:8px;border-bottom:1px solid #e6e2f2;position:relative}
  .dot{width:12px;height:12px;border-radius:50%}.r{background:#ff5f57}.y{background:#febc2e}.g{background:#28c840}
  .title{position:absolute;left:0;right:0;text-align:center;font-size:15px;color:#5b5675;font-weight:600}
  .win.dark .bar{background:#24203a;border-color:#2f2a4a}.win.dark .title{color:#b9b3d6}
  .win img{display:block;width:100%}
  .phones{display:flex;gap:56px;align-items:flex-start}
  .phone{width:330px;border-radius:52px;padding:12px;background:#0d0b16;box-shadow:0 40px 80px -20px rgba(0,0,0,.7),0 0 0 2px #3b355a}
  .phone div{border-radius:42px;overflow:hidden;height:690px;background:#fff}
  .phone img{width:100%;display:block}
  .term{width:1270px;border-radius:16px;overflow:hidden;background:#15121f;box-shadow:0 40px 90px -20px rgba(10,5,30,.75),0 0 0 1px rgba(255,255,255,.08)}
  .term .bar{background:#221d33;border-color:#2c2640}.term .title{color:#a9a2c9}
  pre{padding:26px 30px;font:19px/1.55 "JetBrains Mono","SF Mono",Menlo,monospace;color:#e7e3ff;white-space:pre-wrap;overflow-wrap:anywhere}
  .cmd{color:#ff7bb8}.err{color:#ff8a8a}.ok{color:#8ce8a6}`;

function page(body) {
  return `<!doctype html><html><head><meta charset="utf-8"><style>${css}</style></head><body>${body}</body></html>`;
}
const header = (h, s) => `<header><h1>${h}</h1><p class="sub">${s}</p></header>`;
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;');

function cliHTML() {
  const text = fs.readFileSync(path.join(HERE, 'cli.txt'), 'utf8').trimEnd();
  return text.split('\n').map((l) => {
    if (l.startsWith('$ ')) return `<span class="cmd">${esc(l)}</span>`;
    if (l.startsWith('error:')) return `<span class="err">${esc(l)}</span>`;
    if (/^(reset|created|revoked|deleted)/.test(l)) return `<span class="ok">${esc(l)}</span>`;
    return esc(l);
  }).join('\n');
}

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1920, height: 1080 }, deviceScaleFactor: 1 });
const pg = await ctx.newPage();

async function render(html, out, clip) {
  const f = path.join(TMP, 'slide.html');
  fs.writeFileSync(f, html);
  await pg.goto(url(f));
  await pg.waitForLoadState('load');
  await pg.evaluate(() => document.fonts && document.fonts.ready);
  await pg.screenshot({ path: out, ...(clip ? { clip } : {}) });
}

for (const [n, name, title, h, s] of SLIDES) {
  const out = path.join(OUT, `${n}-${name}.png`);
  let body;
  if (name === 'mobile') {
    const phones = ['mobile-overview', 'mobile-task', 'mobile-indexes', 'mobile-wizard']
      .map((m) => `<div class="phone"><div><img src="${url(path.join(RAW, `${m}.png`))}"></div></div>`).join('');
    body = header(h, s) + `<div class="stage" style="top:236px"><div class="phones">${phones}</div></div>`;
  } else if (name === 'cli') {
    const term = `<div class="term" id="term"><div class="bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span>
      <span class="title">ops@cdc-prod-1: ~</span></div><pre>${cliHTML()}</pre></div>`;
    // The bare terminal is also kept as a raw image (for the website).
    await render(page(`<div style="padding:40px">${term}</div>`), path.join(RAW, '23-cli.png'), { x: 40, y: 40, width: 1270, height: 760 });
    body = header(h, s) + `<div class="stage">${term}</div>`;
  } else {
    const dark = name === 'dark' ? ' dark' : '';
    body = header(h, s) + `<div class="stage"><div class="win${dark}"><div class="bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span>
      <span class="title">Meili CDC — ${title}</span></div><img src="${url(path.join(RAW, `${n}-${name}.png`))}"></div></div>`;
  }
  await render(page(body), out);
  console.log(`✓ docs/img/${n}-${name}.png`);
}
await browser.close();
fs.rmSync(TMP, { recursive: true, force: true });

// ---------------------------------------------------------------- docs/screenshots.md
const md = ['# Screenshots', '',
  'A tour of the dashboard on a production-like workspace: 15 pipelines from eleven kinds of sources into three Meilisearch',
  'instances, live traffic, a team, alerts and AI access. The images are generated by `test/ui/showcase` (see the end of this page).', ''];
for (const [n, name, , h, s, text] of SLIDES) {
  md.push(`## ${Number(n)}. ${h}`, '', `![${h}: ${s}](img/${n}-${name}.png)`, '', `**${s}.** ${text}`, '');
}
md.push('## Regenerating', '', 'With the end-to-end environment running (`make e2e-env`, the lab, see [e2e.md](e2e.md)):', '', '```bash',
  '(set -a; . deploy/e2e/showcase.env; ./bin/meilisearch-cdc) &   # :8781, a separate workspace',
  'node test/ui/showcase/setup.mjs        # destinations, connectors, tasks, team, alerts, MCP',
  'node test/ui/showcase/traffic.mjs 900 & # live changes for the charts; wait a few minutes',
  'node test/ui/showcase/capture.mjs      # raw 2x captures in docs/img/raw',
  'node test/ui/showcase/frame.mjs        # numbered slides in docs/img and this page', '```', '');
fs.writeFileSync(path.join(ROOT, 'docs/screenshots.md'), md.join('\n'));
console.log('✓ docs/screenshots.md');
