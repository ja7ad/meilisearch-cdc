// Task detail: header actions + tabs (overview, logs, schema, diff, DLQ, config).
import { html, useEffect, useMemo, useRef, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { ChangeList, SettingsEditor } from '../settings-editor.js';
import { activeAlerts, live, useLive } from '../sse.js';
import { can, gate } from '../auth.js';
import { destinationsSnapshot, useDestinations } from '../destinations.js';
import { DestinationBadge } from './destinations.js';
import { useFetch, useInterval, useTick } from '../hooks.js';
import { navigate } from '../router.js';
import { AreaChart } from '../charts.js';
import { SourceIcon, deleteTaskFlow, runTaskAction, seedMetrics } from '../components.js';
import { Icon } from '../icons.js';
import { signedCurl } from '../signing.js';
import { Alert, Button, CopyField, Counter, Empty, ErrorBox, JsonView, Modal, Progress, SkeletonRows, Skeleton, StateBadge, Tabs, Tag, Toggle, confirmDialog, toast, toastError } from '../ui.js';
import { ACTION_LABELS, actionsFor, cls, downloadJSON, fmtCompact, fmtInt, fmtLag, fmtPct, fmtRate, fmtTime, isActive, isZeroTime, sourceInfo, timeAgo } from '../util.js';

const ACTION_ICON = { start: 'play', stop: 'stop', pause: 'pause', resume: 'play', restart: 'restart', resync: 'resync', 'reset-checkpoint': 'reset-checkpoint' };

// ---------------------------------------------------------------- overview tab
function OverviewTab({ task, st }) {
  useLive();
  useTick(2000);
  useEffect(() => { seedMetrics([task.id]); }, [task.id]);
  const hist = live.hist[task.id] || [];
  const c = st.counters || {};
  const snap = st.snapshot || {};
  const snapping = (st.state === 'snapshotting' || st.state === 'resyncing') && snap.total > 0;
  const counters = [
    ['upserted', 'Upserted', 'upload'], ['deleted', 'Deleted', 'trash'], ['events', 'Events', 'activity'], ['batches', 'Batches', 'layers'],
    ['coalesced', 'Coalesced', 'compact'], ['errors', 'Errors', 'error'], ['retries', 'Retries', 'restart'], ['dlq', 'Dead letters', 'inbox'],
  ];
  return html`<div class="stack tab-panel">
    ${(st.error || st.state === 'retrying' || st.state === 'held') && html`<${Alert} tone=${st.state === 'retrying' ? 'warn' : 'bad'}
        title=${st.state === 'retrying' ? `Retrying · attempt ${st.attempt || 1} of ${(task.sync && task.sync.retry && task.sync.retry.maxAttempts) || '?'}` : st.state === 'held' ? 'Held by a safety guard' : 'Task failed'}
        action=${(st.state === 'held' || st.state === 'failed') && can('task.action') ? html`<${Button} size="sm" icon="play" onClick=${() => runTaskAction(task, 'start')}>Start again</${Button}>` : null}>
      ${st.message && html`<div>${st.message}</div>`}${st.error && html`<code style="white-space:pre-wrap;background:none;padding:0">${st.error}</code>`}</${Alert}>`}

    ${snapping && html`<section class="card card-pad stack-sm" aria-label="Snapshot progress" data-testid="snapshot-progress">
      <div class="row-between"><h3>${st.state === 'resyncing' ? 'Resync' : 'Snapshot'} in progress</h3><b class="num">${fmtPct(snap.done / snap.total, 1)}</b></div>
      <${Progress} value=${snap.done} max=${snap.total} shimmer tone="primary" label="Snapshot progress" />
      <div class="row-between subtle" style="font-size:12.5px"><span><${Counter} value=${snap.done} /> of ${fmtInt(snap.total)} rows</span>
        <span>${st.rate > 0 ? `~${fmtLag(((snap.total - snap.done) / st.rate) * 1000)} remaining` : ''}</span></div>
    </section>`}

    <div class="counters stagger" data-testid="live-counters">
      ${counters.map(([k, l, ic], i) => html`<div class=${cls('counter', (k === 'errors' || k === 'dlq') && c[k] > 0 && 'tone-bad')} style=${`--i:${i}`} key=${k}>
        <span><${Icon} name=${ic} size=${13} />${l}</span><b data-counter=${k} style=${(k === 'errors' || k === 'dlq') && c[k] > 0 ? 'color:var(--bad-text)' : ''}><${Counter} value=${c[k] || 0} /></b></div>`)}
    </div>

    <div class="grid grid-2">
      <section class="card"><div class="card-head"><h3><${Icon} name="zap" />Rate</h3><span class="num subtle" data-testid="live-rate">${fmtRate(st.rate || 0)} docs/s</span></div>
        <div class="card-body"><${AreaChart} label="Documents per second" height=${200} format=${fmtRate} series=${[{ name: 'docs/s', tone: 'primary', points: hist.map((p) => ({ t: p.t, v: p.rate })) }]} /></div></section>
      <section class="card"><div class="card-head"><h3><${Icon} name="clock" />Lag</h3><span class="num subtle">${isActive(st.state) ? fmtLag(st.lagMs) : '—'}</span></div>
        <div class="card-body"><${AreaChart} label="Replication lag" height=${200} format=${(v) => fmtLag(v)} series=${[{ name: 'lag', tone: 'violet', points: hist.map((p) => ({ t: p.t, v: p.lagMs })) }]} /></div></section>
    </div>

    ${task.source.type === 'webhook' && html`<${PushEndpoint} task=${task} />`}

    <section class="card"><div class="card-head"><h3><${Icon} name="reset-checkpoint" />Runtime</h3></div>
      <dl class="kv card-body">
        <dt>Checkpoint</dt><dd class="mono" data-testid="checkpoint">${st.checkpoint || html`<span class="subtle">none</span>`}</dd>
        <dt>Started</dt><dd>${isZeroTime(st.startedAt) ? '—' : html`${fmtTime(st.startedAt, true)} <span class="subtle">(${timeAgo(st.startedAt)})</span>`}</dd>
        <dt>Last event</dt><dd>${timeAgo(st.lastEventAt)}</dd>
        <dt>In flight</dt><dd>${st.inflight || 0} of ${(task.sync && task.sync.maxInflight) || '—'} Meilisearch tasks</dd>
        ${snap.total > 0 && !snapping && html`<dt>Snapshot</dt><dd>${fmtInt(snap.done)} / ${fmtInt(snap.total)} rows</dd>`}
        ${st.message && html`<dt>Message</dt><dd>${st.message}</dd>`}
        <dt>Updated</dt><dd>${timeAgo(st.updatedAt)}</dd>
      </dl>
    </section>
  </div>`;
}

/** Where and how a webhook task receives pushes. */
function PushEndpoint({ task }) {
  const url = `${location.origin}/api/v1/ingest/${task.id}`;
  const signed = !!task.source.webhookSecret;
  return html`<section class="card" data-testid="push-endpoint">
    <div class="card-head"><h3><${Icon} name="webhook" />Push endpoint</h3>
      ${signed ? html`<span class="badge tone-ok" data-testid="push-signed"><${Icon} name="shield" size=${13} />Signed requests only</span>`
        : html`<span class="badge tone-warn" data-testid="push-unsigned"><${Icon} name="key" size=${13} />API token</span>`}</div>
    <div class="card-body stack-sm">
      <${CopyField} value=${url} label="Ingest URL" testid="push-url" />
      <p class="muted">${signed
        ? html`Requests must carry <code>X-CDC-Timestamp</code> and <code>X-CDC-Signature</code> made with the task's signing secret; unsigned, stale or tampered requests get 401. Rotate the secret from Edit.`
        : html`Requests need a moderator API token (<code>Authorization: Bearer …</code>). Add a signing secret from Edit so senders do not need a token.`}</p>
      ${signed && html`<details class="details-block"><summary>Signed request example</summary><${CopyField} multiline label="Signed request example" value=${signedCurl(url)} /></details>`}
    </div>
  </section>`;
}

// ---------------------------------------------------------------- logs tab
const LEVELS = ['all', 'debug', 'info', 'warn', 'error'];
const LV_TONE = { debug: 'muted', info: 'sync', warn: 'warn', warning: 'warn', error: 'bad' };
function LogsTab({ task }) {
  const lv = useLive();
  const { data, error, reload } = useFetch(() => api.logs(task.id, 200), [task.id]);
  const [level, setLevel] = useState('all');
  const [q, setQ] = useState('');
  const [follow, setFollow] = useState(true);
  const box = useRef(null);
  const lines = useMemo(() => {
    const base = data || [];
    const last = base.length ? Date.parse(base[base.length - 1].time) : 0;
    const extra = lv.logs.filter((l) => l.taskId === task.id && Date.parse(l.time) > last);
    const rank = { debug: 0, info: 1, warn: 2, warning: 2, error: 3 };
    const needle = q.toLowerCase();
    return [...base, ...extra].filter((l) => (level === 'all' || (rank[l.level] ?? 1) >= rank[level]) && (!needle || l.message.toLowerCase().includes(needle))).slice(-500);
  }, [data, lv.logSeq, level, q]);
  useEffect(() => { if (follow && box.current) box.current.scrollTop = box.current.scrollHeight; }, [lines.length, follow]);
  return html`<section class="card tab-panel">
    <div class="card-head">
      <div class="seg" role="group" aria-label="Minimum level">${LEVELS.map((l) => html`<button type="button" aria-pressed=${level === l} onClick=${() => setLevel(l)}>${l}</button>`)}</div>
      <div class="row">
        <div class="input-icon"><${Icon} name="search" size=${15} /><input class="input input-sm" type="search" placeholder="Filter messages" aria-label="Filter log messages" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} /></div>
        <${Toggle} id="log-follow" label="Auto-scroll" checked=${follow} onChange=${setFollow} />
        <${Button} size="sm" kind="ghost" icon="restart" aria-label="Reload logs" onClick=${() => reload()} />
      </div>
    </div>
    <${ErrorBox} error=${error} />
    <div class="log-box" ref=${box} role="log" aria-live="polite" aria-label="Task logs" data-testid="log-box"
      onScroll=${(e) => { const el = e.currentTarget; const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 30; if (!atBottom && follow) setFollow(false); }}>
      ${!data ? html`<div style="padding:16px" class="stack-sm">${[0, 1, 2, 3, 4].map((i) => html`<${Skeleton} key=${i} w=${`${50 + i * 9}%`} />`)}</div>`
        : !lines.length ? html`<${Empty} icon="terminal" title="No log lines">${q || level !== 'all' ? 'Nothing matches the filter.' : 'Logs appear here as the task runs.'}</${Empty}>`
        : lines.map((l, i) => html`<div class=${`log-line tone-${LV_TONE[l.level] || 'muted'}`} key=${`${l.time}${i}`}>
            <span class="log-time">${fmtTime(l.time)}</span><span class="log-lvl">${l.level}</span><span class="log-msg">${l.message}</span></div>`)}
    </div>
  </section>`;
}

// ---------------------------------------------------------------- schema tab
function SchemaTab({ task }) {
  const { data, error, loading, reload } = useFetch(() => api.schemaCheck(task.id), [task.id]);
  if (error && !data) return html`<${ErrorBox} error=${error} onRetry=${() => reload()} />`;
  if (!data) return html`<div class="card"><${SkeletonRows} rows=${6} cols=${5} /></div>`;
  const pk = data.primaryKey || {};
  const tone = { error: 'bad', warning: 'warn', warn: 'warn', info: 'info' };
  return html`<div class="stack tab-panel" data-testid="schema-check">
    <div class="grid grid-2">
      <section class="card card-pad stack-sm">
        <div class="row-between"><h3>Primary key</h3>${pk.ok ? html`<${Tag} tone="ok">consistent</${Tag}>` : html`<${Tag} tone="bad">mismatch</${Tag}>`}</div>
        <div class="row mono" style="gap:8px">${(pk.source || []).map((c) => html`<${Tag}>${c}</${Tag}>`)}<${Icon} name="arrow-right" size=${15} /><${Tag} tone="primary">${pk.field}</${Tag}>
          <span class="subtle">index:</span><${Tag} tone=${pk.ok ? 'violet' : 'bad'}>${pk.index || 'unset'}</${Tag}></div>
      </section>
      <section class="card card-pad stack-sm">
        <div class="row-between"><h3>Issues</h3><${Button} size="sm" kind="ghost" icon="restart" loading=${loading} onClick=${() => reload()}>Re-check</${Button}></div>
        ${data.issues && data.issues.length ? html`<ul class="issue-list">${data.issues.map((i) => html`<li><${Alert} tone=${tone[i.level] || 'info'}>${i.message}</${Alert}></li>`)}</ul>`
          : html`<p class="muted">No issues found.</p>`}
        <p class="subtle" style="font-size:12px">Checked ${timeAgo(data.checkedAt)}</p>
      </section>
    </div>
    <section class="card"><div class="card-head"><h3><${Icon} name="database" />Fields</h3><span class="subtle">${data.fields.length} fields · index <span class="mono">${data.indexUid}</span></span></div>
      <div class="table-wrap"><table class="table table-compact rtable" data-testid="schema-fields">
        <thead><tr><th>Field</th><th class="hide-sm">Source column</th><th>Coverage</th><th>Settings</th><th class="hide-sm">Notes</th></tr></thead>
        <tbody>${data.fields.map((f, i) => html`<tr key=${f.name} style=${`--i:${Math.min(i, 12)}`}>
          <td class="rt-title"><span class="mono" style="font-weight:600">${f.name}</span>${!f.inIndex && html` <${Tag} tone="warn">not indexed</${Tag}>`}${!f.inSource && html` <${Tag} tone="bad">not in source</${Tag}>`}</td>
          <td data-label="Source" class="hide-sm mono subtle">${f.sourceColumn || '—'} ${f.sourceType && html`<small>(${f.sourceType})</small>`}</td>
          <td data-label="Coverage"><div class="coverage"><${Progress} value=${f.coverage} max=${1} tone=${f.coverage >= 0.95 ? 'ok' : f.coverage >= 0.6 ? 'warn' : 'bad'} label=${`Coverage of ${f.name}`} /><span>${fmtPct(f.coverage)}</span></div></td>
          <td data-label="Settings"><div class="tags">${(f.settings || []).map((x) => html`<${Tag} tone=${x === 'filterable' ? 'primary' : x === 'sortable' ? 'violet' : 'neutral'}>${x}</${Tag}>`)}</div></td>
          <td data-label="Notes" class="hide-sm rt-wrap">${(f.issues || []).map((x) => html`<div style="font-size:12.5px;color:var(--warn-text)">${x}</div>`)}</td>
        </tr>`)}</tbody>
      </table></div></section>
  </div>`;
}

// ---------------------------------------------------------------- diff tab
function DiffTab({ task }) {
  const [report, setReport] = useState(undefined);
  const [error, setError] = useState(null);
  const [limit, setLimit] = useState(50);
  const [compare, setCompare] = useState(true);
  const [repairing, setRepairing] = useState('');
  const load = async () => {
    try { const r = await api.diff(task.id); setReport(r && r.status !== 'none' ? r : null); setError(null); } catch (e) { if (e.status === 404) setReport(null); else setError(e); }
  };
  useEffect(() => { load(); }, [task.id]);
  const running = report && report.status === 'running';
  useInterval(load, running ? 1000 : 0);
  const prevStatus = useRef(null);
  useEffect(() => {
    if (prevStatus.current === 'running' && report && report.status === 'done') toast({ tone: report.inSync ? 'ok' : 'warn', title: report.inSync ? 'Index is in sync' : 'Differences found', message: report.inSync ? undefined : `${report.missing} missing · ${report.extra} extra · ${report.mismatched} mismatched` });
    prevStatus.current = report && report.status;
  }, [report && report.status]);

  const run = async () => {
    try { await api.runDiff(task.id, { sampleLimit: Number(limit) || 50, compareFields: compare }); setReport((r) => ({ ...(r || {}), status: 'running' })); load(); }
    catch (e) { toastError(e, 'Could not start diff'); }
  };
  const repair = async (kind) => {
    const opts = { missing: kind === 'missing' || kind === 'all', mismatched: kind === 'mismatched' || kind === 'all', extra: kind === 'extra' };
    if (opts.extra) {
      const ok = await confirmDialog({ title: 'Delete extra documents?', message: `${report.extra} documents exist in the index but not in the source. They will be deleted from “${task.index.uid}”.`, danger: true, confirmLabel: 'Delete extra' });
      if (!ok) return;
    }
    setRepairing(kind);
    try { const r = await api.repair(task.id, opts); toast({ title: 'Repair done', message: `${r.upserted} upserted · ${r.deleted} deleted` }); load(); }
    catch (e) { toastError(e, 'Repair failed'); }
    setRepairing('');
  };

  const r = report;
  return html`<div class="stack tab-panel" data-testid="diff-tab">
    <section class="card card-pad row-between">
      <div class="stack-sm" style="max-width:560px"><h3>Compare source and index</h3>
        <p class="muted">Reads every id from the source and the index, then compares a sample of documents field by field.</p></div>
      <div class="row">
        <label class="check-label">Sample <input class="input input-sm" style="width:80px" type="number" min="1" max="1000" value=${limit} onInput=${(e) => setLimit(e.currentTarget.value)} aria-label="Sample limit" /></label>
        <label class="check-label"><input type="checkbox" class="checkbox" checked=${compare} onChange=${(e) => setCompare(e.currentTarget.checked)} />Compare fields</label>
        <${Button} kind="primary" icon="diff" loading=${running} onClick=${run} ...${gate('task.diff')} data-testid="run-diff">${running ? 'Running…' : 'Run diff'}</${Button}>
      </div>
    </section>
    <${ErrorBox} error=${error} onRetry=${load} />
    ${r === undefined ? html`<div class="card"><${SkeletonRows} rows=${3} cols=${4} /></div>`
      : r === null ? html`<div class="card"><${Empty} icon="diff" title="No diff yet">Run a diff to check that the index matches the source.</${Empty}></div>`
      : running ? html`<section class="card card-pad stack-sm"><div class="row"><span class="spinner"></span><b>Comparing…</b><span class="subtle">started ${timeAgo(r.startedAt)}</span></div>
          <${Progress} value=${1} max=${1} shimmer tone="primary" label="Diff running" /></section>`
      : r.status === 'failed' ? html`<${Alert} tone="bad" title="Diff failed">${r.error}</${Alert}>`
      : html`<div class="stack">
          <${Alert} tone=${r.inSync ? 'ok' : 'warn'} title=${r.inSync ? 'In sync' : 'Out of sync'}
            action=${!r.inSync && html`<${Button} size="sm" kind="primary" icon="check" loading=${repairing === 'all'} onClick=${() => repair('all')} ...${gate('task.diff')}>Repair missing & mismatched</${Button}>`}>
            Finished ${timeAgo(r.finishedAt)}.</${Alert}>
          <div class="diff-stats stagger">
            ${[['Source', r.sourceCount], ['Index', r.indexCount], ['Missing', r.missing, 'warn'], ['Extra', r.extra, 'warn'], ['Mismatched', r.mismatched, 'bad']].map(([l, v, t], i) => html`
              <div class=${cls('diff-stat', v > 0 && t && `tone-${t}`)} style=${`--i:${i}`}><span>${l}</span><b><${Counter} value=${v || 0} /></b></div>`)}
          </div>
          ${!r.inSync && html`<div class="grid grid-2">
            <section class="card"><div class="card-head"><h3>Missing in index</h3><${Button} size="sm" icon="upload" disabled=${!r.missing} loading=${repairing === 'missing'} onClick=${() => repair('missing')} ...${gate('task.diff')}>Upsert missing</${Button}></div>
              <div class="card-body">${r.samples.missing.length ? html`<div class="id-list">${r.samples.missing.map((id) => html`<${Tag} tone="warn">${id}</${Tag}>`)}</div>` : html`<p class="subtle">None</p>`}</div></section>
            <section class="card"><div class="card-head"><h3>Extra in index</h3><${Button} size="sm" kind="danger-ghost" icon="trash" disabled=${!r.extra} loading=${repairing === 'extra'} onClick=${() => repair('extra')} ...${gate('task.diff')}>Delete extra</${Button}></div>
              <div class="card-body">${r.samples.extra.length ? html`<div class="id-list">${r.samples.extra.map((id) => html`<${Tag}>${id}</${Tag}>`)}</div>` : html`<p class="subtle">None</p>`}</div></section>
          </div>`}
          ${r.samples.mismatched && r.samples.mismatched.length > 0 && html`<section class="card"><div class="card-head"><h3>Field mismatches</h3>
              <${Button} size="sm" icon="resync" disabled=${!r.mismatched} loading=${repairing === 'mismatched'} onClick=${() => repair('mismatched')} ...${gate('task.diff')}>Re-sync mismatched</${Button}></div>
            <div class="card-body stack-sm">${r.samples.mismatched.map((m) => html`<div class="mismatch" data-testid="mismatch">
              <div class="mismatch-head"><${Icon} name="doc" size=${15} />Document <span class="mono">${m.id}</span></div>
              <table class="table table-compact"><thead><tr><th>Field</th><th>Source</th><th>Index</th></tr></thead>
                <tbody>${m.fields.map((f) => html`<tr><td>${f.field}</td><td class="val-src">${JSON.stringify(f.source)}</td><td class="val-idx">${JSON.stringify(f.index)}</td></tr>`)}</tbody></table>
            </div>`)}</div></section>`}
        </div>`}
  </div>`;
}

// ---------------------------------------------------------------- DLQ tab
function DlqTab({ task }) {
  const { data, error, reload } = useFetch(() => api.dlq(task.id, 100), [task.id]);
  const [sel, setSel] = useState(new Set());
  const [view, setView] = useState(null);
  const [busy, setBusy] = useState('');
  const list = data || [];
  const all = list.length > 0 && list.every((e) => sel.has(e.id));
  const toggle = (id) => { const s = new Set(sel); s.has(id) ? s.delete(id) : s.add(id); setSel(s); };
  const ids = [...sel];
  const replay = async () => {
    setBusy('replay');
    try { const r = await api.dlqReplay(task.id, ids); toast({ tone: r.failed ? 'warn' : 'ok', title: `Replayed ${r.replayed} document${r.replayed === 1 ? '' : 's'}`, message: r.failed ? `${r.failed} failed again and stay in the queue` : undefined }); setSel(new Set()); reload(true); }
    catch (e) { toastError(e, 'Replay failed'); }
    setBusy('');
  };
  const discard = async () => {
    const n = ids.length || list.length;
    const ok = await confirmDialog({ title: `Discard ${n} dead letter${n === 1 ? '' : 's'}?`, message: 'The rejected documents are removed from the queue and will not be retried.', danger: true, confirmLabel: 'Discard' });
    if (!ok) return;
    setBusy('discard');
    try { const r = await api.dlqDelete(task.id, ids); toast({ title: `Discarded ${r.deleted}` }); setSel(new Set()); reload(true); }
    catch (e) { toastError(e, 'Discard failed'); }
    setBusy('');
  };
  return html`<section class="card tab-panel" data-testid="dlq-tab">
    <div class="card-head"><h3><${Icon} name="inbox" />Dead letter queue</h3>
      <div class="btn-group">
        <${Button} size="sm" icon="restart" disabled=${!list.length} loading=${busy === 'replay'} onClick=${replay} ...${gate('task.dlq')}>${ids.length ? `Replay ${ids.length}` : 'Replay all'}</${Button}>
        <${Button} size="sm" kind="danger-ghost" icon="trash" disabled=${!list.length} loading=${busy === 'discard'} onClick=${discard} ...${gate('task.dlq')}>${ids.length ? `Discard ${ids.length}` : 'Discard all'}</${Button}>
      </div></div>
    <${ErrorBox} error=${error} onRetry=${() => reload()} />
    ${!data ? html`<${SkeletonRows} rows=${4} cols=${5} />`
      : !list.length ? html`<${Empty} icon="success" title="Queue is empty">Documents Meilisearch rejects land here so the pipeline keeps flowing.</${Empty}>`
      : html`<div class="table-wrap"><table class="table table-compact rtable" data-testid="dlq-table">
        <thead><tr>${can('task.dlq') && html`<th class="col-check"><input type="checkbox" class="checkbox" checked=${all} onChange=${() => setSel(all ? new Set() : new Set(list.map((e) => e.id)))} aria-label="Select all dead letters" /></th>`}
          <th>Document</th><th>Error</th><th class="hide-sm num-col">Attempts</th><th class="hide-sm">Created</th><th class="col-actions"><span class="sr-only">View</span></th></tr></thead>
        <tbody>${list.map((e, i) => html`<tr key=${e.id} class=${sel.has(e.id) ? 'is-selected' : ''} style=${`--i:${Math.min(i, 12)}`}>
          ${can('task.dlq') && html`<td class="col-check"><input type="checkbox" class="checkbox" checked=${sel.has(e.id)} onChange=${() => toggle(e.id)} aria-label=${`Select dead letter ${e.docId}`} /></td>`}
          <td class="rt-title"><span class="mono" style="font-weight:600">${e.docId}</span> <${Tag} tone=${e.op === 'delete' ? 'bad' : 'violet'}>${e.op}</${Tag}></td>
          <td data-label="Error" class="rt-wrap"><span class="err-text" title=${e.error}>${e.error}</span>${e.code && html`<span class="subtle mono" style="font-size:11.5px">${e.code}</span>`}</td>
          <td data-label="Attempts" class="hide-sm num-col">${e.attempts}</td><td data-label="Created" class="hide-sm subtle nowrap">${timeAgo(e.createdAt)}</td>
          <td class="col-actions"><${Button} size="sm" kind="ghost" icon="eye" aria-label=${`View document ${e.docId}`} onClick=${() => setView(e)} /></td>
        </tr>`)}</tbody></table></div>`}
    <${Modal} open=${!!view} onClose=${() => setView(null)} title=${view ? `Dead letter · ${view.docId}` : ''} size="lg">
      ${view && html`<${Alert} tone="bad" title=${view.code || 'Rejected'}>${view.error}</${Alert}><${JsonView} value=${view.document || {}} maxHeight=${400} label="Rejected document" />`}
    </${Modal}>
  </section>`;
}

// ---------------------------------------------------------------- index settings tab
function TaskSettingsTab({ task, onTaskChanged }) {
  const { data, error, reload, loading } = useFetch(() => api.taskSettings(task.id), [task.id]);
  const schema = useFetch(() => api.schemaCheck(task.id).catch(() => null), [task.id]);
  const [draft, setDraft] = useState(null);
  const [busy, setBusy] = useState('');
  useEffect(() => { if (data) setDraft(data.desired || {}); }, [data]);
  const admin = can('task.edit');
  const dirty = data && draft && JSON.stringify(draft) !== JSON.stringify(data.desired || {});
  const saveDesired = async (settings, label) => {
    setBusy(label);
    try {
      const full = await api.task(task.id);
      const body = { ...full };
      delete body.status;
      body.index = { ...body.index, settings: Object.keys(settings).length ? settings : undefined };
      const v = await api.updateTask(task.id, body);
      toast({ title: label === 'import' ? 'Live settings imported into the task' : 'Task settings saved' });
      onTaskChanged && onTaskChanged(v);
      reload(true);
    } catch (e) { toastError(e, e.status === 422 ? 'Invalid index settings' : 'Could not save settings'); }
    setBusy('');
  };
  const apply = async () => {
    setBusy('apply');
    try { const r = await api.applyTaskSettings(task.id); toast({ title: r.inSync ? 'Settings applied: in sync' : 'Settings applied', message: `Index ${r.indexUid}` }); reload(true); }
    catch (e) { toastError(e, 'Apply failed'); }
    setBusy('');
  };
  const importLive = async () => {
    const ok = await confirmDialog({ title: 'Import live settings into the task?', message: 'The task’s desired settings are replaced by the index’s current settings.', confirmLabel: 'Import' });
    if (ok) await saveDesired(data.live || {}, 'import');
  };
  if (error && !data) return html`<${ErrorBox} error=${error} onRetry=${() => reload()} />`;
  if (!data || !draft) return html`<div class="card card-pad stack">${[0, 1, 2].map((i) => html`<${Skeleton} key=${i} h=${40} />`)}</div>`;
  const fields = [...((schema.data && schema.data.fields) || []).map((f) => f.name), ...Object.keys((data.live && data.live.fieldDistribution) || {})];
  return html`<div class="stack tab-panel" data-testid="task-settings">
    <section class=${cls('card drift-card', data.inSync ? 'is-sync' : 'is-drift')}>
      <div class="card-head">
        <h3>${!data.indexExists ? html`<${Tag} tone="warn">index missing</${Tag}>` : data.inSync ? html`<${Tag} tone="ok">in sync</${Tag}>` : html`<${Tag} tone="warn">drift · ${data.changes.length}</${Tag}>`}
          <span>Index <span class="mono">${data.indexUid}</span></span>${!data.manageSettings && html`<span class="tag" title="The task does not apply settings on start">not managed</span>`}</h3>
        <div class="btn-group">
          <${Button} size="sm" kind="ghost" icon="restart" loading=${loading} onClick=${() => reload()} aria-label="Refresh settings status">Refresh</${Button}>
          ${admin && html`<${Button} size="sm" icon="download" onClick=${importLive} loading=${busy === 'import'} disabled=${!data.live} data-testid="import-live">Import live settings</${Button}>
            <${Button} size="sm" kind="primary" icon="check" onClick=${apply} loading=${busy === 'apply'} disabled=${data.inSync || dirty} title=${dirty ? 'Save your edits first' : undefined} data-testid="apply-now">Apply now</${Button}>`}
        </div>
      </div>
      <div class="card-body">
        ${data.inSync ? html`<p class="muted">The live index matches every setting this task manages.</p>`
          : data.changes.length ? html`<${ChangeList} changes=${data.changes} testid="drift-changes" currentLabel="Live" desiredLabel="Desired" />`
          : html`<p class="muted">The index does not exist yet; it is created with these settings on the first write.</p>`}
      </div>
    </section>
    <section class="card">
      <div class="card-head"><h3><${Icon} name="settings" />Desired settings</h3>
        ${admin ? html`<div class="btn-group">${dirty && html`<span class="tag tag-primary unsaved">unsaved changes</span>`}
          <${Button} size="sm" onClick=${() => setDraft(data.desired || {})} disabled=${!dirty}>Revert</${Button}>
          <${Button} size="sm" kind="primary" icon="check" loading=${busy === 'save'} disabled=${!dirty} onClick=${() => saveDesired(draft, 'save')} data-testid="save-task-settings">Save</${Button}></div>`
          : html`<span class="tag" title="Requires admin role">read only</span>`}</div>
      <div class="card-body"><${SettingsEditor} value=${draft} onChange=${setDraft} idPrefix="ts" suggestions=${fields} readOnly=${!admin} jsonTestId="task-settings-json" /></div>
    </section>
  </div>`;
}

// ---------------------------------------------------------------- config tab
function ConfigTab({ task }) {
  const clean = { ...task };
  delete clean.status;
  return html`<section class="card tab-panel">
    <div class="card-head"><h3><${Icon} name="code" />Task definition</h3>
      <div class="btn-group"><${Button} size="sm" icon="download" onClick=${() => downloadJSON(`${task.id}.json`, [clean])}>Export</${Button}>
        ${can('task.edit') && html`<a class="btn btn-primary btn-sm" href=${`#/tasks/${encodeURIComponent(task.id)}/edit`}><${Icon} name="edit" size=${15} /><span>Edit</span></a>`}</div></div>
    <div class="card-body stack-sm"><p class="subtle">Secrets are redacted. Saving through the editor keeps the stored secrets.</p>
      <${JsonView} value=${clean} label="Task definition" /></div>
  </section>`;
}

// ---------------------------------------------------------------- page
export function TaskDetailPage({ id, tab }) {
  const lv = useLive();
  useDestinations();
  const { data: task, error, reload, setData } = useFetch(() => api.task(id), [id]);
  const conn = useFetch(() => (task && task.source.connectorId ? api.connector(task.source.connectorId).catch(() => null) : Promise.resolve(null)), [task && task.source.connectorId]);
  const [busy, setBusy] = useState('');
  const [dlqCount, setDlqCount] = useState(null);

  useEffect(() => {
    const ev = lv.lastTaskEvent;
    if (ev && ev.id === id) { if (ev.action === 'deleted') { toast({ tone: 'warn', title: 'This task was deleted' }); navigate('#/tasks'); } else reload(true); }
  }, [lv.taskSeq]);
  useEffect(() => { api.dlq(id, 100).then((l) => setDlqCount(l.length)).catch(() => {}); }, [id, tab]);

  if (error && !task) return html`<a class="back-link" href="#/tasks"><${Icon} name="arrow-left" size=${15} />Tasks</a><div style="margin-top:16px"><${ErrorBox} error=${error} onRetry=${() => reload()} /></div>`;
  if (!task) return html`<div class="stack"><${Skeleton} w=${80} h=${12} /><div class="row"><${Skeleton} w=${48} h=${48} r=${14} /><${Skeleton} w=${260} h=${26} /></div><div class="card"><${SkeletonRows} rows=${4} cols=${4} /></div></div>`;

  const st = live.status[task.id] || task.status || {};
  const taskAlerts = activeAlerts().filter((e) => e.taskId === task.id);
  const dests = destinationsSnapshot();
  const act = async (a) => { setBusy(a); const v = await runTaskAction(task, a); setBusy(''); if (v) setData(v); };
  const del = async () => { if (await deleteTaskFlow(task)) navigate('#/tasks'); };
  const tabs = [
    { id: 'overview', label: 'Overview', icon: 'overview' }, { id: 'logs', label: 'Logs', icon: 'terminal' },
    { id: 'schema', label: 'Schema check', icon: 'shield' }, { id: 'diff', label: 'Diff', icon: 'diff' },
    { id: 'dlq', label: 'DLQ', icon: 'inbox', count: dlqCount }, { id: 'settings', label: 'Index settings', icon: 'settings' }, { id: 'config', label: 'Config', icon: 'code' },
  ];
  const active = tabs.some((t) => t.id === tab) ? tab : 'overview';
  const src = sourceInfo(task.source.type);
  const Panel = { overview: OverviewTab, logs: LogsTab, schema: SchemaTab, diff: DiffTab, dlq: DlqTab, settings: TaskSettingsTab, config: ConfigTab }[active];

  return html`
    <a class="back-link" href="#/tasks"><${Icon} name="arrow-left" size=${15} />Tasks</a>
    <header class="detail-head" style="margin-top:10px">
      <div class="detail-title">
        <${SourceIcon} type=${task.source.type} size="lg" />
        <div style="min-width:0">
          <div class="row" style="gap:12px"><h1>${task.name || task.id}</h1><${StateBadge} state=${st.state} size="lg" />
            ${taskAlerts.length > 0 && html`<a href="#/alerts" class=${`badge badge-lg is-live tone-${taskAlerts.some((e) => e.severity === 'critical') ? 'bad' : taskAlerts.some((e) => e.severity === 'warning') ? 'warn' : 'info'}`}
              title=${taskAlerts.map((e) => e.ruleName).join(', ')} data-testid="task-alert-badge"><span class="dot" aria-hidden="true"></span>${taskAlerts.length} alert${taskAlerts.length > 1 ? 's' : ''} firing</a>`}</div>
          <div class="detail-meta"><span class="mono">${task.id}</span><span>${src.label} · <span class="mono">${task.source.table}</span></span>
            ${task.source.connectorId && html`<a class="connector-link" href=${`#/connectors/${encodeURIComponent(task.source.connectorId)}`} data-testid="task-connector"><${Icon} name="plug" size=${13} />${(conn.data && conn.data.name) || task.source.connectorId}</a>`}
            <span>→ <a class="mono" href=${`#/indexes/${encodeURIComponent(task.index.uid)}${task.index.destinationId ? `?destination=${encodeURIComponent(task.index.destinationId)}` : ''}`}>${task.index.uid}</a></span>
            <${DestinationBadge} list=${dests} id=${task.index.destinationId} />
            ${(task.tags || []).length > 0 && html`<span class="tags">${task.tags.map((t) => html`<${Tag}>${t}</${Tag}>`)}</span>`}</div>
          ${task.description && html`<p class="muted" style="margin-top:6px">${task.description}</p>`}
        </div>
      </div>
      <div class="btn-group" role="group" aria-label="Task actions">
        ${can('task.action') && actionsFor(st.state, task).map((a, i) => html`<${Button} key=${a} size="sm" kind=${i === 0 ? 'primary' : 'secondary'} icon=${ACTION_ICON[a]} loading=${busy === a} disabled=${!!busy} onClick=${() => act(a)} data-action=${a}>${ACTION_LABELS[a]}</${Button}>`)}
        ${can('task.edit') && html`<a class="btn btn-secondary btn-sm" href=${`#/tasks/${encodeURIComponent(task.id)}/edit`}><${Icon} name="edit" size=${15} /><span>Edit</span></a>`}
        ${can('task.delete') && html`<${Button} size="sm" kind="danger-ghost" icon="trash" onClick=${del} data-action="delete">Delete</${Button}>`}
        ${!can('task.action') && html`<span class="tag" title="Requires moderator role">read only</span>`}
      </div>
    </header>
    <${Tabs} tabs=${tabs} active=${active} label="Task sections" onChange=${(t) => navigate(`#/tasks/${encodeURIComponent(task.id)}${t === 'overview' ? '' : `/${t}`}`)} />
    <div role="tabpanel" aria-labelledby=${`tab-${active}`} key=${active}><${Panel} task=${task} st=${st} onTaskChanged=${setData} /></div>
  `;
}
