#!/usr/bin/env node
/*
 * Rebuilds docs/llms.txt and docs/llms-full.txt (https://llmstxt.org).
 *
 *   node docs/site/build-llms.mjs
 *
 * No dependencies.
 *   - llms.txt: project summary, then "## Docs" with one link per guide page
 *     (title and description from docs/guide/sitemap-pages.json, or the page's
 *     <title>/<meta name="description"> when the JSON is missing), the
 *     reference docs in the repository, and "## Optional".
 *   - llms-full.txt: the main content of every guide page, converted to
 *     Markdown-like plain text (headings, paragraphs, lists, tables, code
 *     blocks); navigation, sidebars, pagers and footers are dropped.
 * Run it again whenever guide pages change.
 */
import { readFileSync, writeFileSync, existsSync, readdirSync } from 'node:fs';
import { join, dirname, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const BASE = 'https://meilicdc.javad.dev/';
const GH = 'https://github.com/ja7ad/meilisearch-cdc';
const DOCS = join(dirname(fileURLToPath(import.meta.url)), '..');
const urlFor = (p) => BASE + p.split(sep).join('/').replace(/(^|\/)index\.html$/, '$1');

const ENT = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ', mdash: '—', ndash: '–', hellip: '…', rarr: '→', larr: '←', middot: '·', times: '×', copy: '©' };
const decode = (s) => s.replace(/&(#x?[0-9a-f]+|[a-z]+);/gi, (m, e) => {
  if (e[0] === '#') return String.fromCodePoint(e[1].toLowerCase() === 'x' ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10));
  return ENT[e.toLowerCase()] ?? m;
});
const strip = (s) => decode(s.replace(/<[^>]+>/g, '')).replace(/[ \t\r\n]+/g, ' ').trim();

function walk(dir) {
  const out = [];
  if (!existsSync(dir)) return out;
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) { if (e.name !== 'assets') out.push(...walk(p)); }
    else if (e.name.endsWith('.html')) out.push(p);
  }
  return out.sort();
}

function pages() {
  const json = join(DOCS, 'guide', 'sitemap-pages.json');
  let list = existsSync(json)
    ? JSON.parse(readFileSync(json, 'utf8'))
    : walk(join(DOCS, 'guide')).map((abs) => ({ path: relative(DOCS, abs) }));
  return list.filter((p) => existsSync(join(DOCS, p.path))).map((p) => {
    const html = readFileSync(join(DOCS, p.path), 'utf8');
    const title = p.title || strip((html.match(/<title>([\s\S]*?)<\/title>/i) || [, p.path])[1]).replace(/\s*[|·–—-]\s*Meili CDC.*$/i, '');
    const description = p.description || decode((html.match(/<meta name="description" content="([^"]*)"/i) || [, ''])[1]);
    return { ...p, title, description, html };
  });
}

// HTML (main content) -> Markdown-ish text.
function toText(html) {
  let m = html.match(/<article[^>]*>([\s\S]*?)<\/article>/i) || html.match(/<main[^>]*>([\s\S]*?)<\/main>/i);
  let s = m ? m[1] : html;
  s = s.replace(/<(script|style|nav|aside|footer|header|template|svg|button)\b[\s\S]*?<\/\1>/gi, '');
  // code blocks first, kept verbatim
  const blocks = [];
  s = s.replace(/<pre[^>]*>([\s\S]*?)<\/pre>/gi, (_, c) => {
    const lang = (c.match(/class="[^"]*language-([\w-]+)/) || [, ''])[1];
    blocks.push('\n```' + lang + '\n' + decode(c.replace(/<[^>]+>/g, '')).replace(/\n+$/, '') + '\n```\n');
    return `\u0000${blocks.length - 1}\u0000`;
  });
  s = s.replace(/<h([1-6])[^>]*>([\s\S]*?)<\/h\1>/gi, (_, n, t) => `\n\n${'#'.repeat(+n)} ${strip(t)}\n\n`);
  s = s.replace(/<tr[^>]*>([\s\S]*?)<\/tr>/gi, (_, r) => '\n| ' + [...r.matchAll(/<t[hd][^>]*>([\s\S]*?)<\/t[hd]>/gi)].map((c) => strip(c[1]).replace(/\|/g, '\\|')).join(' | ') + ' |');
  s = s.replace(/<li[^>]*>([\s\S]*?)<\/li>/gi, (_, t) => '\n- ' + strip(t.replace(/<code[^>]*>([\s\S]*?)<\/code>/gi, '`$1`')));
  s = s.replace(/<code[^>]*>([\s\S]*?)<\/code>/gi, '`$1`');
  s = s.replace(/<a [^>]*href="([^"]+)"[^>]*>([\s\S]*?)<\/a>/gi, (_, h, t) => {
    const txt = strip(t);
    if (!txt) return '';
    if (/^#/.test(h)) return txt;
    const abs = /^https?:/.test(h) ? h : BASE + 'guide/' + h.replace(/^\.\//, '');
    return `[${txt}](${abs})`;
  });
  s = s.replace(/<\/(p|div|section|table|ul|ol|blockquote|dl|dd|dt)>/gi, '\n\n').replace(/<br\s*\/?>/gi, '\n');
  s = s.replace(/<[^>]+>/g, '');
  s = decode(s).split('\n').map((l) => l.replace(/[ \t]+/g, ' ').trim()).join('\n');
  s = s.replace(/\u0000(\d+)\u0000/g, (_, i) => blocks[+i]);
  return s.replace(/\n{3,}/g, '\n\n').trim();
}

const list = pages();
const summary = 'meilisearch-cdc (Meili CDC) is open-source change data capture into Meilisearch: it copies a table once, then streams every insert, update and delete into one or many Meilisearch instances in near real time, managed from a live dashboard.';

let llms = `# meilisearch-cdc

> ${summary}

It is a Go service with an embedded state store and dashboard. Sources: PostgreSQL (logical replication), MySQL/MariaDB (binlog), MongoDB (change streams), DynamoDB Streams, CouchDB, SQL Server CDC, CockroachDB changefeeds, Kafka (Debezium), Redis Streams, webhook push and SQL polling. It snapshots then streams with no gap, delivers at least once with idempotent writes, isolates rejected documents in a dead letter queue, resyncs with zero downtime through a shadow index swap, manages full Meilisearch index settings with drift detection, and offers roles, alerts (email, Telegram, webhook), an MCP server for AI agents, an operator CLI and Prometheus metrics. Binaries for Linux, macOS and Windows (amd64, arm64) and a Docker image (ghcr.io/ja7ad/meilisearch-cdc).

## Docs

`;
for (const p of list) llms += `- [${p.title}](${urlFor(p.path)})${p.description ? ': ' + p.description : ''}\n`;
llms += `- [REST API reference](${GH}/blob/main/docs/api.md): every /api/v1 endpoint for tasks, data quality, dead letters, indexes, destinations and settings.
- [Operator CLI](${GH}/blob/main/docs/cli.md): meilisearch-cdc admin commands for users, password reset, ownership, sessions, MCP and purge.
- [Performance and tuning](${GH}/blob/main/docs/performance.md): measured throughput, resource usage, profiling and tuning knobs.
- [End-to-end environment](${GH}/blob/main/docs/e2e.md): every source and two Meilisearch instances in Docker for trying and testing the dashboard.

## Optional

- [Landing page](${BASE}): overview, download links and FAQ.
- [GitHub repository](${GH}): source code, issues and README.
- [Releases](${GH}/releases): binaries and checksums for every version.
- [REST API reference (raw Markdown)](https://raw.githubusercontent.com/ja7ad/meilisearch-cdc/main/docs/api.md)
- [Full documentation as one file](${BASE}llms-full.txt)
`;
writeFileSync(join(DOCS, 'llms.txt'), llms);

let full = `# meilisearch-cdc documentation\n\n> ${summary}\n\nSource: ${BASE}guide/ — generated from the HTML guide.\n`;
for (const p of list) full += `\n\n---\n\nURL: ${urlFor(p.path)}\n\n${toText(p.html)}\n`;
writeFileSync(join(DOCS, 'llms-full.txt'), full);
console.log(`llms.txt: ${list.length} guide pages; llms-full.txt: ${full.length} chars`);
