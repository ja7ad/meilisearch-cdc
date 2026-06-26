/* Meili CDC landing page: theme, nav, reveal animations, tour, lightbox,
   copy buttons, tabs, GitHub stars, release assets and OS detection.
   Plain ES2017, no dependencies. */
(function () {
  'use strict';

  var REPO = 'ja7ad/meilisearch-cdc';
  var GH = 'https://github.com/' + REPO;
  var API = 'https://api.github.com/repos/' + REPO;
  var RELEASES_LATEST = GH + '/releases/latest';
  var IMAGE = 'ghcr.io/ja7ad/meilisearch-cdc:latest';

  var doc = document.documentElement;
  var $ = function (sel, root) { return (root || document).querySelector(sel); };
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };
  var reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  function store(key, value) {
    try {
      if (value === undefined) return localStorage.getItem(key);
      if (value === null) localStorage.removeItem(key); else localStorage.setItem(key, value);
    } catch (e) { /* storage unavailable (private mode, file://) */ }
    return null;
  }
  function sessionCache(key, value) {
    try {
      if (value === undefined) {
        var raw = sessionStorage.getItem(key);
        if (!raw) return null;
        var o = JSON.parse(raw);
        return (Date.now() - o.t < 10 * 60 * 1000) ? o.v : null;
      }
      sessionStorage.setItem(key, JSON.stringify({ t: Date.now(), v: value }));
    } catch (e) { /* ignore */ }
    return null;
  }

  /* ---------- theme ---------- */
  var themeBtn = $('#theme-toggle');
  function currentTheme() {
    var t = doc.getAttribute('data-theme');
    if (t) return t;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  function syncThemeButton() {
    if (!themeBtn) return;
    themeBtn.setAttribute('aria-label', currentTheme() === 'dark' ? 'Switch to light theme' : 'Switch to dark theme');
  }
  if (themeBtn) {
    themeBtn.addEventListener('click', function () {
      var next = currentTheme() === 'dark' ? 'light' : 'dark';
      doc.setAttribute('data-theme', next);
      store('mcdc-theme', next);
      syncThemeButton();
    });
    syncThemeButton();
    if (window.matchMedia) {
      var mq = window.matchMedia('(prefers-color-scheme: dark)');
      if (mq.addEventListener) mq.addEventListener('change', syncThemeButton);
    }
  }

  /* ---------- nav ---------- */
  var nav = $('.nav');
  var menuBtn = $('#menu-btn');
  var links = $('#nav-links');
  function setMenu(open) {
    if (!menuBtn) return;
    menuBtn.setAttribute('aria-expanded', open ? 'true' : 'false');
    menuBtn.setAttribute('aria-label', open ? 'Close menu' : 'Open menu');
    links.classList.toggle('open', open);
  }
  if (menuBtn) {
    menuBtn.addEventListener('click', function () { setMenu(menuBtn.getAttribute('aria-expanded') !== 'true'); });
    links.addEventListener('click', function (e) { if (e.target.closest('a')) setMenu(false); });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && menuBtn.getAttribute('aria-expanded') === 'true') { setMenu(false); menuBtn.focus(); }
    });
    document.addEventListener('click', function (e) {
      if (menuBtn.getAttribute('aria-expanded') === 'true' && !e.target.closest('.nav')) setMenu(false);
    });
    window.addEventListener('resize', function () { if (window.innerWidth > 1080) setMenu(false); });
  }
  function onScroll() { if (nav) nav.classList.toggle('scrolled', window.scrollY > 8); }
  window.addEventListener('scroll', onScroll, { passive: true });
  onScroll();

  // Highlight the nav link of the section in view.
  if ('IntersectionObserver' in window) {
    var navMap = {};
    $$('.nav-links a[href^="#"]').forEach(function (a) { navMap[a.getAttribute('href').slice(1)] = a; });
    var secObs = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        var a = navMap[en.target.id];
        if (!a) return;
        if (en.isIntersecting) {
          Object.keys(navMap).forEach(function (k) { navMap[k].classList.remove('active'); navMap[k].removeAttribute('aria-current'); });
          a.classList.add('active'); a.setAttribute('aria-current', 'true');
        }
      });
    }, { rootMargin: '-45% 0px -50% 0px' });
    Object.keys(navMap).forEach(function (id) { var s = document.getElementById(id); if (s) secObs.observe(s); });
  }

  /* ---------- reveal on scroll ---------- */
  var reveals = $$('.reveal');
  if (!('IntersectionObserver' in window) || reduceMotion) {
    reveals.forEach(function (el) { el.classList.add('in'); });
  } else {
    var revObs = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (en.isIntersecting) { en.target.classList.add('in'); revObs.unobserve(en.target); }
      });
    }, { rootMargin: '0px 0px -8% 0px', threshold: 0.08 });
    reveals.forEach(function (el) { revObs.observe(el); });
  }

  /* ---------- counters ---------- */
  var counters = $$('[data-count]');
  function runCounter(el) {
    var target = parseFloat(el.getAttribute('data-count'));
    if (reduceMotion) { el.textContent = String(target); return; }
    var start = null, dur = 1400;
    function step(ts) {
      if (start === null) start = ts;
      var p = Math.min(1, (ts - start) / dur);
      var eased = 1 - Math.pow(1 - p, 3);
      el.textContent = String(Math.round(target * eased));
      if (p < 1) requestAnimationFrame(step);
    }
    requestAnimationFrame(step);
  }
  if ('IntersectionObserver' in window && !reduceMotion) {
    counters.forEach(function (el) { el.textContent = '0'; });
    var cObs = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) { if (en.isIntersecting) { runCounter(en.target); cObs.unobserve(en.target); } });
    }, { threshold: 0.4 });
    counters.forEach(function (el) { cObs.observe(el); });
  }

  /* ---------- architecture animation ---------- */
  $$('svg.arch').forEach(function (svg) {
    if (typeof svg.pauseAnimations !== 'function') return;
    if (reduceMotion) { svg.pauseAnimations(); return; }
    if ('IntersectionObserver' in window) {
      new IntersectionObserver(function (entries) {
        entries.forEach(function (en) { if (en.isIntersecting) svg.unpauseAnimations(); else svg.pauseAnimations(); });
      }).observe(svg);
    }
  });

  /* ---------- tour ---------- */
  var steps = $$('.tour-step');
  var tourImgs = $$('.tour-img');
  var tourUrl = $('#tour-url');
  var dots = $('#tour-dots');
  if (dots) {
    steps.forEach(function () { dots.appendChild(document.createElement('span')); });
  }
  function activate(i) {
    steps.forEach(function (s, j) { s.classList.toggle('is-active', i === j); });
    tourImgs.forEach(function (im, j) { im.classList.toggle('is-active', i === j); });
    if (dots) $$('span', dots).forEach(function (d, j) { d.classList.toggle('on', i === j); });
    if (tourUrl && steps[i]) tourUrl.textContent = steps[i].getAttribute('data-url') || 'localhost:8080';
    // Warm the next screenshot so the swap is instant.
    var next = tourImgs[i + 1] && $('img', tourImgs[i + 1]);
    if (next) next.loading = 'eager';
  }
  if (steps.length) {
    activate(0);
    if ('IntersectionObserver' in window) {
      var tObs = new IntersectionObserver(function (entries) {
        entries.forEach(function (en) {
          if (en.isIntersecting) activate(parseInt(en.target.getAttribute('data-step'), 10));
        });
      }, { rootMargin: '-48% 0px -48% 0px' });
      steps.forEach(function (s) { tObs.observe(s); });
    }
  }

  /* ---------- toast + copy ---------- */
  var toast = $('#toast'), toastTimer;
  function showToast(msg) {
    if (!toast) return;
    toast.textContent = msg;
    toast.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { toast.classList.remove('show'); }, 1800);
  }
  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text; ta.setAttribute('readonly', '');
    ta.style.position = 'fixed'; ta.style.top = '-1000px'; ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
    document.body.removeChild(ta);
    return ok;
  }
  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).then(function () { return true; }, function () { return fallbackCopy(text); });
    }
    return Promise.resolve(fallbackCopy(text));
  }
  document.addEventListener('click', function (e) {
    var btn = e.target.closest('[data-copy-target]');
    if (!btn) return;
    var src = document.getElementById(btn.getAttribute('data-copy-target'));
    if (!src) return;
    copyText(src.textContent.replace(/ /g, ' ')).then(function (ok) {
      var label = $('.copy-l', btn);
      btn.classList.toggle('copied', !!ok);
      if (label) label.textContent = ok ? 'Copied' : 'Press ⌘/Ctrl+C';
      showToast(ok ? 'Copied to clipboard' : 'Copy failed: select the text and copy it manually');
      setTimeout(function () { btn.classList.remove('copied'); if (label) label.textContent = 'Copy'; }, 1800);
    });
  });

  /* ---------- tabs ---------- */
  $$('[data-tabs]').forEach(function (root) {
    var tabs = $$('[role="tab"]', root);
    function select(tab, focus) {
      tabs.forEach(function (t) {
        var on = t === tab;
        t.setAttribute('aria-selected', on ? 'true' : 'false');
        t.tabIndex = on ? 0 : -1;
        var panel = document.getElementById(t.getAttribute('aria-controls'));
        if (panel) panel.hidden = !on;
      });
      if (focus) tab.focus();
    }
    tabs.forEach(function (t, i) {
      t.addEventListener('click', function () { select(t, false); });
      t.addEventListener('keydown', function (e) {
        var n = null;
        if (e.key === 'ArrowRight') n = tabs[(i + 1) % tabs.length];
        else if (e.key === 'ArrowLeft') n = tabs[(i - 1 + tabs.length) % tabs.length];
        else if (e.key === 'Home') n = tabs[0];
        else if (e.key === 'End') n = tabs[tabs.length - 1];
        if (n) { e.preventDefault(); select(n, true); }
      });
    });
  });

  /* ---------- lightbox ---------- */
  var lb = $('#lightbox');
  var lbImg = $('#lb-img'), lbCap = $('#lb-cap'), lbCount = $('#lb-count'), lbActual = $('#lb-actual');
  var items = [], lbIndex = 0, lastFocus = null;
  function indexOfSrc(src) { for (var k = 0; k < items.length; k++) if (items[k].src === src) return k; return -1; }
  $$('[data-lb]').forEach(function (el) {
    $$('img', el).forEach(function (img, n) {
      var src = img.getAttribute('src');
      if (!src || indexOfSrc(src) >= 0) return;
      items.push({ src: src, alt: img.alt, caption: (n === 0 && el.getAttribute('data-caption')) || img.alt });
    });
  });
  function setActual(on) {
    if (!lb) return;
    lb.classList.toggle('actual', on);
    lbActual.setAttribute('aria-pressed', on ? 'true' : 'false');
    $('#lb-actual-l').textContent = on ? 'Fit to screen' : 'Actual size';
    if (on && lbImg.naturalWidth) {
      // 1:1 with the capture's device pixels.
      lbImg.style.width = Math.round(lbImg.naturalWidth / (window.devicePixelRatio || 1)) + 'px';
    } else {
      lbImg.style.width = '';
    }
  }
  function show(i, animate) {
    if (!items.length) return;
    lbIndex = (i + items.length) % items.length;
    var it = items[lbIndex];
    function apply() {
      setActual(false);
      lbImg.src = it.src; lbImg.alt = it.alt;
      lbCap.textContent = it.caption;
      lbCount.textContent = (lbIndex + 1) + ' / ' + items.length;
      lbImg.classList.remove('swap');
    }
    if (animate && !reduceMotion) { lbImg.classList.add('swap'); setTimeout(apply, 150); } else apply();
  }
  function openFrom(el, opener) {
    // Open the image that is visible in the element (themed frames hold a light and a dark one).
    var imgs = $$('img', el);
    var visible = imgs.filter(function (im) { return im.offsetParent !== null; })[0] || imgs[0];
    var idx = visible ? indexOfSrc(visible.getAttribute('src')) : 0;
    if (idx < 0) idx = 0;
    if (!lb || typeof lb.showModal !== 'function') { window.open(items[idx].src, '_blank', 'noopener'); return; }
    lastFocus = opener || el;
    show(idx, false);
    lb.showModal();
    $('#lb-close').focus();
  }
  function closeLb() { if (lb && lb.open) lb.close(); }
  if (lb) {
    document.addEventListener('click', function (e) {
      var el = e.target.closest('[data-lb]');
      if (!el || el.closest('dialog')) return;
      e.preventDefault();
      openFrom(el, el.matches('button') ? el : $('#hero-zoom'));
    });
    $('#lb-close').addEventListener('click', closeLb);
    $('#lb-prev').addEventListener('click', function () { show(lbIndex - 1, true); });
    $('#lb-next').addEventListener('click', function () { show(lbIndex + 1, true); });
    lbActual.addEventListener('click', function () { setActual(!lb.classList.contains('actual')); });
    lbImg.addEventListener('dblclick', function () { setActual(!lb.classList.contains('actual')); });
    lbImg.addEventListener('click', function () { if (!lb.classList.contains('actual') && window.matchMedia('(hover: hover)').matches) setActual(true); });
    lb.addEventListener('keydown', function (e) {
      if (e.key === 'ArrowRight') { e.preventDefault(); show(lbIndex + 1, true); }
      else if (e.key === 'ArrowLeft') { e.preventDefault(); show(lbIndex - 1, true); }
      else if (e.key === 'Home') { e.preventDefault(); show(0, true); }
      else if (e.key === 'End') { e.preventDefault(); show(items.length - 1, true); }
      else if (e.key === '1' || e.key === 'z') { e.preventDefault(); setActual(!lb.classList.contains('actual')); }
    });
    lb.addEventListener('click', function (e) {
      // A click on the dark backdrop (not the image or a control) closes.
      if (e.target === lb || e.target.classList.contains('lb-inner') || e.target.classList.contains('lb-fig')) closeLb();
    });
    lb.addEventListener('close', function () {
      setActual(false);
      lbImg.removeAttribute('src');
      if (lastFocus && typeof lastFocus.focus === 'function') lastFocus.focus();
    });
    var tx = null, ty = null;
    lb.addEventListener('touchstart', function (e) {
      // Two fingers = native pinch zoom; actual-size mode = panning. Neither is a swipe.
      if (e.touches.length !== 1 || lb.classList.contains('actual')) { tx = null; return; }
      tx = e.touches[0].clientX; ty = e.touches[0].clientY;
    }, { passive: true });
    lb.addEventListener('touchend', function (e) {
      if (tx === null) return;
      if (window.visualViewport && window.visualViewport.scale > 1.05) { tx = null; return; }
      var t = e.changedTouches[0];
      var dx = t.clientX - tx, dy = t.clientY - ty;
      tx = null;
      if (Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy) * 1.3) show(lbIndex + (dx < 0 ? 1 : -1), true);
      else if (dy > 90 && Math.abs(dy) > Math.abs(dx) * 1.5) closeLb();
    }, { passive: true });
  }

  /* ---------- GitHub stars ---------- */
  function formatStars(n) {
    if (n >= 10000) return Math.round(n / 1000) + 'k';
    if (n >= 1000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k';
    return String(n);
  }
  function getJSON(url) {
    var cached = sessionCache('mcdc:' + url);
    if (cached) return Promise.resolve(cached);
    if (!window.fetch) return Promise.reject(new Error('no fetch'));
    return fetch(url, { headers: { Accept: 'application/vnd.github+json' } }).then(function (r) {
      if (!r.ok) { var err = new Error('HTTP ' + r.status); err.status = r.status; throw err; }
      return r.json();
    }).then(function (j) { sessionCache('mcdc:' + url, j); return j; });
  }
  getJSON(API).then(function (repo) {
    if (repo && typeof repo.stargazers_count === 'number') {
      $('#gh-stars-n').textContent = formatStars(repo.stargazers_count);
      $('#gh-stars').hidden = false;
      $('.gh-btn').setAttribute('aria-label', 'GitHub repository, ' + repo.stargazers_count + ' stars');
    }
  }).catch(function () { /* keep the plain GitHub button */ });

  /* ---------- platform detection ---------- */
  var LABELS = {
    'darwin/arm64': 'macOS (Apple silicon)', 'darwin/amd64': 'macOS (Intel)',
    'windows/amd64': 'Windows (x64)', 'windows/arm64': 'Windows (ARM64)',
    'linux/amd64': 'Linux (x86-64)', 'linux/arm64': 'Linux (ARM64)'
  };
  function osFromString(s) {
    s = s || '';
    if (/Android/i.test(s)) return 'android';
    if (/iPhone|iPad|iPod|iOS/i.test(s)) return 'ios';
    if (/CrOS|Chrome OS/i.test(s)) return 'chromeos';
    if (/Win/i.test(s)) return 'windows';
    if (/Mac/i.test(s)) return 'darwin';
    if (/Linux|X11|FreeBSD/i.test(s)) return 'linux';
    return null;
  }
  function archFromString(s) {
    s = s || '';
    if (/aarch64|arm64|armv8|\bARM\b/i.test(s)) return 'arm64';
    if (/x86_64|x86-64|x64|Win64|WOW64|amd64|i[3-6]86/i.test(s)) return 'amd64';
    return null;
  }
  // Returns {os, arch, mobile, guessed, source}
  function detectPlatform() {
    var ua = navigator.userAgent || '';
    var plat = navigator.platform || '';
    var uaOS = osFromString(ua) || osFromString(plat);
    var uaArch = archFromString(ua) || archFromString(plat);
    var ipadOS = plat === 'MacIntel' && navigator.maxTouchPoints > 1;
    var base = { os: uaOS, arch: uaArch, mobile: uaOS === 'android' || uaOS === 'ios' || ipadOS || /Mobi/i.test(ua), guessed: false, source: 'ua' };
    if (ipadOS) base.os = 'ios';

    var uad = navigator.userAgentData;
    var hint = (uad && typeof uad.getHighEntropyValues === 'function')
      ? uad.getHighEntropyValues(['platform', 'architecture', 'bitness']).catch(function () { return null; })
      : Promise.resolve(null);
    return hint.then(function (v) {
      var r = { os: base.os, arch: base.arch, mobile: base.mobile, guessed: false, source: base.source };
      if (v) {
        if (uad.mobile) r.mobile = true;
        var chOS = osFromString(v.platform);
        // If the UA string names a different OS than the client hints, someone (an
        // extension, a test, an embedded browser) overrode it: trust the UA string.
        var agree = !chOS || !r.os || chOS === r.os;
        if (agree) {
          r.os = r.os || chOS;
          if (chOS === 'android' || chOS === 'ios') r.mobile = true;
          if (v.architecture === 'arm') r.arch = 'arm64';
          else if (v.architecture === 'x86') r.arch = 'amd64'; // 32-bit x86 runs amd64 builds on 64-bit Windows; there is no 386 build
          r.source = 'ua-ch';
        }
      }
      if (r.os === 'darwin' && !r.arch) { r.arch = 'arm64'; r.guessed = true; } // Safari/Firefox do not expose the CPU; most Macs sold since 2020 are Apple silicon
      if (r.os === 'darwin' && r.source === 'ua' && r.arch === 'amd64' && /Intel Mac OS X/.test(ua) && !/x86_64/.test(ua)) {
        // "Intel Mac OS X" is frozen in every Mac UA string, including Apple silicon.
        r.arch = 'arm64'; r.guessed = true;
      }
      if ((r.os === 'linux' || r.os === 'windows') && !r.arch) { r.arch = 'amd64'; r.guessed = true; }
      if (r.os === 'chromeos') { r.os = 'linux'; r.arch = r.arch || 'amd64'; r.guessed = true; }
      return r;
    });
  }

  /* ---------- releases ---------- */
  // Uses the releases list rather than /releases/latest: before the first release,
  // /latest answers 404, which browsers log as a console error on every visit.
  // The list answers 200 with []. The first non-draft, non-prerelease entry is
  // the same release /latest would return.
  var ASSET_KEYS = {
    '_linux_amd64.tar.gz': 'linux-x64', '_linux_arm64.tar.gz': 'linux-arm64',
    '_darwin_amd64.tar.gz': 'mac-x64', '_darwin_arm64.tar.gz': 'mac-arm64',
    '_windows_amd64.zip': 'win-x64', '_windows_arm64.zip': 'win-arm64'
  };
  var KEY_OF = { 'linux/amd64': 'linux-x64', 'linux/arm64': 'linux-arm64', 'darwin/amd64': 'mac-x64', 'darwin/arm64': 'mac-arm64', 'windows/amd64': 'win-x64', 'windows/arm64': 'win-arm64' };
  function fetchRelease() {
    var url = API + '/releases?per_page=10';
    var cached = sessionCache('mcdc:rel');
    if (cached) return Promise.resolve(cached.none ? null : cached);
    if (!window.fetch) return Promise.resolve(null);
    return fetch(url, { headers: { Accept: 'application/vnd.github+json' } }).then(function (r) {
      if (!r.ok) return null;
      return r.json().then(function (list) {
        var data = Array.isArray(list) ? list.filter(function (x) { return x && !x.draft && !x.prerelease; })[0] : null;
        if (!data || !data.tag_name) { sessionCache('mcdc:rel', { none: true }); return null; }
        var assets = {};
        (data.assets || []).forEach(function (a) {
          var name = String(a.name || '');
          Object.keys(ASSET_KEYS).forEach(function (suffix) {
            if (name.indexOf('meilisearch-cdc_') === 0 && name.slice(-suffix.length) === suffix) assets[ASSET_KEYS[suffix]] = { name: name, url: a.browser_download_url, size: a.size };
          });
          if (name === 'checksums.txt') assets.checksums = { name: name, url: a.browser_download_url };
        });
        var rel = { tag: data.tag_name, version: String(data.tag_name).replace(/^v/, ''), url: data.html_url || RELEASES_LATEST, assets: assets };
        sessionCache('mcdc:rel', rel);
        return rel;
      });
    }).catch(function () { return null; });
  }
  function fmtSize(b) { return b ? (b / 1048576).toFixed(1) + ' MB' : ''; }
  function assetName(version, os, arch) {
    return 'meilisearch-cdc_' + version + '_' + os + '_' + arch + (os === 'windows' ? '.zip' : '.tar.gz');
  }
  var ARCH_LABEL = { 'darwin/arm64': 'Apple silicon', 'darwin/amd64': 'Intel', 'windows/amd64': 'x64', 'windows/arm64': 'Arm64', 'linux/amd64': 'x64', 'linux/arm64': 'Arm64' };
  var OS_LABEL = { darwin: 'macOS', windows: 'Windows', linux: 'Linux' };

  function render(p, rel) {
    var known = p.os && OS_LABEL[p.os] && p.arch && !p.mobile;

    // Version pill.
    var pill = $('#latest-ver-badge'), tag = $('#ver-tag');
    if (rel) {
      tag.textContent = 'Latest release: ' + rel.tag;
      pill.href = rel.url;
      pill.classList.add('live');
    } else {
      tag.textContent = 'See releases on GitHub';
      pill.href = RELEASES_LATEST;
      pill.classList.remove('live');
    }

    // Every pill/button with a data-asset key gets the real asset URL when there is one.
    $$('[data-asset]').forEach(function (a) {
      var asset = rel && rel.assets[a.getAttribute('data-asset')];
      a.href = asset ? asset.url : (rel ? rel.url : RELEASES_LATEST);
      if (asset && asset.size) a.title = asset.name + ' · ' + fmtSize(asset.size); else a.removeAttribute('title');
    });

    // Cards: highlight the detected OS; its primary button follows the detected arch.
    ['windows', 'linux', 'darwin'].forEach(function (os) {
      var card = $('#card-' + os);
      var arch = (known && p.os === os) ? p.arch : (os === 'darwin' ? 'arm64' : 'amd64');
      var key = KEY_OF[os + '/' + arch];
      var btn = $('#btn-primary-' + os);
      btn.setAttribute('data-asset', key);
      var asset = rel && rel.assets[key];
      btn.href = asset ? asset.url : (rel ? rel.url : RELEASES_LATEST);
      $('#sub-' + os).textContent = ARCH_LABEL[os + '/' + arch] + ' · ' + (os === 'windows' ? '.zip' : '.tar.gz') + (asset && asset.size ? ' · ' + fmtSize(asset.size) : '');
      btn.setAttribute('aria-label', 'Download for ' + OS_LABEL[os] + ', ' + ARCH_LABEL[os + '/' + arch] + (rel ? '' : ' (opens GitHub Releases)'));
      var on = !!known && p.os === os;
      card.classList.toggle('highlighted', on);
      $$('.pill', card).forEach(function (pl) { pl.classList.toggle('is-rec', on && pl.getAttribute('data-asset') === key); });
    });
    $('#dl-mobile').hidden = !p.mobile;

    // Hero CTA.
    $('#hero-dl-label').textContent = known ? 'Download for ' + OS_LABEL[p.os] : 'Download';
    $('#hero-dl').href = '#download';
    doc.setAttribute('data-platform', known ? p.os + '/' + p.arch : (p.mobile ? 'mobile' : 'unknown'));

    // Docker: pin the version when there is one.
    var pull = $('#pull-cmd');
    if (pull) pull.textContent = rel
      ? 'docker pull ' + IMAGE.replace(':latest', ':' + rel.version) + '\n' + 'docker pull ' + IMAGE
      : 'docker pull ' + IMAGE;

    // "Binary" tab commands.
    var binCmd = $('#bin-cmd'), binNote = $('#bin-note');
    if (binCmd) {
      var os2 = known ? p.os : 'linux', arch2 = known ? p.arch : 'amd64';
      var ver = rel ? rel.version : '<version>';
      var name = assetName(ver, os2, arch2);
      var dir = name.replace(/\.(tar\.gz|zip)$/, '');
      var a2 = rel && rel.assets[KEY_OF[os2 + '/' + arch2]];
      var url = a2 ? a2.url : GH + '/releases/download/v' + ver + '/' + name;
      binCmd.textContent = os2 === 'windows'
        ? '# PowerShell\nInvoke-WebRequest ' + url + ' -OutFile ' + name + '\nExpand-Archive ' + name + ' -DestinationPath .\ncd ' + dir + '\n.\\meilisearch-cdc.exe'
        : 'curl -LO ' + url + '\ntar -xzf ' + name + '\ncd ' + dir + '\n./meilisearch-cdc';
      if (binNote) {
        binNote.textContent = 'Commands for ' + OS_LABEL[os2] + ' (' + ARCH_LABEL[os2 + '/' + arch2] + ')' +
          (rel ? ', release ' + rel.tag + '. ' : '. Replace <version> with a release from GitHub. ') +
          'The binary needs no runtime and no configuration.' +
          (os2 === 'darwin' ? ' If you download with a browser instead of curl, macOS may quarantine the unsigned binary: run xattr -d com.apple.quarantine meilisearch-cdc.' : '');
      }
    }
  }

  // Render at once from the user agent, then again when client hints and the release arrive.
  var platformP = detectPlatform();
  var releaseP = fetchRelease();
  Promise.all([platformP, releaseP]).then(function (r) {
    render(r[0], r[1]);
    window.__mcdcPlatform = r[0];
    window.__mcdcRelease = r[1];
  }).catch(function () { /* the static fallback links stay */ });
})();
