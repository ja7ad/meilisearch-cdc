// Meilisearch destinations: list, editor, test, default, detail with synced tasks.
import { html, useEffect, useLayoutEffect, useState } from '../../vendor/htm-preact.js';
import { api, setDestination } from '../api.js';
import { can } from '../auth.js';
import { destinationsChanged, hostOf, selectedDestination, singleDestination, useDestinations } from '../destinations.js';
import { useFetch, useTick } from '../hooks.js';
import { navigate } from '../router.js';
import { live, useLive } from '../sse.js';
import { Icon } from '../icons.js';
import { Alert, Button, Counter, Empty, ErrorBox, Field, Modal, PageHeader, Skeleton, SkeletonRows, StateBadge, Tabs, Tag, Toggle, confirmDialog, toast, toastError } from '../ui.js';
import { IndexCard } from './indexes.js';
import { MeiliTasksPage } from './meili-tasks.js';
import { cls, fmtBytes, fmtCompact, fmtInt, fmtLag, fmtRate, isDuration, isZeroTime, timeAgo } from '../util.js';

export const HEALTH_INTERVALS = [['5s', '5 seconds'], ['10s', '10 seconds'], ['15s', '15 seconds'], ['30s', '30 seconds (default)'], ['1m0s', '1 minute'], ['2m0s', '2 minutes'], ['5m0s', '5 minutes'], ['10m0s', '10 minutes']];
const normInterval = (v) => ({ '': '30s', 0: '30s', '1m': '1m0s', '2m': '2m0s', '5m': '5m0s', '10m': '10m0s' }[v] ?? v);
/** Latest health: the SSE "destination.health" event wins over the fetched snapshot. */
export const healthOf = (d) => (d && (live.destHealth[d.id] || d.health)) || {};

/** "checked 4s ago · next in 26s" with a countdown, re-rendered every second. */
export function CheckTimes({ h }) {
  useTick(1000);
  if (!h || !h.checkedAt) return null;
  const next = h.nextCheckAt ? Math.max(0, Math.round((Date.parse(h.nextCheckAt) - Date.now()) / 1000)) : null;
  return html`<span class="check-times subtle" data-testid="check-times">checked ${timeAgo(h.checkedAt)}${next != null ? html` · next in <span class="num">${next}s</span>` : ''}</span>`;
}

export function HealthDot({ healthy, label }) {
  const tone = healthy == null ? 'muted' : healthy ? 'ok' : 'bad';
  return html`<span class=${cls('health-dot', `tone-${tone}`, healthy && 'is-live')} role="img" aria-label=${label || (healthy == null ? 'Unknown health' : healthy ? 'Healthy' : 'Unreachable')}></span>`;
}

/** Badge naming a task's destination, shown when several destinations exist. */
export function DestinationBadge({ list, id }) {
  if (!list || list.length < 2) return null;
  const d = list.find((x) => x.id === id);
  if (!d) return null;
  return html`<span class="dest-badge" title=${`Meilisearch destination: ${d.name} (${hostOf(d.url)})`} data-testid="dest-badge">
    <${HealthDot} healthy=${d.health ? d.health.healthy : null} label=${`${d.name} ${d.health && d.health.healthy ? 'healthy' : 'unreachable'}`} />${d.name}</span>`;
}

/** Which destination a scoped page (indexes, Meilisearch tasks, instance settings) shows. */
export function ScopeBadge() {
  const list = useDestinations();
  if (!list || list.length < 2 || singleDestination()) return null;
  const d = selectedDestination(list);
  if (!d) return null;
  return html`<span class="scope-badge" data-testid="scope-badge"><${HealthDot} healthy=${d.health ? d.health.healthy : null} />on <b>${d.name}</b><span class="mono subtle">${hostOf(d.url)}</span></span>`;
}

// ---------------------------------------------------------------- editor
function TestResult({ r }) {
  if (!r) return null;
  return r.ok
    ? html`<${Alert} tone="ok" title="Connected" data-testid="destination-test">Meilisearch <b>v${r.version}</b> · <span class="num">${r.latencyMs} ms</span> · ${fmtInt(r.indexes)} index${r.indexes === 1 ? '' : 'es'}</${Alert}>`
    : html`<${Alert} tone="bad" title="Connection failed" data-testid="destination-test">${r.error}</${Alert}>`;
}

export function DestinationForm({ value, onChange, errors = {}, env, editing }) {
  const up = (k) => (e) => onChange({ ...value, [k]: e.currentTarget.value });
  return html`<div class="form-grid">
    <${Field} label="Name" required htmlFor="ds-name" error=${errors.name}><input id="ds-name" class="input" value=${value.name} placeholder="Production" onInput=${up('name')} /></${Field}>
    <${Field} label="Tags" htmlFor="ds-tags" hint="Comma separated."><input id="ds-tags" class="input" autocapitalize="off" value=${value.tags} placeholder="prod, eu" onInput=${up('tags')} /></${Field}>
    <${Field} label="URL" required htmlFor="ds-url" class="span-2" error=${errors.url} hint=${env ? 'Set by MEILI_URL; change it in the service environment.' : 'Base URL of the Meilisearch instance.'}>
      <input id="ds-url" class="input mono" type="url" inputmode="url" autocapitalize="off" spellcheck="false" value=${value.url} readonly=${env} placeholder="https://meili.example.com" onInput=${up('url')} /></${Field}>
    <${Field} label="API key" htmlFor="ds-key" class="span-2" hint=${env ? 'Set by MEILI_API_KEY.' : editing ? 'Write only. Leave the mask to keep the stored key.' : 'A key with access to the indexes the tasks write to (the master or an admin key).'}>
      <input id="ds-key" class="input mono" type="password" autocomplete="new-password" value=${value.apiKey} readonly=${env} onInput=${up('apiKey')} /></${Field}>
    <${Field} label="Timeout" htmlFor="ds-timeout" error=${errors.timeout} hint="Per request, e.g. 30s."><input id="ds-timeout" class="input mono" value=${value.timeout} onInput=${up('timeout')} /></${Field}>
    <${Field} label="Health check every" htmlFor="ds-interval" hint="How often the server checks this instance in the background.">
      <select id="ds-interval" class="select" value=${normInterval(value.healthCheckInterval || '')} onChange=${up('healthCheckInterval')}>
        ${!HEALTH_INTERVALS.some(([v]) => v === normInterval(value.healthCheckInterval || '')) && html`<option value=${value.healthCheckInterval}>${value.healthCheckInterval} (current)</option>`}
        ${HEALTH_INTERVALS.map(([v, l]) => html`<option value=${v}>${l}</option>`)}</select></${Field}>
    <div style="padding-top:26px"><${Toggle} id="ds-gzip" label="Gzip requests" hint="Compress document batches." checked=${value.gzip} onChange=${(v) => onChange({ ...value, gzip: v })} /></div>
    <${Field} label="Description" htmlFor="ds-desc" class="span-2"><input id="ds-desc" class="input" value=${value.description} onInput=${up('description')} /></${Field}>
  </div>`;
}

const blank = () => ({ name: '', url: '', apiKey: '', timeout: '30s', healthCheckInterval: '30s', gzip: true, tags: '', description: '' });
export const destFormErrors = (f) => ({
  ...(f.name.trim() ? {} : { name: 'Give the destination a name' }),
  ...(/^https?:\/\/\S+$/.test(f.url.trim()) ? {} : { url: 'Use an http(s) URL' }),
  ...(!f.timeout || isDuration(f.timeout) ? {} : { timeout: 'Use a duration such as 30s' }),
});
export const destBody = (f) => ({ name: f.name.trim(), url: f.url.trim(), apiKey: f.apiKey, timeout: f.timeout || '30s', healthCheckInterval: normInterval(f.healthCheckInterval || '30s'), gzip: f.gzip, tags: f.tags.split(',').map((x) => x.trim()).filter(Boolean), description: f.description.trim() });

function DestinationEditor({ open, dest, onClose, onSaved }) {
  const editing = !!dest;
  const [f, setF] = useState(blank());
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState('');
  const [err, setErr] = useState(null);
  const [test, setTest] = useState(null);
  useLayoutEffect(() => {
    if (!open) return;
    setF(dest ? { name: dest.name, url: dest.url, apiKey: dest.apiKey, timeout: dest.timeout || '30s', healthCheckInterval: dest.healthCheckInterval || '30s', gzip: dest.gzip !== false, tags: (dest.tags || []).join(', '), description: dest.description || '' } : blank());
    setTried(false); setErr(null); setTest(null); setBusy('');
  }, [open, dest && dest.id]);
  const errs = destFormErrors(f);
  const runTest = async () => {
    setTried(true);
    if (errs.url) return;
    setBusy('test'); setTest(null);
    try { setTest(await api.testDestinationDraft({ ...destBody(f), ...(editing ? { id: dest.id } : {}) })); } catch (e) { setTest({ ok: false, error: e.message }); }
    setBusy('');
  };
  const save = async () => {
    setTried(true);
    if (Object.keys(errs).length) return;
    setBusy('save'); setErr(null);
    try {
      const v = editing ? await api.updateDestination(dest.id, destBody(f)) : await api.createDestination(destBody(f));
      toast({ title: editing ? 'Destination saved' : 'Destination added', message: v.name });
      destinationsChanged(); onSaved(v); onClose();
    } catch (e) { setErr(e); setBusy(''); }
  };
  return html`<${Modal} open=${open} onClose=${onClose} title=${editing ? `Edit ${dest.name}` : 'New destination'} size="md"
    footer=${html`${can('connector.test') && html`<${Button} icon="plug" loading=${busy === 'test'} onClick=${runTest} data-testid="destination-test-btn">Test connection</${Button}>`}
      <span class="grow"></span><${Button} onClick=${onClose}>Cancel</${Button}>
      <${Button} kind="primary" icon="check" loading=${busy === 'save'} onClick=${save} data-testid="destination-save">${editing ? 'Save' : 'Add destination'}</${Button}>`}>
    <div class="stack">
      <${ErrorBox} error=${err} />
      ${dest && dest.managedByEnv && html`<${Alert} tone="info" title="Managed by the environment">URL and API key come from MEILI_URL / MEILI_API_KEY and are read-only here.</${Alert}>`}
      <${DestinationForm} value=${f} onChange=${(v) => { setF(v); setTest(null); }} errors=${tried ? errs : {}} env=${dest && dest.managedByEnv} editing=${editing} />
      <div aria-live="polite"><${TestResult} r=${test} /></div>
    </div>
  </${Modal}>`;
}

async function deleteDestinationFlow(d) {
  const ok = await confirmDialog({ title: `Delete destination “${d.name}”?`, message: 'The connection settings are removed. Indexes in that Meilisearch instance are not touched.', typed: d.name, danger: true, confirmLabel: 'Delete destination' });
  if (!ok) return false;
  try { await api.deleteDestination(d.id); toast({ title: 'Destination deleted', message: d.name }); destinationsChanged(); return true; }
  catch (e) {
    const used = (e.data && e.data.usedBy) || [];
    toast({ tone: 'bad', title: used.length ? 'Destination is in use' : 'Cannot delete', message: used.length ? `Used by ${used.map((t) => t.name || t.id).join(', ')}.` : e.message, timeout: 9000 });
    return false;
  }
}
export async function checkNow(d) {
  try {
    const h = await api.checkDestination(d.id);
    live.destHealth[d.id] = h;
    window.dispatchEvent(new CustomEvent('cdc:dest-health', { detail: { id: d.id, health: h } }));
    toast({ tone: h.healthy ? 'ok' : 'bad', title: `${d.name}: ${h.healthy ? 'healthy' : 'unreachable'}`, message: h.healthy ? `${h.latencyMs} ms` : h.error });
    return h;
  } catch (e) { toastError(e, 'Health check failed'); return null; }
}

async function makeDefault(d, done) {
  try { await api.makeDefaultDestination(d.id); toast({ title: `${d.name} is now the default`, message: 'Tasks without an explicit destination write here.' }); destinationsChanged(); done && done(); }
  catch (e) { toastError(e, 'Could not change the default'); }
}

// ---------------------------------------------------------------- list
function SyncLine({ sync }) {
  if (!sync) return null;
  return html`<div class="sync-line"><span><b class="num">${sync.running}</b> running</span>
    ${sync.failing > 0 && html`<span class="tone-bad" style="color:var(--bad-text)"><b class="num">${sync.failing}</b> failing</span>`}
    <span class="subtle">last sync ${isZeroTime(sync.lastSyncAt) ? 'never' : timeAgo(sync.lastSyncAt)}</span></div>`;
}

function DestinationCard({ d, i, onEdit, onChanged }) {
  useLive();
  const h = healthOf(d);
  const [checking, setChecking] = useState(false);
  const single = singleDestination();
  return html`<article class=${cls('card dest-card is-hoverable', !h.healthy && 'is-down')} style=${`--i:${i}`} data-destination=${d.id}>
    <div class="dest-head">
      <span class="dest-glyph"><${Icon} name="database" size=${20} /></span>
      <div class="grow" style="min-width:0"><a class="dest-name" href=${`#/destinations/${encodeURIComponent(d.id)}`}>${d.name}</a>
        <span class="mono subtle dest-host">${hostOf(d.url)}</span></div>
      <${HealthDot} healthy=${h.healthy} label=${`${d.name} ${h.healthy ? 'healthy' : 'unreachable'}`} />
    </div>
    <div class="tags">${d.isDefault && html`<span class="tag tag-primary" data-testid="default-badge">default</span>`}${d.managedByEnv && html`<span class="tag tag-violet" title="URL and key come from MEILI_URL / MEILI_API_KEY">managed by env</span>`}
      ${(d.tags || []).map((t) => html`<${Tag}>${t}</${Tag}>`)}</div>
    ${h.healthy ? html`<div class="dest-stats">
        <div class="mini-stat"><span>Version</span><b>v${h.version}</b></div>
        <div class="mini-stat"><span>Docs</span><b><${Counter} value=${h.documents} format=${fmtCompact} /></b></div>
        <div class="mini-stat"><span>Indexes</span><b>${fmtInt(h.indexes)}</b></div>
        <div class="mini-stat"><span>Latency</span><b>${h.latencyMs} ms</b></div></div>`
      : html`<p class="dest-error" title=${h.error}><${Icon} name="error" size=${14} />${h.error || 'Unreachable'}</p>`}
    <div class="row-between dest-check"><${CheckTimes} h=${h} />
      ${can('connector.test') && html`<${Button} size="sm" kind="ghost" icon="restart" loading=${checking} onClick=${async () => { setChecking(true); await checkNow(d); setChecking(false); }} aria-label=${`Check ${d.name} now`}>Check now</${Button}>`}</div>
    ${!h.healthy && h.consecutiveFailures > 0 && html`<p class="subtle" style="font-size:12px">${h.consecutiveFailures} failed check${h.consecutiveFailures > 1 ? 's' : ''} in a row${h.lastHealthyAt ? ` · last healthy ${timeAgo(h.lastHealthyAt)}` : ''}</p>`}
    <${SyncLine} sync=${d.sync} />
    <div class="connector-used">${(d.usedBy || []).length ? html`<span class="subtle">Used by</span> ${d.usedBy.slice(0, 3).map((t) => html`<a class="tag tag-primary" href=${`#/tasks/${encodeURIComponent(t.id)}`}>${t.name}</a>`)}${d.usedBy.length > 3 && html`<span class="subtle">+${d.usedBy.length - 3}</span>`}`
      : html`<span class="subtle">No task writes here yet</span>`}</div>
    ${can('index.write') && html`<div class="connector-actions">
      <${Button} size="sm" icon="edit" onClick=${() => onEdit(d)} aria-label=${`Edit ${d.name}`}>Edit</${Button}>
      ${!single && !d.isDefault && html`<${Button} size="sm" icon="check" onClick=${() => makeDefault(d, onChanged)} aria-label=${`Make ${d.name} the default`}>Make default</${Button}>`}
      ${!single && !d.isDefault && html`<${Button} size="sm" kind="danger-ghost" icon="trash" onClick=${async () => { if (await deleteDestinationFlow(d)) onChanged(); }} aria-label=${`Delete ${d.name}`} />`}
      <a class="btn btn-ghost btn-sm" href=${`#/destinations/${encodeURIComponent(d.id)}`}><span>Details</span><${Icon} name="arrow-right" size=${14} /></a>
    </div>`}
  </article>`;
}

export function DestinationsPage() {
  const { data, error, reload } = useFetch(() => api.destinations(), []);
  const [editing, setEditing] = useState(null);
  const single = singleDestination();
  useEffect(() => { const on = () => reload(true); window.addEventListener('cdc:destinations', on); return () => window.removeEventListener('cdc:destinations', on); }, []);
  return html`
    <${PageHeader} title="Destinations" subtitle="Meilisearch instances the tasks write to. Fan out by creating several tasks on the same table with different destinations."
      actions=${!single && can('index.write') && html`<${Button} kind="primary" icon="plus" class="keep-label" onClick=${() => setEditing('new')} data-testid="new-destination">New destination</${Button}>`} />
    ${single && html`<div style="margin-bottom:16px"><${Alert} tone="info" title="Single destination" data-testid="single-note">Pinned by MEILI_URL — unset it to manage several Meilisearch instances here.</${Alert}></div>`}
    <${ErrorBox} error=${error && !data && error} onRetry=${() => reload()} />
    ${!data ? html`<div class="grid grid-auto">${[0, 1].map((i) => html`<div class="card skeleton-card" key=${i}><${Skeleton} w="50%" h=${18} /><${Skeleton} w="70%" /><${Skeleton} h=${40} /></div>`)}</div>`
      : !data.length ? html`<div class="card"><${Empty} icon="database" title="No Meilisearch destination yet"
          action=${can('index.write') && html`<${Button} kind="primary" icon="plus" onClick=${() => setEditing('new')}>Add a Meilisearch destination</${Button}>`}>Tasks need a Meilisearch instance to write to.</${Empty}></div>`
      : html`<div class="grid grid-auto stagger" data-testid="destination-list">${data.map((d, i) => html`<${DestinationCard} key=${d.id} d=${d} i=${i} onEdit=${setEditing} onChanged=${() => reload(true)} />`)}</div>`}
    <${DestinationEditor} open=${!!editing} dest=${editing && editing !== 'new' ? editing : null} onClose=${() => setEditing(null)} onSaved=${() => reload(true)} />
  `;
}

// ---------------------------------------------------------------- detail tabs
function OverviewTab({ d, rows, h }) {
  const stats = useFetch(() => api.meiliStats(d.id).catch(() => null), [d.id, h.healthy]);
  const st = stats.data;
  const running = rows.filter((r) => ['starting', 'snapshotting', 'streaming', 'resyncing', 'retrying'].includes(r.state));
  const failing = rows.filter((r) => ['failed', 'held', 'retrying'].includes(r.state));
  const last = rows.map((r) => r.lastSyncAt).filter((t) => !isZeroTime(t)).sort().pop();
  const [checking, setChecking] = useState(false);
  const openIndex = (uid) => navigate(`#/indexes/${encodeURIComponent(uid)}?destination=${encodeURIComponent(d.id)}`);
  return html`<div class="stack tab-panel">
    <section class=${cls('card health-panel', h.healthy ? 'is-up' : 'is-down')} data-testid="destination-health">
      <div class="card-head"><h2><${HealthDot} healthy=${h.healthy} />${h.healthy ? 'Healthy' : 'Unreachable'}</h2>
        <div class="row" style="gap:8px"><${CheckTimes} h=${h} />${can('connector.test') && html`<${Button} size="sm" icon="restart" loading=${checking} onClick=${async () => { setChecking(true); await checkNow(d); setChecking(false); }} data-testid="check-now">Check now</${Button}>`}</div></div>
      <div class="card-body stack">
        ${!h.healthy && html`<${Alert} tone="bad">${h.error}</${Alert}>`}
        <div class="dest-health-grid">
          <div class="mini-stat"><span>Version</span><b>${h.version ? `v${h.version}` : '—'}</b></div>
          <div class="mini-stat"><span>Latency</span><b>${h.healthy ? `${h.latencyMs} ms` : '—'}</b></div>
          <div class="mini-stat"><span>Failed checks</span><b class=${h.consecutiveFailures ? 'tone-bad' : ''} style=${h.consecutiveFailures ? 'color:var(--bad-text)' : ''}>${fmtInt(h.consecutiveFailures || 0)}</b></div>
          <div class="mini-stat"><span>Last healthy</span><b>${h.lastHealthyAt ? timeAgo(h.lastHealthyAt) : '—'}</b></div>
          <div class="mini-stat"><span>Documents</span><b>${h.healthy ? html`<${Counter} value=${h.documents} format=${fmtCompact} />` : '—'}</b></div>
          <div class="mini-stat"><span>Indexes</span><b>${h.healthy ? fmtInt(h.indexes) : '—'}</b></div>
          <div class="mini-stat"><span>Database</span><b>${st ? fmtBytes(st.databaseSize) : h.healthy ? fmtBytes(h.databaseSize) : '—'}</b></div>
          <div class="mini-stat"><span>Used</span><b>${st && st.usedDatabaseSize ? fmtBytes(st.usedDatabaseSize) : '—'}</b></div>
        </div>
      </div>
    </section>
    <div class="sync-strip stagger" data-testid="sync-summary">
      ${[['Tasks', fmtInt(rows.length)], ['Running', fmtInt(running.length)], ['Failing', fmtInt(failing.length), failing.length ? 'bad' : ''], ['Last sync', last ? timeAgo(last) : 'never'],
        ['Max lag', running.length ? fmtLag(Math.max(...running.map((r) => r.lagMs || 0))) : '—'], ['Total rate', `${fmtRate(rows.reduce((a, r) => a + (r.rate || 0), 0))}/s`]].map(([l, v, tone], i) => html`
        <div class=${cls('sync-cell', tone && `tone-${tone}`)} style=${`--i:${i}`}><span>${l}</span><b>${v}</b></div>`)}
    </div>
    <section class="card"><div class="card-head"><h2><${Icon} name="tasks" />Synced tasks</h2></div>
      ${!rows.length ? html`<${Empty} icon="tasks" title="No task writes here yet" action=${can('task.create') && html`<a class="btn btn-primary" href=${`#/tasks/new?destination=${encodeURIComponent(d.id)}`}><${Icon} name="plus" /><span>New task</span></a>`}>Pick this destination in the task wizard's Index step.</${Empty}>`
        : html`<div class="table-wrap"><table class="table rtable" data-testid="synced-tasks">
        <thead><tr><th>Task</th><th>Index</th><th>State</th><th class="num-col">Lag</th><th class="num-col">Rate</th><th>Last sync</th></tr></thead>
        <tbody>${rows.map((r, i) => html`<tr key=${r.id} style=${`--i:${i}`} data-task-id=${r.id}>
          <td class="rt-title"><a href=${`#/tasks/${encodeURIComponent(r.id)}`}><b>${r.name}</b></a>${r.error && html`<span class="err-text" title=${r.error}>${r.error}</span>`}</td>
          <td data-label="Index"><button type="button" class="link-btn mono" onClick=${() => openIndex(r.indexUid)} aria-label=${`Open index ${r.indexUid} on ${d.name}`}>${r.indexUid}</button></td>
          <td data-label="State"><${StateBadge} state=${r.state} /></td>
          <td data-label="Lag" class="num-col">${['streaming', 'snapshotting', 'resyncing', 'retrying'].includes(r.state) ? fmtLag(r.lagMs) : '—'}</td>
          <td data-label="Rate" class="num-col" data-testid="synced-rate">${fmtRate(r.rate || 0)}/s</td>
          <td data-label="Last sync" class="nowrap">${isZeroTime(r.lastSyncAt) ? html`<span class="subtle">never</span>` : timeAgo(r.lastSyncAt)}</td>
        </tr>`)}</tbody></table></div>`}
    </section>
  </div>`;
}

function IndexesTab({ d, healthy }) {
  const { data, error, reload } = useFetch(() => api.indexesOf(d.id), [d.id]);
  if (!healthy) return html`<div class="tab-panel"><${Alert} tone="bad" title="Unreachable">Indexes cannot be listed while this instance is down.</${Alert}></div>`;
  return html`<div class="tab-panel stack" data-testid="destination-indexes">
    <div class="row-between"><p class="muted">Indexes stored on ${d.name}.</p><a class="btn btn-secondary btn-sm" href=${`#/indexes?instance=${encodeURIComponent(d.id)}`}><span>Open in Indexes</span><${Icon} name="arrow-right" size=${14} /></a></div>
    <${ErrorBox} error=${error && !data && error} onRetry=${() => reload()} />
    ${!data ? (error ? null : html`<${SkeletonRows} rows=${3} cols=${3} />`)
      : !data.length ? html`<div class="card"><${Empty} icon="indexes" title="No index yet">Tasks create their index on the first write.</${Empty}></div>`
      : html`<div class="grid grid-auto stagger">${data.map((ix, i) => html`<${IndexCard} key=${ix.uid} ix=${ix} i=${i} destination=${d.id} onChanged=${() => reload(true)} />`)}</div>`}
  </div>`;
}

function BackupsTab({ d, healthy }) {
  const [busy, setBusy] = useState('');
  const run = async (kind) => {
    setBusy(kind);
    try { const r = kind === 'dump' ? await api.dump(d.id) : await api.snapshot(d.id); toast({ title: kind === 'dump' ? `Dump enqueued on ${d.name}` : `Snapshot enqueued on ${d.name}`, message: `Meilisearch task #${r.taskUid}` }); }
    catch (e) { toastError(e); }
    setBusy('');
  };
  const admin = can('instance.write');
  return html`<div class="tab-panel grid grid-2" style="align-items:start" data-testid="destination-backups">
    <section class="card"><div class="card-head"><h2><${Icon} name="download" />Dump</h2></div>
      <div class="card-body stack"><p class="muted">A portable export of every index, its documents and settings. Use it to migrate to another Meilisearch version or machine. Large instances take a while.</p>
        <div><${Button} kind="primary" icon="download" loading=${busy === 'dump'} disabled=${!healthy} onClick=${() => run('dump')} ...${admin ? {} : { disabled: true, title: 'Requires admin role' }} data-testid="create-dump">Create dump</${Button}></div></div></section>
    <section class="card"><div class="card-head"><h2><${Icon} name="layers" />Snapshot</h2></div>
      <div class="card-body stack"><p class="muted">A fast binary copy of the database, restorable only on the same Meilisearch version. Ideal for quick backups before risky changes.</p>
        <div><${Button} kind="primary" icon="layers" loading=${busy === 'snapshot'} disabled=${!healthy} onClick=${() => run('snapshot')} ...${admin ? {} : { disabled: true, title: 'Requires admin role' }} data-testid="create-snapshot">Create snapshot</${Button}></div></div></section>
    <p class="subtle span-all">Progress appears in this instance's <a href=${`#/destinations/${encodeURIComponent(d.id)}/tasks?types=dumpCreation,snapshotCreation`}>task queue (dumps and snapshots)</a>.</p>
  </div>`;
}

const FEATURE_HINTS = {
  metrics: 'Expose Prometheus metrics on /metrics.', logsRoute: 'Stream and configure logs over HTTP.', editDocumentsByFunction: 'Edit documents with a Rhai function.',
  containsFilter: 'Enable CONTAINS and STARTS WITH filter operators.', network: 'Remote instances for federated search.', getTaskDocumentsRoute: 'Read the payload of document tasks.',
  compositeEmbedders: 'Different embedders for indexing and search.', chatCompletions: 'Conversational search with an LLM.', multimodal: 'Embed images and other media.', vectorStoreSetting: 'Choose the vector store backend.',
};
function ExperimentalTab({ d, healthy }) {
  const { data, error, reload, setData } = useFetch(() => (healthy ? api.experimental(d.id) : Promise.resolve(null)), [d.id, healthy]);
  const [busy, setBusy] = useState('');
  const admin = can('instance.write');
  const flip = async (k, v) => {
    setBusy(k); setData((x) => ({ ...x, [k]: v }));
    try { setData(await api.setExperimental({ [k]: v }, d.id)); toast({ title: `${k} ${v ? 'enabled' : 'disabled'} on ${d.name}` }); }
    catch (e) { setData((x) => ({ ...x, [k]: !v })); toastError(e, `Could not update ${k}`); }
    setBusy('');
  };
  return html`<section class="card tab-panel" data-testid="destination-experimental"><div class="card-head"><h2><${Icon} name="flask" />Experimental features</h2>${!admin && html`<span class="tag">read only</span>`}</div>
    <div class="card-body stack">
      <${Alert} tone="warn">Experimental Meilisearch features can change or disappear between versions. They apply to ${d.name} only.</${Alert}>
      ${!healthy ? html`<${Alert} tone="bad">Unavailable while the instance is unreachable.</${Alert}>` : html`
      <${ErrorBox} error=${error} onRetry=${() => reload()} />
      ${!data ? html`<div class="feature-list">${[0, 1, 2, 3].map((k) => html`<${Skeleton} key=${k} h=${40} />`)}</div>`
        : html`<div class="feature-list" data-testid="experimental-features">${Object.keys(data).sort().map((k) => typeof data[k] === 'boolean' && html`
            <${Toggle} key=${k} id=${`exp-${k}`} label=${k} hint=${FEATURE_HINTS[k]} checked=${data[k]} disabled=${!admin || busy === k} onChange=${(v) => flip(k, v)} />`)}</div>`}`}
    </div></section>`;
}

function SettingsTab({ d, onSaved }) {
  const [f, setF] = useState(null);
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState('');
  const [err, setErr] = useState(null);
  const single = singleDestination();
  useLayoutEffect(() => { setF({ name: d.name, url: d.url, apiKey: d.apiKey, timeout: d.timeout || '30s', healthCheckInterval: d.healthCheckInterval || '30s', gzip: d.gzip !== false, tags: (d.tags || []).join(', '), description: d.description || '' }); }, [d.id, d.updatedAt]);
  if (!f) return null;
  const admin = can('index.write');
  const errs = destFormErrors(f);
  const save = async () => {
    setTried(true);
    if (Object.keys(errs).length) return;
    setBusy('save'); setErr(null);
    try { const v = await api.updateDestination(d.id, destBody(f)); toast({ title: 'Destination saved', message: v.name }); destinationsChanged(); onSaved(); }
    catch (e) { setErr(e); }
    setBusy('');
  };
  return html`<div class="stack tab-panel" data-testid="destination-settings">
    <section class="card"><div class="card-head"><h2><${Icon} name="settings" />Connection</h2>${!admin && html`<span class="tag">read only</span>`}</div>
      <div class="card-body stack">
        <${ErrorBox} error=${err} />
        ${d.managedByEnv && html`<${Alert} tone="info" title="Managed by the environment">URL and API key come from MEILI_URL / MEILI_API_KEY and are read-only here.</${Alert}>`}
        <fieldset class="plain-fieldset" disabled=${!admin}><${DestinationForm} value=${f} onChange=${setF} errors=${tried ? errs : {}} env=${d.managedByEnv} editing /></fieldset>
      </div>
      ${admin && html`<div class="card-foot">
        ${!single && !d.isDefault && html`<${Button} icon="check" onClick=${() => makeDefault(d, onSaved)}>Make default</${Button}>`}
        <span class="grow"></span><${Button} kind="primary" icon="check" loading=${busy === 'save'} onClick=${save} data-testid="destination-save">Save</${Button}></div>`}
    </section>
    ${admin && !single && html`<section class="card danger-zone"><div class="card-head"><h2><${Icon} name="alert" />Danger zone</h2></div>
      <div class="card-body row-between"><div><b>Delete this destination</b><p class="muted">${d.isDefault ? 'The default destination cannot be deleted; make another one the default first.' : 'Only possible when no task writes to it. Indexes in Meilisearch are not touched.'}</p></div>
        <${Button} kind="danger" icon="trash" disabled=${d.isDefault} onClick=${async () => { if (await deleteDestinationFlow(d)) navigate('#/destinations'); }} data-testid="destination-delete">Delete destination</${Button}></div></section>`}
  </div>`;
}

const DEST_TABS = [
  { id: 'overview', label: 'Overview', icon: 'overview' }, { id: 'indexes', label: 'Indexes', icon: 'indexes' },
  { id: 'tasks', label: 'Meilisearch tasks', icon: 'meili-tasks' }, { id: 'backups', label: 'Backups', icon: 'hdd' },
  { id: 'experimental', label: 'Experimental', icon: 'flask' }, { id: 'settings', label: 'Settings', icon: 'settings' },
];

export function DestinationDetailPage({ id, tab = 'overview' }) {
  const lv = useLive();
  useTick(5000);
  const { data: d, error, reload } = useFetch(() => api.destination(id), [id]);
  const [test, setTest] = useState(null);
  const [busy, setBusy] = useState(false);
  if (error && !d) return html`<a class="back-link" href="#/destinations"><${Icon} name="arrow-left" size=${15} />Destinations</a><div style="margin-top:16px"><${ErrorBox} error=${error} onRetry=${() => reload()} /></div>`;
  if (!d) return html`<div class="stack"><${Skeleton} w=${90} h=${12} /><${Skeleton} w=${280} h=${28} /><div class="card"><${SkeletonRows} rows=${4} cols=${4} /></div></div>`;
  const h = healthOf(d);
  // recomputed on every SSE update (the live store mutates in place)
  const rows = (d.usedBy || []).map((u) => {
    const st = lv.status[u.id];
    return st ? { ...u, state: st.state, lagMs: st.lagMs, rate: st.rate, lastSyncAt: isZeroTime(st.lastEventAt) ? u.lastSyncAt : st.lastEventAt, error: st.error || '' } : u;
  });
  const active = DEST_TABS.some((t) => t.id === tab) ? tab : 'overview';
  const types = (location.hash.split('?')[1] || '').match(/types=([^&]+)/);
  const runTest = async () => { setBusy(true); try { setTest(await api.testDestination(d.id)); } catch (e) { setTest({ ok: false, error: e.message }); } setBusy(false); };
  const body = {
    overview: () => html`<${OverviewTab} d=${d} rows=${rows} h=${h} />`,
    indexes: () => html`<${IndexesTab} d=${d} healthy=${h.healthy} />`,
    tasks: () => (h.healthy ? html`<div class="tab-panel"><${MeiliTasksPage} destination=${d.id} embedded initialTypes=${types ? decodeURIComponent(types[1]).split(',') : []} /></div>`
      : html`<div class="tab-panel"><${Alert} tone="bad" title="Unreachable">The task queue is unavailable while this instance is down.</${Alert}></div>`),
    backups: () => html`<${BackupsTab} d=${d} healthy=${h.healthy} />`,
    experimental: () => html`<${ExperimentalTab} d=${d} healthy=${h.healthy} />`,
    settings: () => html`<${SettingsTab} d=${d} onSaved=${() => reload(true)} />`,
  }[active];
  return html`
    <a class="back-link" href="#/destinations"><${Icon} name="arrow-left" size=${15} />Destinations</a>
    <header class="detail-head" style="margin-top:10px">
      <div class="detail-title"><span class="dest-glyph dest-glyph-lg"><${Icon} name="database" size=${24} /></span>
        <div style="min-width:0"><div class="row" style="gap:10px"><h1>${d.name}</h1><${HealthDot} healthy=${h.healthy} label=${h.healthy ? 'Healthy' : 'Unreachable'} />
            ${d.isDefault && html`<span class="tag tag-primary">default</span>`}${d.managedByEnv && html`<span class="tag tag-violet">managed by env</span>`}</div>
          <div class="detail-meta"><span class="mono">${d.url}</span>${(d.tags || []).map((t) => html`<${Tag}>${t}</${Tag}>`)}</div>
          ${d.description && html`<p class="muted" style="margin-top:6px">${d.description}</p>`}</div></div>
      <div class="btn-group">
        ${can('connector.test') && html`<${Button} size="sm" kind="primary" icon="plug" loading=${busy} onClick=${runTest} data-testid="destination-detail-test">Test connection</${Button}>`}
      </div>
    </header>
    ${test && html`<div style="margin-bottom:16px" aria-live="polite"><${TestResult} r=${test} /></div>`}
    <${Tabs} tabs=${DEST_TABS} active=${active} label="Destination sections" onChange=${(t) => navigate(`#/destinations/${encodeURIComponent(d.id)}${t === 'overview' ? '' : `/${t}`}`)} />
    <div role="tabpanel" aria-labelledby=${`tab-${active}`} key=${active}>${body()}</div>
  `;
}
