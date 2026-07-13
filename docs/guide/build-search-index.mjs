#!/usr/bin/env node
// Rebuilds the Meili CDC user guide from its pages. Run from anywhere:
//
//   node docs/guide/build-search-index.mjs
//
// Each page's source of truth is its <article class="doc"> ... </article>.
// The script keeps the article and regenerates everything around it from
// assets/nav.js: <head> metadata (title, description, canonical, Open Graph,
// Twitter, JSON-LD TechArticle + BreadcrumbList), the top bar, the sidebar,
// breadcrumbs, previous/next links and the footer. It gives h2/h3/h4
// headings without an id a stable one, then writes:
//
//   assets/search-index.json   client-side search index (fetched lazily)
//   assets/search-index.js     the same index as a script, for file:// use
//   sitemap-pages.json         [{path, title, description, lastmod}] per page
//
// No dependencies: Node 18 or newer.
import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const ROOT = dirname(fileURLToPath(import.meta.url));
const SITE = 'https://meilicdc.javad.dev/';
const GUIDE = SITE + 'guide/';
const OG_IMAGE = SITE + 'img/og.png';
const REPO = 'https://github.com/ja7ad/meilisearch-cdc';

// ---------- navigation ----------
const ctx = { window: {} };
vm.runInNewContext(readFileSync(join(ROOT, 'assets/nav.js'), 'utf8'), ctx);
const NAV = ctx.window.GUIDE_NAV;
if (!NAV || !Array.isArray(NAV.sections)) throw new Error('assets/nav.js does not define window.GUIDE_NAV.sections');
const pages = [];
for (const s of NAV.sections) for (const p of s.pages) pages.push({ ...p, section: s.title, sectionFirst: s.pages[0].file });
const seen = new Set();
for (const p of pages) {
  if (seen.has(p.file)) throw new Error(`duplicate page ${p.file} in nav.js`);
  seen.add(p.file);
}

// ---------- helpers ----------
const escHtml = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const ENT = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ', rarr: '→', larr: '←', mdash: '—', ndash: '–', hellip: '…', times: '×', middot: '·', rsaquo: '›', lsquo: '‘', rsquo: '’', ldquo: '“', rdquo: '”', copy: '©', ge: '≥', le: '≤' };
const decode = (s) => s.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (m, e) => {
  if (e[0] === '#') return String.fromCodePoint(e[1] === 'x' || e[1] === 'X' ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10));
  return ENT[e.toLowerCase()] ?? m;
});
const toText = (html) => decode(html
  .replace(/<(script|style|svg|template)\b[\s\S]*?<\/\1>/gi, ' ')
  .replace(/<br\s*\/?>/gi, ' ')
  .replace(/<\/(p|li|div|tr|td|th|h\d|pre|dt|dd|figcaption|summary)>/gi, ' ')
  .replace(/<[^>]+>/g, ''))
  .replace(/\s+/g, ' ').trim();
const slug = (s) => s.toLowerCase().replace(/[^a-z0-9\s_-]/g, '').trim().replace(/\s+/g, '-').slice(0, 64) || 'section';
const jsonLd = (o) => JSON.stringify(o, null, 1).replace(/<\//g, '<\\/');

// ---------- shell ----------
const icon = {
  menu: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16"/></svg>',
  search: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg>',
  moon: '<svg class="i-moon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>',
  sun: '<svg class="i-sun" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>',
  github: '<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12 .5a11.5 11.5 0 0 0-3.64 22.41c.58.1.79-.25.79-.56v-2c-3.2.7-3.88-1.37-3.88-1.37-.52-1.33-1.28-1.69-1.28-1.69-1.05-.71.08-.7.08-.7 1.16.08 1.77 1.19 1.77 1.19 1.03 1.77 2.7 1.26 3.36.96.1-.75.4-1.26.73-1.55-2.56-.29-5.25-1.28-5.25-5.69 0-1.26.45-2.29 1.19-3.1-.12-.29-.52-1.46.11-3.05 0 0 .97-.31 3.17 1.18a11 11 0 0 1 5.77 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.76.11 3.05.74.81 1.19 1.84 1.19 3.1 0 4.42-2.7 5.39-5.27 5.68.41.36.78 1.06.78 2.14v3.17c0 .31.21.67.8.56A11.5 11.5 0 0 0 12 .5z"/></svg>'
};

function head(p, crumbs) {
  const url = GUIDE + p.file;
  const title = `${p.title} · Meili CDC Docs`;
  const updated = p.updated || NAV.updated;
  const ld = {
    '@context': 'https://schema.org',
    '@graph': [
      {
        '@type': 'TechArticle', headline: p.title, description: p.description, dateModified: updated,
        inLanguage: 'en', url, mainEntityOfPage: url, image: OG_IMAGE,
        author: { '@type': 'Organization', name: 'meilisearch-cdc contributors', url: REPO },
        publisher: { '@type': 'Organization', name: 'Meili CDC', url: SITE },
        isPartOf: { '@type': 'WebSite', name: 'Meili CDC', url: SITE }
      },
      {
        '@type': 'BreadcrumbList',
        itemListElement: crumbs.map((c, i) => ({ '@type': 'ListItem', position: i + 1, name: c.name, item: c.abs }))
      }
    ]
  };
  return `<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${escHtml(title)}</title>
<meta name="description" content="${escHtml(p.description)}">
<link rel="canonical" href="${url}">
<meta name="robots" content="index, follow">
<meta name="color-scheme" content="light dark">
<meta name="theme-color" content="#FFFFFF" media="(prefers-color-scheme: light)">
<meta name="theme-color" content="#15112A" media="(prefers-color-scheme: dark)">
<meta property="og:type" content="article">
<meta property="og:site_name" content="Meili CDC">
<meta property="og:locale" content="en_US">
<meta property="og:title" content="${escHtml(title)}">
<meta property="og:description" content="${escHtml(p.description)}">
<meta property="og:url" content="${url}">
<meta property="og:image" content="${OG_IMAGE}">
<meta property="og:image:alt" content="Meili CDC: change data capture into Meilisearch">
<meta property="article:modified_time" content="${updated}">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="${escHtml(title)}">
<meta name="twitter:description" content="${escHtml(p.description)}">
<meta name="twitter:image" content="${OG_IMAGE}">
<link rel="icon" href="assets/favicon.svg" type="image/svg+xml">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&amp;family=JetBrains+Mono:wght@400;500;600&amp;display=swap">
<link rel="stylesheet" href="assets/guide.css">
<script>(function(){try{var t=localStorage.getItem('mcdc-guide-theme');if(t==='light'||t==='dark')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
<script src="assets/guide.js" defer></script>
<script type="application/ld+json">
${jsonLd(ld)}
</script>`;
}

function topbar() {
  return `<header class="topbar">
<div class="topbar-inner">
<button type="button" class="icon-btn menu-btn" aria-controls="sidebar" aria-expanded="false" aria-label="Open navigation">${icon.menu}</button>
<a class="brand" href="../" aria-label="Meili CDC home"><img src="assets/favicon.svg" alt="" width="30" height="30"><span>Meili CDC</span></a>
<a class="brand-docs" href="index.html">Docs</a>
<div class="search" role="search">
<button type="button" class="icon-btn search-toggle" aria-controls="search-input" aria-expanded="false" aria-label="Search the guide">${icon.search}</button>
<div class="search-box">${icon.search}<label class="sr-only" for="search-input">Search the guide</label><input id="search-input" type="search" placeholder="Search the guide" autocomplete="off" spellcheck="false" role="combobox" aria-expanded="false" aria-controls="search-panel" aria-autocomplete="list"><kbd aria-hidden="true">/</kbd></div>
<div class="search-panel" id="search-panel" hidden></div>
</div>
<div class="top-actions">
<a class="top-link hide-sm" href="../">Home</a>
<a class="top-link hide-xs" href="../#download">Download</a>
<button type="button" class="icon-btn theme-btn" aria-label="Toggle dark theme" aria-pressed="false">${icon.moon}${icon.sun}</button>
<a class="icon-btn" href="${REPO}" aria-label="meilisearch-cdc on GitHub" rel="noopener">${icon.github}</a>
</div>
</div>
</header>`;
}

function sidebar(p) {
  const secs = NAV.sections.map((s, i) => `<div class="nav-section"><p id="nav-s${i}">${escHtml(s.title)}</p><ul aria-labelledby="nav-s${i}">
${s.pages.map((x) => `<li><a href="${x.file}"${x.file === p.file ? ' aria-current="page"' : ''}>${escHtml(x.title)}</a></li>`).join('\n')}
</ul></div>`).join('\n');
  return `<nav class="sidebar" id="sidebar" aria-label="Guide">
${secs}
</nav>`;
}

function crumbsFor(p) {
  const out = [{ name: 'Meili CDC', href: '../', abs: SITE }, { name: 'Docs', href: 'index.html', abs: GUIDE + 'index.html' }];
  if (p.file === 'index.html') return out;
  if (p.sectionFirst !== 'index.html' && p.sectionFirst !== p.file) out.push({ name: p.section, href: p.sectionFirst, abs: GUIDE + p.sectionFirst });
  out.push({ name: p.title, href: p.file, abs: GUIDE + p.file });
  return out;
}

function breadcrumbs(crumbs) {
  return `<nav class="breadcrumbs" aria-label="Breadcrumb"><ol>
${crumbs.map((c, i) => i === crumbs.length - 1 ? `<li><span aria-current="page">${escHtml(c.name)}</span></li>` : `<li><a href="${c.href}">${escHtml(c.name)}</a></li>`).join('\n')}
</ol></nav>`;
}

function pager(i) {
  const prev = pages[i - 1], next = pages[i + 1];
  return `<nav class="pager" aria-label="Previous and next page">
${prev ? `<a class="prev" href="${prev.file}" rel="prev"><span class="p-label">Previous</span><span class="p-title">${escHtml(prev.title)}</span></a>` : ''}
${next ? `<a class="next" href="${next.file}" rel="next"><span class="p-label">Next</span><span class="p-title">${escHtml(next.title)}</span></a>` : ''}
</nav>`;
}

function footer(p) {
  return `<footer class="site-footer">
<p>Meili CDC user guide · last updated ${escHtml(p.updated || NAV.updated)} · <a href="${REPO}/edit/main/docs/guide/${p.file}" rel="noopener">Edit this page on GitHub</a></p>
<p>meilisearch-cdc is open source. Meilisearch is a trademark of Meili SAS; this project is not affiliated with it.</p>
</footer>`;
}

// ---------- build ----------
const idx = { pages: [], entries: [] };
const sitemap = [];
let problems = 0;
pages.forEach((p, i) => {
  const path = join(ROOT, p.file);
  if (!existsSync(path)) { console.error(`missing page ${p.file}`); problems++; return; }
  const src = readFileSync(path, 'utf8');
  const m = /<article class="doc"[^>]*>[\s\S]*<\/article>/.exec(src);
  if (!m) { console.error(`${p.file}: no <article class="doc">`); problems++; return; }
  let article = m[0];

  // Stable ids for headings.
  const used = new Set();
  for (const mm of article.matchAll(/\sid="([^"]+)"/g)) used.add(mm[1]);
  article = article.replace(/<(h[234])(\s[^>]*)?>([\s\S]*?)<\/\1>/g, (all, tag, attrs = '', inner) => {
    if (/\sid="/.test(attrs)) return all;
    const base = slug(toText(inner));
    let id = base, n = 2;
    while (used.has(id)) id = `${base}-${n++}`;
    used.add(id);
    return `<${tag} id="${id}"${attrs}>${inner}</${tag}>`;
  });
  const h1s = article.match(/<h1[\s>]/g) || [];
  if (h1s.length !== 1) { console.error(`${p.file}: expected exactly one <h1>, found ${h1s.length}`); problems++; }
  for (const img of article.matchAll(/<img\b[^>]*>/g)) {
    if (!/\salt="[^"]{8,}"/.test(img[0])) { console.error(`${p.file}: image without a descriptive alt: ${img[0].slice(0, 80)}`); problems++; }
  }

  const crumbs = crumbsFor(p);
  const html = `<!doctype html>
<html lang="en">
<head>
${head(p, crumbs)}
</head>
<body>
<a class="skip-link" href="#content">Skip to content</a>
${topbar()}
<div class="layout">
${sidebar(p)}
<main id="content" class="content" tabindex="-1">
${breadcrumbs(crumbs)}
${article}
${pager(i)}
${footer(p)}
</main>
<aside class="toc" aria-label="On this page"></aside>
</div>
</body>
</html>
`;
  writeFileSync(path, html);

  // Search entries: the page top, then one per h2/h3 section.
  const pi = idx.pages.length;
  idx.pages.push({ f: p.file, t: p.title, s: p.section });
  const body = article.replace(/<h1[\s\S]*?<\/h1>/, '');
  const parts = body.split(/(?=<h[23][\s>])/);
  parts.forEach((part, k) => {
    const hm = /^<h([23])[^>]*id="([^"]+)"[^>]*>([\s\S]*?)<\/h\1>/.exec(part);
    const h = hm ? toText(hm[3]) : '';
    const a = hm ? hm[2] : '';
    const x = toText(hm ? part.slice(hm[0].length) : part).slice(0, 5000);
    if (k === 0 && !hm) idx.entries.push({ p: pi, h: '', a: '', x: (p.description + ' ' + x).slice(0, 5000) });
    else if (h || x) idx.entries.push({ p: pi, h, a, x });
  });
  sitemap.push({ path: `guide/${p.file}`, title: p.title, description: p.description, lastmod: p.updated || NAV.updated });
});

const json = JSON.stringify(idx);
writeFileSync(join(ROOT, 'assets/search-index.json'), json);
writeFileSync(join(ROOT, 'assets/search-index.js'), `/* Generated by build-search-index.mjs; do not edit. */\nwindow.GUIDE_SEARCH_INDEX=${json};\n`);
writeFileSync(join(ROOT, 'sitemap-pages.json'), JSON.stringify(sitemap, null, 2) + '\n');
console.log(`${pages.length} pages, ${idx.entries.length} search entries, ${(json.length / 1024).toFixed(0)} KiB index`);
if (problems) { console.error(`${problems} problem(s)`); process.exit(1); }
