// Overview: KPI tiles, live throughput, activity feed and task cards.
import { html, useEffect, useMemo, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { activeAlerts, live, seedOverviewHist, statusOf, useLive } from '../sse.js';
import { can } from '../auth.js';
import { useDestinations } from '../destinations.js';
import { DestinationBadge, HealthDot } from './destinations.js';
import { GettingStarted } from './onboarding.js';
import { useFetch, useTick } from '../hooks.js';
import { AreaChart, Sparkline, StackBar } from '../charts.js';
import { SourceIcon, seedMetrics } from '../components.js';
import { Icon } from '../icons.js';
import { Counter, Empty, ErrorBox, PageHeader, Progress, Skeleton, StateBadge } from '../ui.js';
import { STATES, fmtBytes, fmtCompact, fmtInt, fmtLag, fmtRate, isActive, stateInfo, timeAgo } from '../util.js';

let overviewSeeded = false;

function Kpi({ icon, label, alt, children, sub, i, testid }) {
  return html`<div class="card kpi is-hoverable" style=${`--i:${i}`} data-testid=${testid}>
    <div class="kpi-label"><span class=${`kpi-icon ${alt ? 'alt' : ''}`}><${Icon} name=${icon} size=${16} /></span>${label}</div>
    <div class="kpi-value">${children}</div>
    ${sub && html`<div class="kpi-sub">${sub}</div>`}
  </div>`;
}

function AlertsKpi({ i }) {
  const firing = activeAlerts();
  const by = (s) => firing.filter((e) => e.severity === s).length;
  const crit = by('critical');
  return html`<a class=${`card kpi is-hoverable kpi-link ${firing.length ? (crit ? 'kpi-alert-critical' : 'kpi-alert-warning') : ''}`} href="#/alerts" style=${`--i:${i}`} data-testid="kpi-alerts">
    <div class="kpi-label"><span class=${`kpi-icon ${firing.length ? 'kpi-icon-alert' : 'alt'}`}><${Icon} name="bell" size=${16} /></span>Active alerts
      ${firing.length > 0 && html`<span class="alert-pulse" aria-hidden="true"></span>`}</div>
    <div class="kpi-value"><${Counter} value=${firing.length} /></div>
    <div class="kpi-sub">${firing.length ? html`<span>${crit} critical · ${by('warning')} warning · ${by('info')} info</span>` : html`<span>All clear</span>`}</div>
  </a>`;
}

function KpiSkeleton() {
  return html`<div class="kpis">${[0, 1, 2, 3, 4, 5].map((i) => html`<div class="card kpi" key=${i}><${Skeleton} w="50%" h=${12} /><${Skeleton} w="70%" h=${28} /><${Skeleton} w="40%" h=${10} /></div>`)}</div>`;
}

/** One tile per Meilisearch destination (health, version, documents). */
function DestinationsStrip({ list }) {
  if (!list) return null;
  if (!list.length) {
    return html`<div class="card dest-strip-empty" data-testid="destinations-strip"><span class="kpi-icon alt"><${Icon} name="database" size=${16} /></span>
      <div class="grow"><b>No Meilisearch destination</b><p class="muted">Tasks need a Meilisearch instance to write to.</p></div>
      ${can('index.write') && html`<a class="btn btn-primary btn-sm" href="#/destinations"><${Icon} name="plus" size=${15} /><span>Add a destination</span></a>`}</div>`;
  }
  return html`<div class="dest-strip stagger" data-testid="destinations-strip" aria-label="Meilisearch destinations">
    ${list.map((d, i) => html`<a class=${`card dest-tile is-hoverable ${d.healthy ? '' : 'is-down'}`} href=${`#/destinations/${encodeURIComponent(d.id)}`} style=${`--i:${i}`} data-destination=${d.id}>
      <div class="dest-tile-head"><${HealthDot} healthy=${d.healthy} label=${`${d.name} ${d.healthy ? 'healthy' : 'unreachable'}`} /><b>${d.name}</b>${d.isDefault && html`<span class="tag tag-primary">default</span>`}</div>
      ${d.healthy ? html`<div class="dest-tile-stats"><span>v${d.version}</span><span><b class="num">${fmtCompact(d.documents)}</b> docs</span><span><b class="num">${fmtInt(d.indexes)}</b> indexes</span></div>`
        : html`<p class="dest-error" title=${d.error}>${d.error || 'Unreachable'}</p>`}
    </a>`)}
  </div>`;
}

export function TaskCard({ task, i, dests }) {
  const st = statusOf(task) || {};
  const info = stateInfo(st.state);
  const hist = (live.hist[task.id] || []).map((p) => p.rate);
  const snap = st.snapshot || {};
  const snapping = (st.state === 'snapshotting' || st.state === 'resyncing') && snap.total > 0;
  return html`<a class="card task-card" href=${`#/tasks/${encodeURIComponent(task.id)}`} style=${`--i:${i}`} data-testid="task-card" aria-label=${`${task.name}, ${info.label}`}>
    <div class="task-card-head">
      <${SourceIcon} type=${task.source && task.source.type} />
      <div class="grow"><div class="task-card-title">${task.name || task.id}</div>
        <div class="task-card-sub">${task.source && task.source.table} → <span class="mono">${task.index && task.index.uid}</span></div>
        <${DestinationBadge} list=${dests} id=${task.index && task.index.destinationId} /></div>
      <${StateBadge} state=${st.state} />
    </div>
    ${snapping ? html`<div class="stack-sm"><${Progress} value=${snap.done} max=${snap.total} shimmer label="Snapshot progress" />
        <div class="row-between subtle" style="font-size:12px"><span>${fmtCompact(snap.done)} / ${fmtCompact(snap.total)} rows</span><span>${Math.floor((snap.done / snap.total) * 100)}%</span></div></div>`
      : st.error && !isActive(st.state) ? html`<div class="task-card-error" title=${st.error}>${st.error}</div>` : null}
    <div class="task-card-metrics">
      <div class="mini-stat"><span>Rate</span><b><${Counter} value=${st.rate || 0} format=${fmtRate} /><small class="subtle"> /s</small></b></div>
      <div class="mini-stat"><span>Lag</span><b>${isActive(st.state) ? fmtLag(st.lagMs) : '—'}</b></div>
      <${Sparkline} values=${hist} width=${110} height=${34} tone=${info.tone === 'muted' ? 'muted' : info.tone === 'bad' ? 'bad' : info.tone === 'warn' ? 'warn' : 'primary'} />
    </div>
  </a>`;
}

function ActivityFeed({ seed }) {
  const lv = useLive();
  useTick(5000);
  const items = useMemo(() => {
    const seen = new Set();
    return [...lv.logs, ...seed].filter((l) => { const k = `${l.time}|${l.taskId}|${l.message}`; if (seen.has(k)) return false; seen.add(k); return true; })
      .sort((a, b) => Date.parse(b.time) - Date.parse(a.time)).slice(0, 40);
  }, [lv.logSeq, seed]);
  if (!items.length) return html`<${Empty} icon="activity" title="No activity yet">Task events will stream in here.</${Empty}>`;
  const tone = { error: 'bad', warn: 'warn', warning: 'warn', info: 'sync', debug: 'muted' };
  return html`<ul class="feed" aria-label="Recent activity" data-testid="activity-feed">
    ${items.map((l) => html`<li key=${`${l.time}|${l.taskId}|${l.message}`} class=${`tone-${tone[l.level] || 'muted'}`}>
      <span class="lvl" aria-hidden="true"></span>
      <div><div class="feed-msg">${l.message}</div>
        <div class="feed-meta">${l.taskId && html`<a href=${`#/tasks/${encodeURIComponent(l.taskId)}/logs`}>${l.taskId}</a>`}<span>${timeAgo(l.time)}</span><span class="sr-only">level ${l.level}</span></div></div>
    </li>`)}
  </ul>`;
}

export function OverviewPage() {
  const lv = useLive();
  const dests = useDestinations();
  const ov = useFetch(() => api.overview(), []);
  const tasks = useFetch(() => api.tasks(), []);
  const [logSeed, setLogSeed] = useState([]);

  // refetch the task list when tasks are created/updated/deleted
  useEffect(() => { if (lv.taskSeq) tasks.reload(true); }, [lv.taskSeq]);

  useEffect(() => {
    const list = tasks.data;
    if (!list || !list.length) return;
    seedMetrics(list.map((t) => t.id)).then((res) => {
      if (overviewSeeded) return;
      overviewSeeded = true;
      const sum = new Map();
      for (const [, pts] of res) for (const p of pts) { const k = Math.round(p.t / 2000) * 2000; sum.set(k, (sum.get(k) || 0) + (p.rate || 0)); }
      seedOverviewHist([...sum.entries()].sort((a, b) => a[0] - b[0]).slice(-150).map(([t, rate]) => ({ t, rate })));
    });
    Promise.all(list.slice(0, 12).map((t) => api.logs(t.id, 8).catch(() => []))).then((r) => setLogSeed(r.flat()));
  }, [tasks.data && tasks.data.length]);

  const o = lv.overview || ov.data;
  const by = (o && o.tasks && o.tasks.byState) || {};
  const order = ['streaming', 'snapshotting', 'resyncing', 'starting', 'retrying', 'held', 'failed', 'paused', 'stopped', 'completed'];
  const parts = order.filter((s) => by[s]).map((s) => ({ label: STATES[s].label, value: by[s], tone: STATES[s].tone }));
  const sorted = useMemo(() => [...(tasks.data || [])].sort((a, b) => {
    const rank = (t) => { const s = (statusOf(t) || {}).state; return isActive(s) ? 0 : s === 'failed' || s === 'held' ? 1 : 2; };
    return rank(a) - rank(b) || String(a.name).localeCompare(String(b.name));
  }), [tasks.data, lv.taskSeq]);

  return html`
    <${PageHeader} title="Overview" subtitle="Live state of every change-data-capture pipeline feeding Meilisearch."
      actions=${html`<a class="btn btn-secondary" href="#/tasks"><${Icon} name="tasks" /><span>All tasks</span></a>
        ${can('task.create') && html`<a class="btn btn-primary keep-label" href="#/tasks/new"><${Icon} name="plus" /><span>New task</span></a>`}`} />
    <${GettingStarted} />
    <${ErrorBox} error=${!o && ov.error} onRetry=${() => ov.reload()} />
    ${!o ? html`<${KpiSkeleton} />` : html`<div class="kpis stagger">
      <${Kpi} i=${0} icon="tasks" label="Tasks" testid="kpi-tasks" sub=${html`<div class="stack-sm" style="width:100%">
          <${StackBar} parts=${parts} label="Tasks by state" />
          <div class="legend">${parts.slice(0, 4).map((p) => html`<span class="legend-item"><span class=${`legend-dot tone-bg-${p.tone}`}></span>${p.value} ${p.label.toLowerCase()}</span>`)}</div></div>`}>
        <${Counter} value=${o.tasks.total} />
      </${Kpi}>
      <${Kpi} i=${1} icon="zap" label="Throughput" testid="kpi-rate" sub=${html`<span>events per second, all tasks</span>`}>
        <${Counter} value=${o.rate} format=${fmtRate} /><small>/s</small>
      </${Kpi}>
      <${Kpi} i=${2} icon="upload" label="Documents" alt testid="kpi-upserted" sub=${html`<span><${Counter} value=${o.totals.deleted} format=${fmtCompact} /> deleted · <${Counter} value=${o.totals.events} format=${fmtCompact} /> events</span>`}>
        <${Counter} value=${o.totals.upserted} format=${fmtCompact} /><small>upserted</small>
      </${Kpi}>
      <${AlertsKpi} i=${3} />
      <${Kpi} i=${4} icon="inbox" label="Dead letters" testid="kpi-dlq" sub=${html`<span>${fmtInt(o.totals.errors)} errors · ${fmtInt(o.totals.retries)} retries</span>`}>
        <${Counter} value=${o.totals.dlq} />
      </${Kpi}>
    </div>
    <${DestinationsStrip} list=${o.destinations} />`}

    <div class="split" style="margin-top:16px">
      <section class="card" aria-labelledby="tp-title">
        <div class="card-head"><h2 id="tp-title"><${Icon} name="activity" />Throughput</h2>
          <span class="subtle num" style="font-size:12.5px">${o ? `${fmtRate(o.rate)} events/s now` : ''}</span></div>
        <div class="card-body"><${AreaChart} label="Events per second across all tasks" height=${250} format=${fmtRate}
          series=${[{ name: 'events/s', tone: 'primary', points: lv.overviewHist.map((p) => ({ t: p.t, v: p.rate })) }]} /></div>
      </section>
      <section class="card" aria-labelledby="act-title">
        <div class="card-head"><h2 id="act-title"><${Icon} name="terminal" />Recent activity</h2></div>
        <${ActivityFeed} seed=${logSeed} />
      </section>
    </div>

    <div class="section-title"><h2>Pipelines</h2><a href="#/tasks" class="btn btn-ghost btn-sm"><span>Manage</span><${Icon} name="arrow-right" size=${15} /></a></div>
    ${tasks.error && !tasks.data ? html`<${ErrorBox} error=${tasks.error} onRetry=${() => tasks.reload()} />`
      : !tasks.data ? html`<div class="task-cards">${[0, 1, 2].map((i) => html`<div class="card skeleton-card" key=${i}><${Skeleton} w="60%" h=${16} /><${Skeleton} w="40%" h=${12} /><${Skeleton} h=${34} /></div>`)}</div>`
      : !sorted.length ? html`<div class="card"><${Empty} icon="plug" title="No tasks yet" action=${can('task.create') && html`<a class="btn btn-primary" href="#/tasks/new"><${Icon} name="plus" /><span>Create your first task</span></a>`}>
          Connect a database and stream its changes into a Meilisearch index.</${Empty}></div>`
      : html`<div class="task-cards stagger">${sorted.map((t, i) => html`<${TaskCard} key=${t.id} task=${t} i=${i} dests=${dests} />`)}</div>`}
  `;
}
