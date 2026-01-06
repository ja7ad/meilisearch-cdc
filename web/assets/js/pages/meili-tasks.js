// Meilisearch task queue: status/index/type filters, auto refresh, cancel, pagination.
import { html, useEffect, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { Icon } from '../icons.js';
import { ScopeBadge } from './destinations.js';
import { setDestination } from '../api.js';
import { selectedDestination, singleDestination, useDestinations } from '../destinations.js';
import { can } from '../auth.js';
import { useFetch } from '../hooks.js';
import { Button, Empty, ErrorBox, JsonView, Modal, PageHeader, SkeletonRows, Toggle, confirmDialog, toast, toastError } from '../ui.js';
import { cls, fmtIsoDuration, lsGet, lsSet, timeAgo } from '../util.js';

const STATUSES = ['enqueued', 'processing', 'succeeded', 'failed', 'canceled'];
const TONE = { enqueued: 'muted', processing: 'sync', succeeded: 'ok', failed: 'bad', canceled: 'warn' };
const TYPES = ['documentAdditionOrUpdate', 'documentDeletion', 'documentEdition', 'settingsUpdate', 'indexCreation', 'indexUpdate', 'indexDeletion', 'indexSwap', 'indexCompaction', 'taskCancelation', 'taskDeletion', 'dumpCreation', 'snapshotCreation'];
const cancellable = (t) => t.status === 'enqueued' || t.status === 'processing';

/** Instance picker for the Meilisearch tasks page (multi-instance mode); drives the global destination. */
function InstancePicker() {
  const dests = useDestinations();
  if (!dests || dests.length < 2 || singleDestination()) return null;
  const cur = selectedDestination(dests);
  return html`<select class="select" aria-label="Meilisearch instance" value=${cur ? cur.id : ''} data-testid="mt-instance"
    onChange=${(e) => { const d = dests.find((x) => x.id === e.currentTarget.value); setDestination(d && !d.isDefault ? d.id : ''); }}>
    ${dests.map((d) => html`<option value=${d.id}>${d.name}${d.health && !d.health.healthy ? ' — unreachable' : ''}</option>`)}</select>`;
}

/**
 * Meilisearch task queue. As a page it follows the global destination; embedded in a
 * destination page it is pinned to `destination` and passes it on every call.
 */
export function MeiliTasksPage({ destination, embedded, initialTypes = [] }) {
  const [status, setStatus] = useState([]);
  const [indexUid, setIndexUid] = useState('');
  const [type, setType] = useState(initialTypes.join(','));
  const [auto, setAuto] = useState(lsGet('cdc.meiliTasks.auto', '1') === '1');
  const [pages, setPages] = useState([]); // extra pages loaded with "Load more"
  const [sel, setSel] = useState(new Set());
  const [view, setView] = useState(null);
  const indexes = useFetch(() => (destination ? api.indexesOf(destination) : api.indexes()), [destination]);
  const filters = { status: status.join(','), indexUid, type, limit: 50, ...(destination ? { destination } : {}) };
  const { data, error, reload } = useFetch(() => api.meiliTasks(filters), [status.join(','), indexUid, type], { interval: auto ? 3000 : 0 });
  useEffect(() => { setPages([]); setSel(new Set()); }, [status.join(','), indexUid, type]);
  useEffect(() => { lsSet('cdc.meiliTasks.auto', auto ? '1' : '0'); }, [auto]);

  const rows = [...((data && data.results) || []), ...pages.flatMap((p) => p.results)];
  const next = pages.length ? pages[pages.length - 1].next : data && data.next;
  const loadMore = async () => {
    try { const p = await api.meiliTasks({ ...filters, from: next }); setPages([...pages, p]); } catch (e) { toastError(e); }
  };
  const toggleStatus = (s) => setStatus(status.includes(s) ? status.filter((x) => x !== s) : [...status, s]);
  const toggle = (uid) => { const s = new Set(sel); s.has(uid) ? s.delete(uid) : s.add(uid); setSel(s); };
  const cancel = async (uids) => {
    const ok = await confirmDialog({ title: `Cancel ${uids.length} task${uids.length === 1 ? '' : 's'}?`, message: 'Enqueued and processing tasks are canceled; their changes are not applied.', confirmLabel: 'Cancel tasks', danger: true });
    if (!ok) return;
    try { const r = await api.cancelMeiliTasks(uids, destination); toast({ title: 'Cancelation enqueued', message: `Meilisearch task #${r.taskUid}` }); setSel(new Set()); setPages([]); reload(true); }
    catch (e) { toastError(e, 'Cancel failed'); }
  };
  const selectable = rows.filter(cancellable);
  const canCancel = can('meili.cancel');

  return html`
    ${embedded ? html`<div class="row-between" style="margin-bottom:12px"><p class="muted">This instance's task queue. CDC writes carry customMetadata so you can trace them to a pipeline.</p>
        <div class="row"><${Toggle} id="mt-auto" label="Auto refresh" checked=${auto} onChange=${setAuto} />
        <${Button} icon="restart" onClick=${() => { setPages([]); reload(); }} aria-label="Refresh">Refresh</${Button}></div></div>`
      : html`<${PageHeader} title="Meilisearch tasks" children=${html`<${ScopeBadge} />`} subtitle="The Meilisearch task queue. CDC writes carry customMetadata so you can trace them to a pipeline."
      actions=${html`<${Toggle} id="mt-auto" label="Auto refresh" checked=${auto} onChange=${setAuto} />
        <${Button} icon="restart" onClick=${() => { setPages([]); reload(); }} aria-label="Refresh">Refresh</${Button}>`} />`}
    <div class="toolbar">
      ${!embedded && html`<${InstancePicker} />`}
      <div class="chips" role="group" aria-label="Status filter">
        ${STATUSES.map((s) => html`<button type="button" class="chip" aria-pressed=${status.includes(s)} onClick=${() => toggleStatus(s)} data-status=${s}>
          <span class=${`legend-dot tone-bg-${TONE[s]}`}></span>${s}</button>`)}
      </div>
      <select class="select" aria-label="Filter by index" value=${indexUid} onChange=${(e) => setIndexUid(e.currentTarget.value)} data-testid="mt-index">
        <option value="">All indexes</option>${((indexes.data || []).map((i) => i.uid)).sort().map((u) => html`<option value=${u}>${u}</option>`)}</select>
      <select class="select" aria-label="Filter by type" value=${type} onChange=${(e) => setType(e.currentTarget.value)}>
        ${type.includes(',') && html`<option value=${type}>${type.split(',').join(' + ')}</option>`}
        <option value="">All types</option>${TYPES.map((t) => html`<option value=${t}>${t}</option>`)}</select>
      ${sel.size > 0 && html`<${Button} kind="danger-ghost" icon="x" onClick=${() => cancel([...sel])} data-testid="cancel-selected">Cancel ${sel.size}</${Button}>`}
    </div>
    ${error && error.status === 409 && !data ? html`<div class="card"><${Empty} icon="database" title="No Meilisearch destination yet"
        action=${html`<a class="btn btn-primary" href="#/destinations"><${Icon} name="plus" /><span>Add a Meilisearch destination</span></a>`}>Connect a Meilisearch instance to browse its indexes and tasks.</${Empty}></div>`
      : html`<${ErrorBox} error=${error && !data && error} onRetry=${() => reload()} />`}
    <div class="card">
      ${!data ? (error ? null : html`<${SkeletonRows} rows=${8} cols=${6} />`)
        : !rows.length ? html`<${Empty} icon="meili-tasks" title="No tasks">No Meilisearch task matches these filters.</${Empty}>`
        : html`<div class="table-wrap"><table class="table table-compact rtable" data-testid="meili-tasks-table">
          <thead><tr>
            ${canCancel && html`<th class="col-check"><input type="checkbox" class="checkbox" disabled=${!selectable.length} checked=${selectable.length > 0 && selectable.every((t) => sel.has(t.uid))}
              onChange=${(e) => setSel(e.currentTarget.checked ? new Set(selectable.map((t) => t.uid)) : new Set())} aria-label="Select all cancellable tasks" /></th>`}
            <th>Uid</th><th>Status</th><th>Type</th><th class="hide-sm">Index</th><th class="hide-sm">Metadata</th><th class="hide-sm">Enqueued</th><th class="num-col hide-sm">Duration</th><th class="col-actions"><span class="sr-only">Actions</span></th>
          </tr></thead>
          <tbody>${rows.map((t, i) => html`<tr key=${t.uid} class=${sel.has(t.uid) ? 'is-selected' : ''} style=${`--i:${Math.min(i, 12)}`} data-uid=${t.uid}>
            ${canCancel && html`<td class="col-check">${cancellable(t) && html`<input type="checkbox" class="checkbox" checked=${sel.has(t.uid)} onChange=${() => toggle(t.uid)} aria-label=${`Select task ${t.uid}`} />`}</td>`}
            <td class="mono rt-title"><b>#${t.uid}</b></td>
            <td data-label="Status"><span class=${cls('badge', `tone-${TONE[t.status] || 'muted'}`, t.status === 'processing' && 'is-live')}><span class="dot"></span>${t.status}</span></td>
            <td data-label="Type" class="rt-wrap"><span class="mono type-cell" style="font-size:12.5px">${t.type}</span>${t.error && html`<span class="err-text" title=${t.error.message}>${t.error.message}</span>`}</td>
            <td data-label="Index" class="hide-sm">${t.indexUid ? html`<a class="mono" href=${`#/indexes/${encodeURIComponent(t.indexUid)}`}>${t.indexUid}</a>` : html`<span class="subtle">—</span>`}</td>
            <td data-label="Metadata" class="hide-sm">${t.customMetadata ? html`<span class="meta-chip" title=${t.customMetadata}>${t.customMetadata}</span>` : html`<span class="subtle">—</span>`}</td>
            <td data-label="Enqueued" class="hide-sm subtle nowrap" title=${t.enqueuedAt}>${timeAgo(t.enqueuedAt)}</td>
            <td data-label="Duration" class="num-col hide-sm">${fmtIsoDuration(t.duration)}</td>
            <td class="col-actions"><div class="row-actions">
              <${Button} size="sm" kind="ghost" icon="eye" aria-label=${`View task ${t.uid}`} onClick=${() => setView(t)} />
              ${canCancel && cancellable(t) && html`<${Button} size="sm" kind="ghost" icon="x" aria-label=${`Cancel task ${t.uid}`} onClick=${() => cancel([t.uid])} />`}
            </div></td>
          </tr>`)}</tbody>
        </table></div>
        <div class="pager"><span>${rows.length} of ${data.total ?? rows.length} tasks</span>
          ${next != null && html`<${Button} size="sm" icon="chevron-down" onClick=${loadMore}>Load more</${Button}>`}</div>`}
    </div>
    <${Modal} open=${!!view} onClose=${() => setView(null)} title=${view ? `Task #${view.uid} · ${view.type}` : ''} size="lg">${view && html`<${JsonView} value=${view} maxHeight=${480} label="Meilisearch task" />`}</${Modal}>
  `;
}

