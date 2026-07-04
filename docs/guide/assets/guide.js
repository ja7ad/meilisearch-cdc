/* Meili CDC user guide: theme, navigation drawer, table of contents with
 * scroll-spy, heading anchors, copy buttons, syntax highlighting and
 * client-side search. Plain script, no dependencies, works from file://. */
(function () {
  'use strict';
  var doc = document.documentElement;
  var THEME_KEY = 'mcdc-guide-theme';

  function lsGet(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
  function lsSet(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* storage unavailable */ } }
  function $(sel, root) { return (root || document).querySelector(sel); }
  function $all(sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); }
  function esc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'); }

  /* ---------- theme ---------- */
  function currentTheme() {
    var t = doc.getAttribute('data-theme');
    if (t === 'light' || t === 'dark') return t;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  function initTheme() {
    var btn = $('.theme-btn');
    if (!btn) return;
    var sync = function () {
      var dark = currentTheme() === 'dark';
      btn.setAttribute('aria-pressed', dark ? 'true' : 'false');
      btn.setAttribute('aria-label', dark ? 'Switch to light theme' : 'Switch to dark theme');
      btn.title = dark ? 'Light theme' : 'Dark theme';
    };
    btn.addEventListener('click', function () {
      var next = currentTheme() === 'dark' ? 'light' : 'dark';
      doc.setAttribute('data-theme', next);
      lsSet(THEME_KEY, next);
      sync();
    });
    sync();
  }

  /* ---------- sidebar drawer ---------- */
  function initDrawer() {
    var btn = $('.menu-btn');
    var side = $('#sidebar');
    if (!btn || !side) return;
    var scrim = document.createElement('div');
    scrim.className = 'scrim';
    scrim.hidden = true;
    document.body.appendChild(scrim);
    var mq = window.matchMedia('(max-width: 920px)');
    function setOpen(open, focusBack) {
      document.body.classList.toggle('nav-open', open);
      btn.setAttribute('aria-expanded', open ? 'true' : 'false');
      scrim.hidden = !open;
      if (open) {
        var cur = $('[aria-current="page"]', side) || $('a', side);
        if (cur) { cur.scrollIntoView({ block: 'center' }); cur.focus({ preventScroll: true }); }
      } else if (focusBack) btn.focus();
    }
    btn.addEventListener('click', function () { setOpen(!document.body.classList.contains('nav-open'), false); });
    scrim.addEventListener('click', function () { setOpen(false, true); });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && document.body.classList.contains('nav-open')) setOpen(false, true);
    });
    side.addEventListener('click', function (e) { if (e.target.closest('a') && mq.matches) setOpen(false, false); });
    var onChange = function () { if (!mq.matches) setOpen(false, false); };
    if (mq.addEventListener) mq.addEventListener('change', onChange); else mq.addListener(onChange);
    // Keep the current page visible in the desktop sidebar.
    var cur = $('[aria-current="page"]', side);
    if (cur && !mq.matches) {
      var r = cur.getBoundingClientRect();
      if (r.bottom > side.clientHeight) side.scrollTop = cur.offsetTop - side.clientHeight / 3;
    }
  }

  /* ---------- headings, anchors and table of contents ---------- */
  function slug(s) {
    return s.toLowerCase().replace(/<[^>]+>/g, '').replace(/[^a-z0-9\s_-]/g, '').trim().replace(/\s+/g, '-').slice(0, 64) || 'section';
  }
  function initHeadings() {
    var article = $('.doc');
    if (!article) return [];
    var used = {};
    $all('[id]').forEach(function (el) { used[el.id] = true; });
    var heads = $all('h2, h3', article);
    heads.forEach(function (h) {
      if (!h.id) {
        var base = slug(h.textContent), id = base, i = 2;
        while (used[id]) id = base + '-' + (i++);
        h.id = id; used[id] = true;
      }
      if (!$('.anchor', h)) {
        var a = document.createElement('a');
        a.className = 'anchor';
        a.href = '#' + h.id;
        a.setAttribute('aria-label', 'Link to the section: ' + h.textContent.trim());
        a.textContent = '#';
        h.appendChild(a);
      }
    });
    $all('h4', article).forEach(function (h) { if (!h.id) { var id = slug(h.textContent); if (!used[id]) { h.id = id; used[id] = true; } } });
    return heads;
  }
  function headingText(h) {
    var c = h.cloneNode(true);
    $all('.anchor', c).forEach(function (a) { a.remove(); });
    return c.textContent.trim();
  }
  function initToc(heads) {
    var aside = $('.toc');
    var article = $('.doc');
    if (!heads.length || !article) { if (aside) aside.hidden = true; return; }
    var items = heads.map(function (h) {
      return '<li class="' + (h.tagName === 'H3' ? 'l3' : 'l2') + '"><a href="#' + esc(h.id) + '">' + esc(headingText(h)) + '</a></li>';
    }).join('');
    if (aside) aside.innerHTML = '<p id="toc-title">On this page</p><nav aria-labelledby="toc-title"><ul>' + items + '</ul></nav>';
    // Collapsible copy for narrower screens, right below the h1/lead.
    var det = document.createElement('details');
    det.className = 'toc-inline';
    det.innerHTML = '<summary>On this page</summary><nav aria-label="On this page"><ul>' + items + '</ul></nav>';
    var anchorEl = $('.lead', article) || $('h1', article);
    if (anchorEl && anchorEl.parentNode === article) anchorEl.insertAdjacentElement('afterend', det);
    else article.insertBefore(det, article.firstChild);
    if (!aside || !('IntersectionObserver' in window)) return;
    var links = {};
    $all('a', aside).forEach(function (a) { links[a.getAttribute('href').slice(1)] = a; });
    var visible = {};
    var setActive = function (id) {
      Object.keys(links).forEach(function (k) { links[k].classList.toggle('active', k === id); });
    };
    var obs = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) { visible[e.target.id] = e.isIntersecting; });
      var first = null;
      for (var i = 0; i < heads.length; i++) { if (visible[heads[i].id]) { first = heads[i].id; break; } }
      if (!first) {
        // Between headings: the last one above the viewport.
        for (var j = heads.length - 1; j >= 0; j--) { if (heads[j].getBoundingClientRect().top < 120) { first = heads[j].id; break; } }
      }
      if (first) setActive(first);
    }, { rootMargin: '-64px 0px -55% 0px', threshold: 0 });
    heads.forEach(function (h) { obs.observe(h); });
  }

  /* ---------- tables ---------- */
  function initTables() {
    $all('.doc table').forEach(function (t) {
      if (t.parentElement && t.parentElement.classList.contains('table-wrap')) {
        t.parentElement.tabIndex = 0;
        if (!t.parentElement.hasAttribute('role')) t.parentElement.setAttribute('role', 'region');
        var cap = t.querySelector('caption');
        var label = cap ? cap.textContent : (t.querySelector('th') ? 'Table: ' + t.querySelector('th').textContent : 'Table');
        if (!t.parentElement.hasAttribute('aria-label')) t.parentElement.setAttribute('aria-label', label);
        return;
      }
      var w = document.createElement('div');
      w.className = 'table-wrap'; w.tabIndex = 0; w.setAttribute('role', 'region'); w.setAttribute('aria-label', 'Table');
      t.parentNode.insertBefore(w, t); w.appendChild(t);
    });
  }

  /* ---------- syntax highlighting ---------- */
  var SH = 'if then else elif fi for in do done while case esac function return export local set unset echo printf cd mkdir chmod chown cp mv rm ln sudo cat curl tar unzip sha256sum shasum docker kubectl systemctl useradd install go make node npm npx git exec source read test true false';
  var SQL = 'select from where and or not null is in as on join left right inner outer insert into values update set delete create alter drop table index database schema user role grant revoke to with replica identity full default publication for all tables system wal_level exec use go begin end if exists primary key references cluster setting setting show identified by password login replication slave client super on schema';
  function words(list) { return new RegExp('\\b(?:' + list.split(/\s+/).join('|') + ')\\b', 'y'); }
  var LANGS = {
    bash: [
      [/(?<=^|[\s;])#.*/y, 'c'], [/"(?:\\.|[^"\\])*"/y, 's'], [/'[^']*'/y, 's'],
      [/\$\{[^}]*\}|\$[A-Za-z_][\w]*|\$\(|\$[0-9?@#!]/y, 'v'],
      [/(?<=^|\s)--?[A-Za-z][\w-]*/y, 'p'], [words(SH), 'k'], [/\b\d+\b/y, 'n']
    ],
    powershell: [
      [/#.*/y, 'c'], [/"(?:`.|[^"`])*"/y, 's'], [/'[^']*'/y, 's'], [/\$[A-Za-z_][\w:]*/y, 'v'],
      [/\b[A-Z][a-z]+-[A-Z][A-Za-z]+\b/y, 'k'], [/(?<=\s)-[A-Za-z]+/y, 'p'], [/\b\d+\b/y, 'n']
    ],
    yaml: [
      [/(?<=^|\s)#.*/y, 'c'],
      [/(?<=^|\n)[ \t]*(?:- +)?[A-Za-z0-9_.$\/"-]+(?=[ \t]*:(?:\s|$))/y, 'p'],
      [/"(?:\\.|[^"\\])*"/y, 's'], [/'[^']*'/y, 's'], [/\$\{[^}]*\}/y, 'v'],
      [/\b(?:true|false|null|yes|no)\b/y, 'k'], [/(?<=[\s:\[,])-?\d+(?:\.\d+)?(?:ms|s|m|h|Mi|Gi)?\b/y, 'n'], [/[{}\[\],|>]/y, 'o']
    ],
    json: [
      [/"(?:\\.|[^"\\])*"(?=\s*:)/y, 'p'], [/"(?:\\.|[^"\\])*"/y, 's'], [/\/\/.*/y, 'c'],
      [/\b(?:true|false|null)\b/y, 'k'], [/-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b/y, 'n'], [/[{}\[\],:]/y, 'o']
    ],
    sql: [
      [/--.*/y, 'c'], [/\/\*[\s\S]*?\*\//y, 'c'], [/'(?:''|[^'])*'/y, 's'], [/N'(?:''|[^'])*'/y, 's'],
      [/"[^"]*"/y, 'v'], [/@@?[A-Za-z_][\w.]*/y, 'v'], [new RegExp(words(SQL).source, 'yi'), 'k'], [/\b\d+\b/y, 'n']
    ],
    nginx: [
      [/#.*/y, 'c'], [/"(?:\\.|[^"\\])*"/y, 's'], [/'[^']*'/y, 's'], [/\$[A-Za-z_]\w*/y, 'v'],
      [/\{[a-z_]+\}/y, 'v'],
      [/(?<=^|\n|\{)[ \t]*[a-z_][\w.]*(?=[ \t])/y, 'k'], [/\b\d+[smhkKmMgG]?\b/y, 'n'], [/[{};]/y, 'o']
    ],
    ini: [
      [/(?<=^|\n)[ \t]*[#;].*/y, 'c'], [/(?<=^|\n)[ \t]*\[[^\]\n]+\]/y, 'k'],
      [/(?<=^|\n)[ \t]*[A-Za-z_][\w.-]*(?=[ \t]*=)/y, 'p'], [/"(?:\\.|[^"\\])*"/y, 's'], [/\$\{[^}]*\}|\$[A-Za-z_]\w*/y, 'v']
    ],
    xml: [
      [/<!--[\s\S]*?-->/y, 'c'], [/<\/?[A-Za-z][\w.:-]*/y, 'k'], [/\/?>/y, 'k'], [/"[^"]*"/y, 's'], [/\b[A-Za-z_:][\w.:-]*(?==)/y, 'p']
    ],
    text: []
  };
  LANGS.sh = LANGS.shell = LANGS.console = LANGS.bash;
  LANGS.yml = LANGS.yaml;
  LANGS.env = LANGS.systemd = LANGS.ini;
  LANGS.caddy = LANGS.caddyfile = LANGS.nginx;
  LANGS.plist = LANGS.html = LANGS.xml;
  LANGS.ps1 = LANGS.pwsh = LANGS.powershell;
  function highlight(src, rules) {
    if (!rules || !rules.length) return esc(src);
    var out = '', plain = '', i = 0, n = src.length;
    outer:
    while (i < n) {
      for (var r = 0; r < rules.length; r++) {
        var re = rules[r][0];
        re.lastIndex = i;
        var m = re.exec(src);
        if (m && m[0].length) {
          if (plain) { out += esc(plain); plain = ''; }
          out += '<span class="tok-' + rules[r][1] + '">' + esc(m[0]) + '</span>';
          i += m[0].length;
          continue outer;
        }
      }
      // Skip ahead over an identifier so keywords only match whole words.
      var w = /[A-Za-z0-9_]+/y; w.lastIndex = i;
      var wm = w.exec(src);
      if (wm) { plain += wm[0]; i += wm[0].length; } else { plain += src[i]; i++; }
    }
    return out + esc(plain);
  }
  function initCode() {
    $all('.doc pre > code').forEach(function (code) {
      var pre = code.parentElement;
      var m = /language-([\w-]+)/.exec(code.className);
      var lang = m ? m[1].toLowerCase() : 'text';
      try { code.innerHTML = highlight(code.textContent, LANGS[lang]); } catch (e) { /* keep plain text */ }
      if (!pre.parentElement.classList.contains('code-block')) {
        var wrap = document.createElement('div');
        wrap.className = 'code-block';
        pre.parentNode.insertBefore(wrap, pre);
        wrap.appendChild(pre);
      }
      pre.tabIndex = 0;
      var wrapEl = pre.parentElement;
      if (lang !== 'text' && !$('.code-lang', wrapEl)) {
        var lab = document.createElement('span');
        lab.className = 'code-lang'; lab.setAttribute('aria-hidden', 'true');
        lab.textContent = lang;
        wrapEl.appendChild(lab);
      }
      var btn = document.createElement('button');
      btn.type = 'button'; btn.className = 'copy-btn'; btn.textContent = 'Copy';
      btn.setAttribute('aria-label', 'Copy code to clipboard');
      btn.addEventListener('click', function () {
        var text = code.textContent;
        var done = function () {
          btn.textContent = 'Copied'; btn.classList.add('copied');
          setTimeout(function () { btn.textContent = 'Copy'; btn.classList.remove('copied'); }, 1600);
        };
        if (navigator.clipboard && window.isSecureContext) {
          navigator.clipboard.writeText(text).then(done, function () { fallbackCopy(text); done(); });
        } else { fallbackCopy(text); done(); }
      });
      wrapEl.appendChild(btn);
    });
  }
  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text; ta.setAttribute('readonly', '');
    ta.style.position = 'fixed'; ta.style.top = '-1000px'; ta.style.opacity = '0';
    document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); } catch (e) { /* ignore */ }
    ta.remove();
  }

  /* ---------- search ---------- */
  var index = null, loading = null;
  function loadIndex() {
    if (index) return Promise.resolve(index);
    if (loading) return loading;
    var base = (document.querySelector('script[src$="guide.js"]') || {}).src || '';
    base = base.replace(/guide\.js(\?.*)?$/, '');
    var viaScript = function () {
      return new Promise(function (resolve, reject) {
        if (window.GUIDE_SEARCH_INDEX) return resolve(window.GUIDE_SEARCH_INDEX);
        var s = document.createElement('script');
        s.src = base + 'search-index.js';
        s.onload = function () { window.GUIDE_SEARCH_INDEX ? resolve(window.GUIDE_SEARCH_INDEX) : reject(new Error('empty index')); };
        s.onerror = function () { reject(new Error('index not found')); };
        document.head.appendChild(s);
      });
    };
    var p = (location.protocol === 'file:' || !window.fetch) ? viaScript()
      : fetch(base + 'search-index.json').then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); }).catch(viaScript);
    loading = p.then(function (data) {
      data.entries.forEach(function (e) { e.lt = (e.h + ' ' + e.x).toLowerCase(); e.lh = e.h.toLowerCase(); });
      data.pages.forEach(function (pg) { pg.lt = pg.t.toLowerCase(); });
      index = data; return data;
    });
    loading.catch(function () { loading = null; });
    return loading;
  }
  function tokens(q) {
    return q.toLowerCase().replace(/[^\w\s.$\/@:-]/g, ' ').split(/\s+/).filter(function (t) { return t.length > 0; }).slice(0, 8);
  }
  function countOcc(hay, t) { var c = 0, i = hay.indexOf(t); while (i !== -1 && c < 20) { c++; i = hay.indexOf(t, i + t.length); } return c; }
  function search(q) {
    var ts = tokens(q);
    if (!ts.length || !index) return [];
    var res = [];
    index.entries.forEach(function (e) {
      var page = index.pages[e.p];
      var score = 0;
      for (var i = 0; i < ts.length; i++) {
        var t = ts[i];
        var inText = countOcc(e.lt, t);
        var inPage = page.lt.indexOf(t) !== -1;
        if (!inText && !inPage) return;
        score += Math.min(inText, 6);
        if (e.lh.indexOf(t) !== -1) score += 8;
        if (inPage) score += 5;
        if (new RegExp('(^|[^a-z0-9])' + t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '([^a-z0-9]|$)').test(e.lh)) score += 4;
      }
      if (!e.a) score += 2; // page top
      res.push({ e: e, page: page, score: score });
    });
    res.sort(function (a, b) { return b.score - a.score; });
    // At most three hits per page so one page does not crowd the list.
    var per = {}, out = [];
    for (var k = 0; k < res.length && out.length < 25; k++) {
      var key = res[k].e.p;
      per[key] = (per[key] || 0) + 1;
      if (per[key] <= 3) out.push(res[k]);
    }
    return out;
  }
  function mark(text, ts) {
    var s = esc(text);
    ts.forEach(function (t) {
      var re = new RegExp('(' + esc(t).replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + ')', 'gi');
      s = s.replace(re, '\u0001$1\u0002');
    });
    return s.replace(/\u0001/g, '<mark>').replace(/\u0002/g, '</mark>');
  }
  function snippet(text, ts) {
    var low = text.toLowerCase(), at = -1;
    for (var i = 0; i < ts.length && at === -1; i++) at = low.indexOf(ts[i]);
    if (at === -1) at = 0;
    var start = Math.max(0, at - 60), end = Math.min(text.length, at + 120);
    if (start > 0) { var sp = text.indexOf(' ', start); if (sp !== -1 && sp < at) start = sp + 1; }
    return (start > 0 ? '… ' : '') + text.slice(start, end).trim() + (end < text.length ? ' …' : '');
  }
  function initSearch() {
    var input = $('#search-input');
    var panel = $('#search-panel');
    var toggle = $('.search-toggle');
    if (!input || !panel) return;
    var list = document.createElement('ul');
    list.className = 'search-results'; list.id = 'search-results'; list.setAttribute('role', 'listbox');
    list.setAttribute('aria-label', 'Search results');
    var status = document.createElement('p');
    status.className = 'search-status'; status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
    panel.appendChild(status); panel.appendChild(list);
    var sel = -1, current = [];
    var pageBase = (document.querySelector('script[src$="guide.js"]') || {}).src || '';
    pageBase = pageBase.replace(/assets\/guide\.js(\?.*)?$/, '');

    function open() { panel.hidden = false; input.setAttribute('aria-expanded', 'true'); }
    function close() { panel.hidden = true; input.setAttribute('aria-expanded', 'false'); input.removeAttribute('aria-activedescendant'); sel = -1; }
    function select(i) {
      var items = $all('li', list);
      items.forEach(function (li, j) { li.setAttribute('aria-selected', j === i ? 'true' : 'false'); });
      sel = i;
      if (items[i]) { input.setAttribute('aria-activedescendant', items[i].id); items[i].scrollIntoView({ block: 'nearest' }); }
    }
    function render() {
      var q = input.value.trim();
      if (!q) { list.innerHTML = ''; status.textContent = 'Type to search the guide.'; current = []; return; }
      if (!index) { status.textContent = 'Loading the search index…'; return; }
      var ts = tokens(q);
      current = search(q);
      status.textContent = current.length ? current.length + (current.length === 25 ? '+' : '') + ' result' + (current.length === 1 ? '' : 's') : 'No results for “' + q + '”.';
      list.innerHTML = current.map(function (r, i) {
        var href = pageBase + r.page.f + (r.e.a ? '#' + r.e.a : '');
        var title = r.e.h || r.page.t;
        var crumb = r.page.s + ' › ' + r.page.t;
        return '<li role="option" id="sr-' + i + '" aria-selected="false"><a href="' + esc(href) + '" tabindex="-1">' +
          '<span class="r-title">' + mark(title, ts) + '</span>' +
          '<span class="r-page">' + esc(crumb) + '</span>' +
          '<span class="r-snippet">' + mark(snippet(r.e.x, ts), ts) + '</span></a></li>';
      }).join('');
      sel = -1;
    }
    var timer = null;
    input.addEventListener('input', function () {
      open();
      clearTimeout(timer);
      timer = setTimeout(function () {
        if (index) render();
        else { render(); loadIndex().then(render, function () { status.textContent = 'The search index could not be loaded.'; }); }
      }, 60);
    });
    input.addEventListener('focus', function () {
      loadIndex().then(function () { if (input.value.trim()) render(); }, function () {});
      if (input.value.trim()) open();
    });
    input.addEventListener('keydown', function (e) {
      var n = current.length;
      if (e.key === 'ArrowDown') { e.preventDefault(); open(); if (n) select((sel + 1) % n); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); if (n) select(sel <= 0 ? n - 1 : sel - 1); }
      else if (e.key === 'Enter') {
        var target = current[sel >= 0 ? sel : 0];
        if (target) { e.preventDefault(); var a = $all('li a', list)[sel >= 0 ? sel : 0]; if (a) { close(); location.href = a.href; } }
      } else if (e.key === 'Escape') {
        if (!panel.hidden) { close(); } else { input.value = ''; input.blur(); document.body.classList.remove('search-open'); }
      }
    });
    list.addEventListener('mousedown', function (e) { if (e.target.closest('a')) e.preventDefault(); });
    list.addEventListener('click', function (e) { var a = e.target.closest('a'); if (a) { close(); location.href = a.href; } });
    document.addEventListener('click', function (e) {
      if (!e.target.closest('.search')) { close(); }
    });
    if (toggle) toggle.addEventListener('click', function () {
      var on = !document.body.classList.contains('search-open');
      document.body.classList.toggle('search-open', on);
      toggle.setAttribute('aria-expanded', on ? 'true' : 'false');
      if (on) input.focus(); else close();
    });
    document.addEventListener('keydown', function (e) {
      if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
      var t = e.target;
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
      e.preventDefault();
      document.body.classList.add('search-open');
      if (toggle) toggle.setAttribute('aria-expanded', 'true');
      input.focus(); input.select();
    });
    // Prefill from ?q= (handy for links to a search).
    var m = /[?&]q=([^&]+)/.exec(location.search);
    if (m) { input.value = decodeURIComponent(m[1].replace(/\+/g, ' ')); open(); loadIndex().then(render); }
  }

  function init() {
    initTheme();
    initDrawer();
    initTables();
    initCode();
    var heads = initHeadings();
    initToc(heads);
    initSearch();
    // Re-apply a hash target now that ids exist.
    if (location.hash) { var t = document.getElementById(decodeURIComponent(location.hash.slice(1))); if (t) t.scrollIntoView(); }
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init); else init();
})();
