#!/usr/bin/env node
/*
 * Rebuilds docs/sitemap.xml for the GitHub Pages site.
 *
 *   node docs/site/build-sitemap.mjs
 *
 * No dependencies. Entries:
 *   - the landing page (docs/index.html),
 *   - every documentation page: from docs/guide/sitemap-pages.json
 *     ([{path, title, lastmod}], path relative to docs/) when it exists,
 *     otherwise every docs/guide/**\/*.html found on disk.
 * <lastmod> comes from the JSON or else from the file's modification time.
 * Every URL is absolute (https://meilicdc.javad.dev/...) and
 * entries whose file does not exist are skipped with a warning.
 * Run it again whenever guide pages are added or renamed.
 */
import { readFileSync, writeFileSync, existsSync, statSync, readdirSync } from 'node:fs';
import { join, dirname, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const BASE = 'https://meilicdc.javad.dev/';
const DOCS = join(dirname(fileURLToPath(import.meta.url)), '..');

const day = (d) => d.toISOString().slice(0, 10);
const mtime = (p) => day(statSync(join(DOCS, p)).mtime);
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
// "guide/index.html" -> "guide/", "index.html" -> ""
const urlFor = (p) => BASE + p.split(sep).join('/').replace(/(^|\/)index\.html$/, '$1');

function walk(dir) {
  const out = [];
  if (!existsSync(dir)) return out;
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) { if (e.name !== 'assets') out.push(...walk(p)); }
    else if (e.name.endsWith('.html') && e.name !== '404.html') out.push(p);
  }
  return out;
}

const entries = [{ path: 'index.html', lastmod: mtime('index.html'), changefreq: 'weekly', priority: '1.0' }];

const jsonPath = join(DOCS, 'guide', 'sitemap-pages.json');
let pages = [];
if (existsSync(jsonPath)) {
  pages = JSON.parse(readFileSync(jsonPath, 'utf8')).map((p) => ({ path: p.path, lastmod: p.lastmod }));
} else {
  pages = walk(join(DOCS, 'guide')).map((abs) => ({ path: relative(DOCS, abs) }));
}
for (const p of pages) {
  if (!existsSync(join(DOCS, p.path))) { console.warn('skip (missing file):', p.path); continue; }
  const isIndex = /(^|\/)index\.html$/.test(p.path);
  entries.push({
    path: p.path,
    lastmod: p.lastmod || mtime(p.path),
    changefreq: isIndex ? 'weekly' : 'monthly',
    priority: isIndex ? '0.9' : '0.7',
  });
}

const xml = '<?xml version="1.0" encoding="UTF-8"?>\n' +
  '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n' +
  entries.map((e) => `  <url>\n    <loc>${esc(urlFor(e.path))}</loc>\n    <lastmod>${e.lastmod}</lastmod>\n    <changefreq>${e.changefreq}</changefreq>\n    <priority>${e.priority}</priority>\n  </url>`).join('\n') +
  '\n</urlset>\n';
writeFileSync(join(DOCS, 'sitemap.xml'), xml);
console.log(`sitemap.xml: ${entries.length} URLs`);
