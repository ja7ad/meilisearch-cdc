/* Live replica of the overview dashboard in the hero.
   - Designed at 1280x800 and scaled as one unit to its frame (--s).
   - A scripted 28 s story: steady streaming, a new task snapshots then streams,
     the Staging instance goes down (alert by email/Telegram) and recovers, a
     rejected document lands in the dead letter queue, an AI agent pauses and
     resumes a task over MCP.
   - Text updates at 5 Hz, the canvas chart at display rate. Paused off-screen,
     in hidden tabs and by the Pause button; a static frame under
     prefers-reduced-motion. Seeded randomness: every visit looks the same. */
(function () {
  'use strict';
  var app = document.getElementById('app');
  var vp = document.getElementById('app-vp');
  if (!app || !vp) return;
  var $ = function (s) { return document.querySelector(s); };
  var reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  /* ---- scale the 1280x800 design to the frame ---- */
  function fit() { app.style.setProperty('--s', (vp.clientWidth / 1280).toFixed(5)); }
  fit();
  if (window.ResizeObserver) new ResizeObserver(fit).observe(vp); else window.addEventListener('resize', fit);

  /* ---- seeded PRNG ---- */
  function prng(seed) {
    return function () {
      seed |= 0; seed = seed + 0x6D2B79F5 | 0;
      var t = Math.imul(seed ^ seed >>> 15, 1 | seed);
      t = t + Math.imul(t ^ t >>> 7, 61 | t) ^ t;
      return ((t ^ t >>> 14) >>> 0) / 4294967296;
    };
  }
  var rnd = prng(20261026);
  var fmt = function (n) { return Math.round(n).toLocaleString('en-US'); };
  var compact = function (n) { return n >= 1e6 ? (n / 1e6).toFixed(2) + 'M' : n >= 1e4 ? (n / 1e3).toFixed(1) + 'K' : fmt(n); };
  var hex = function () { var s = ''; for (var i = 0; i < 12; i++) s += '0123456789abcdef'[Math.floor(rnd() * 16)]; return s; };

  /* ---- model ---- */
  var LOOP = 28;
  var tasks = []; // 15 streaming pipelines with small rates (sum ~100/s)
  for (var i = 0; i < 15; i++) { var b = 2 + rnd() * 12; tasks.push({ base: b, rate: b, state: 'stream' }); }
  var snap = { on: false, progress: 0, rows: 400000 };
  var m = { docs: 221000, deleted: 5, alerts: 1, dlq: 29, paused: 0, extra: 0, euDocs: 45700 };
  var hist = [];
  for (var h = 0; h < 90; h++) hist.push(95 + Math.sin(h / 5) * 12 + rnd() * 10);

  /* ---- DOM helpers ---- */
  var cache = new Map();
  function setText(el, v) { if (el && cache.get(el) !== v) { cache.set(el, v); el.textContent = v; } }
  function pop(el) { if (!el) return; el.classList.remove('pop'); void el.offsetWidth; el.classList.add('pop'); }
  var feed = $('#app-feed');
  function log(text, color) {
    var li = document.createElement('li');
    li.innerHTML = '<i class="fd fd-' + (color || 'v') + '"></i><span></span>';
    var sp = li.lastChild;
    sp.appendChild(document.createTextNode(text));
    var sm = document.createElement('small'); sm.innerHTML = '<code></code>just now';
    sm.firstChild.textContent = hex(); sp.appendChild(sm);
    feed.insertBefore(li, feed.firstChild);
    while (feed.children.length > 6) feed.removeChild(feed.lastChild);
  }
  var capEl = $('#app-cap'), capT = $('#app-cap-t'), capTimer;
  function caption(text, ms) {
    if (!capEl) return;
    setText(capT, text); capEl.classList.add('show');
    clearTimeout(capTimer); capTimer = setTimeout(function () { capEl.classList.remove('show'); }, ms || 3600);
  }
  var toast = $('#app-toast'), toastTimer;
  function showToast(ok, title, sub, ms) {
    toast.classList.toggle('ok', ok);
    setText($('#app-toast-t'), title); setText($('#app-toast-s'), sub);
    toast.classList.add('show');
    clearTimeout(toastTimer); toastTimer = setTimeout(function () { toast.classList.remove('show'); }, ms || 3800);
  }
  function setStaging(down) {
    $('#d-stg').classList.toggle('down', down);
    var dot = document.querySelector('.hdots i[data-dest="stg"]'); if (dot) dot.classList.toggle('down', down);
  }
  function setAlerts(n, critical) {
    m.alerts = n;
    var k = $('#k-alerts'); setText(k, String(n)); pop(k);
    var nav = $('#nav-alerts'); setText(nav, String(n)); pop(nav);
    setText($('#k-alerts-s'), (critical ? '1 critical · ' : '0 critical · ') + '1 warning · 0 info');
    var card = $('#kpi-alerts'); card.classList.toggle('flash', !!critical);
  }
  function setDlq(n) {
    m.dlq = n;
    var k = $('#k-dlq'); setText(k, String(n)); pop(k);
    setText($('#k-dlq-s'), '0 errors · ' + n + ' retries');
    var c = $('#kpi-dlq'); c.classList.add('warn'); setTimeout(function () { c.classList.remove('warn'); }, 2200);
  }

  /* ---- story ---- */
  var events = [
    [0.3, function () { // tidy up the previous loop quietly
      if (m.extra) { m.extra = 0; tasks.pop(); }
      if (m.dlq !== 29) { m.dlq = 29; setText($('#k-dlq'), '29'); setText($('#k-dlq-s'), '0 errors · 29 retries'); log('1 dead letter replayed', 'g'); }
    }],
    [4, function () {
      snap.on = true; snap.progress = 0; m.extra = 1;
      log('task created: customers ← PostgreSQL · snapshot started', 'p');
      caption('Snapshot then stream, no gap');
    }],
    [9.4, function () {
      snap.on = false; tasks.push({ base: 9, rate: 9, state: 'stream' });
      log('snapshot finished: ' + fmt(snap.rows) + ' documents · streaming from 0/1E4A2F0', 'g');
    }],
    [11, function () {
      setStaging(true); setAlerts(2, true);
      log('Meilisearch Staging unreachable: connection refused', 'r');
      showToast(false, 'Alert · Meilisearch unreachable · Staging', 'emailed oncall@acme.io · Telegram #ops', 4300);
      caption('Alert by email & Telegram');
    }],
    [17, function () {
      setStaging(false); setAlerts(1, false);
      log('Staging is reachable again · alert resolved', 'g');
      showToast(true, 'Resolved · Staging is reachable again', 'resolve notice sent by email & Telegram', 3200);
    }],
    [19.5, function () {
      setDlq(30);
      log('1 document rejected → dead letter queue · sync keeps going', 'a');
      caption('Bad documents isolated in the DLQ');
    }],
    [23, function () {
      tasks[6].state = 'pause'; m.paused = 1;
      log('Claude Desktop paused "Live prices" via MCP', 'p');
      caption('AI agents via MCP');
    }],
    [26.2, function () {
      tasks[6].state = 'stream'; m.paused = 0;
      log('Claude Desktop resumed "Live prices" via MCP', 'p');
    }]
  ];

  /* ---- simulation tick (5 Hz) ---- */
  var T = 0, fired = {}, acc = 0, lastSample = performance.now();
  function tick(dt) {
    var total = 0, streaming = 0;
    tasks.forEach(function (t) {
      if (t.state === 'stream') {
        var target = t.base * (0.6 + rnd() * 0.8);
        t.rate += (target - t.rate) * 0.35; streaming++;
      } else t.rate = 0;
      total += t.rate;
    });
    if (snap.on) {
      var ramp = Math.min(1, snap.progress / 0.12, (1 - snap.progress) / 0.08 + 0.15);
      var sr = 85000 * (0.9 + rnd() * 0.2) * Math.max(0.05, ramp);
      snap.progress = Math.min(1, snap.progress + dt / 5.2);
      total += sr;
    }
    m.docs += total * dt * (snap.on ? 0.06 : 1); // the snapshot is slowed down for the eye; keep the counter believable
    if (rnd() < 0.02) m.deleted++;
    hist.push(total); if (hist.length > 140) hist.shift();
    lastSample = performance.now();

    var count = tasks.length + (snap.on ? 1 : 0);
    setText($('#k-tasks'), String(count));
    var bits = snap.on ? ['1 snapshotting · ' + Math.round(snap.progress * 100) + '%'] : [streaming + ' streaming'];
    if (m.paused) bits.push(m.paused + ' paused');
    setText($('#k-tasks-s'), bits.join(' · '));
    var bars = document.querySelectorAll('.sbar i');
    bars[0].style.flex = streaming; bars[1].style.flex = snap.on ? 1 : 0; bars[2].style.flex = m.paused;
    setText($('#k-rate'), fmt(total));
    setText($('#chart-now'), fmt(total));
    setText($('#k-docs'), compact(m.docs));
    setText($('#k-docs-s'), m.deleted + ' deleted · ' + compact(m.docs + m.deleted) + ' events');
    m.euDocs += total * dt * 0.02;
    setText($('#d-eu'), compact(m.euDocs));
  }

  /* ---- chart: canvas, display rate, scrolls smoothly between samples ---- */
  var cv = $('#app-chart'), ctx = cv.getContext('2d'), W = 0, H = 0, dpr = 1, rgb = '255, 92, 170', grid = 'rgba(120,110,150,.16)', axis = '#8A84A1';
  function sizeChart() {
    // The canvas lives inside the scaled design: size it in design pixels, render at device resolution.
    W = cv.offsetWidth; H = cv.offsetHeight;
    var s = vp.clientWidth / 1280;
    dpr = Math.min(3, Math.max(1, (window.devicePixelRatio || 1) * s));
    cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr);
    var cs = getComputedStyle(app);
    rgb = (cs.getPropertyValue('--a-chart') || rgb).trim();
    grid = (cs.getPropertyValue('--a-grid') || grid).trim();
    axis = (cs.getPropertyValue('--a-axis') || axis).trim();
    draw(performance.now());
  }
  function nice(v) { var p = Math.pow(10, Math.floor(Math.log10(v))); var n = v / p; return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * p; }
  function path(data, step, frac, x0, y0, h, mx) {
    ctx.beginPath();
    for (var i = 0; i < data.length; i++) {
      var x = x0 + (i - frac) * step, y = y0 + h - data[i] / mx * h;
      if (i === 0) ctx.moveTo(x, y);
      else { var px = x0 + (i - 1 - frac) * step, py = y0 + h - data[i - 1] / mx * h; ctx.bezierCurveTo(px + step / 2, py, x - step / 2, y, x, y); }
    }
  }
  function draw(now) {
    if (!W || !H) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, W, H);
    var n = 100, data = hist.slice(-n);
    var mx = 0; data.forEach(function (v) { if (v > mx) mx = v; });
    var top = nice(Math.max(mx * 1.1, 120));
    var x0 = 54, y0 = 18, h = H - y0 - 30, w = W - x0 - 18, step = w / (n - 2);
    var frac = Math.min(1, (now - lastSample) / 200);
    ctx.font = '11.5px Inter, system-ui, sans-serif'; ctx.textAlign = 'right'; ctx.textBaseline = 'middle';
    for (var g = 0; g <= 4; g++) {
      var gy = Math.round(y0 + h - h * g / 4) + .5;
      ctx.strokeStyle = grid; ctx.lineWidth = 1; ctx.setLineDash([3, 4]);
      ctx.beginPath(); ctx.moveTo(x0, gy); ctx.lineTo(x0 + w, gy); ctx.stroke();
      ctx.setLineDash([]);
      ctx.fillStyle = axis;
      var val = top * g / 4;
      ctx.fillText(val >= 1000 ? (val / 1000).toFixed(val % 1000 ? 1 : 0) + 'K' : String(Math.round(val)), x0 - 10, gy);
    }
    ctx.textAlign = 'center'; ctx.textBaseline = 'top';
    ['11:24 PM', '11:25 PM', '11:25 PM', '11:26 PM', '11:26 PM'].forEach(function (l, i) { ctx.fillText(l, x0 + 20 + (w - 40) * i / 4, y0 + h + 10); });
    ctx.save();
    ctx.beginPath(); ctx.rect(x0, 0, w, H); ctx.clip();
    path(data, step, frac, x0, y0, h, top);
    ctx.lineTo(x0 + w + step, y0 + h); ctx.lineTo(x0 - step, y0 + h); ctx.closePath();
    var grd = ctx.createLinearGradient(0, y0, 0, y0 + h);
    grd.addColorStop(0, 'rgba(' + rgb + ', .32)'); grd.addColorStop(1, 'rgba(' + rgb + ', .02)');
    ctx.fillStyle = grd; ctx.fill();
    path(data, step, frac, x0, y0, h, top);
    ctx.strokeStyle = 'rgb(' + rgb + ')'; ctx.lineWidth = 2; ctx.stroke();
    ctx.restore();
  }
  if (window.ResizeObserver) new ResizeObserver(sizeChart).observe(vp); else window.addEventListener('resize', sizeChart);
  new MutationObserver(function () { setTimeout(sizeChart, 30); }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  if (window.matchMedia) { var mq = window.matchMedia('(prefers-color-scheme: dark)'); if (mq.addEventListener) mq.addEventListener('change', sizeChart); }

  /* ---- loop control ---- */
  var running = false, onScreen = true, raf = 0, last = 0;
  function runEvents() { events.forEach(function (ev, i) { if (!fired[i] && T >= ev[0]) { fired[i] = true; ev[1](); } }); }
  function frame(now) {
    raf = 0;
    if (!running) return;
    var dt = Math.min(0.1, (now - last) / 1000); last = now;
    T += dt;
    if (T >= LOOP) { T -= LOOP; fired = {}; }
    runEvents();
    acc += dt;
    if (acc >= 0.2) { tick(acc); acc = 0; }
    draw(now);
    raf = requestAnimationFrame(frame);
  }
  function update() {
    var should = !reduce && onScreen && !document.hidden;
    if (should && !running) { running = true; last = performance.now(); if (!raf) raf = requestAnimationFrame(frame); }
    else if (!should && running) { running = false; if (raf) cancelAnimationFrame(raf); raf = 0; }
  }
  if ('IntersectionObserver' in window) new IntersectionObserver(function (en) { onScreen = en[0].isIntersecting; update(); }, { threshold: 0.02 }).observe(vp);
  document.addEventListener('visibilitychange', update);

  /* ---- start ---- */
  for (var k = 0; k < 30; k++) tick(0.2);
  if (reduce) {
    // Static, fully populated frame (Staging down, as in the real screenshot).
    setStaging(true);
  } else {
    setStaging(false); setText($('#k-alerts'), '1'); setText($('#nav-alerts'), '1'); setText($('#k-alerts-s'), '0 critical · 1 warning · 0 info');
    update();
  }
  sizeChart();
  // For tests: jump the story clock.
  window.__mcdcHero = {
    seek: function (t) { while (T < t) { var d = Math.min(0.2, t - T); T += d; runEvents(); tick(d); } draw(performance.now()); },
    time: function () { return T; }
  };
})();
