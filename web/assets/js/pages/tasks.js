// Tasks list: search, filters, multi-select batch actions, import/export.
import { html, useEffect, useMemo, useRef, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { live, statusOf, useLive } from '../sse.js';
import { can } from '../auth.js';
import { useDestinations } from '../destinations.js';
import { DestinationBadge } from './destinations.js';
import { useFetch } from '../hooks.js';
import { Sparkline } from '../charts.js';
import { SourceIcon, deleteTaskFlow, runTaskAction, seedMetrics } from '../components.js';
import { Icon } from '../icons.js';
import { Button, Empty, ErrorBox, PageHeader, Progress, SkeletonRows, StateBadge, confirmDialog, toast, toastError } from '../ui.js';
import { ACTION_LABELS, SOURCES, STATES, downloadJSON, fmtCompact, fmtLag, fmtRate, isActive, lsGet, lsSet, sourceInfo, stateInfo } from '../util.js';

const BATCH = ['start', 'stop', 'pause', 'resume', 'restart', 'resync'];
const BATCH_ICON = { start: 'play', stop: 'stop', pause: 'pause', resume: 'play', restart: 'restart', resync: 'resync' };

function QuickActions({ task, onChanged }) {
  const st = (statusOf(task) || {}).state;
  const primary = st === 'paused' ? 'resume' : isActive(st) ? 'stop' : 'start';
  const [busy, setBusy] = useState(false);
  const run = async (a) => { setBusy(true); const v = await runTaskAction(task, a); setBusy(false); if (v) onChanged(); };
  return html`<div class="row-actions">
    ${can('task.action') && html`<${Button} kind="ghost" size="sm" icon=${BATCH_ICON[primary]} loading=${busy} aria-label=${`${ACTION_LABELS[primary]} ${task.name}`} title=${ACTION_LABELS[primary]} onClick=${() => run(primary)} />`}
    ${can('task.action') && isActive(st) && st !== 'retrying' && html`<${Button} kind="ghost" size="sm" class="hide-sm" icon="pause" aria-label=${`Pause ${task.name}`} title="Pause" onClick=${() => run('pause')} />`}
    ${can('task.edit') && html`<a class="btn btn-ghost btn-sm btn-icon hide-sm" href=${`#/tasks/${encodeURIComponent(task.id)}/edit`} aria-label=${`Edit ${task.name}`} title="Edit"><${Icon} name="edit" size=${15} /></a>`}
    ${can('task.delete') && html`<${Button} kind="ghost" size="sm" class="hide-sm" icon="trash" aria-label=${`Delete ${task.name}`} title="Delete" onClick=${async () => { if (await deleteTaskFlow(task)) onChanged(); }} />`}
  </div>`;
}

export function TasksPage() {
  const lv = useLive();
  const dests = useDestinations();
  const { data, error, reload } = useFetch(() => api.tasks(), []);
  const [q, setQ] = useState(lsGet('cdc.tasks.q', ''));
  const [state, setState] = useState('');
  const [source, setSource] = useState('');
  const [tag, setTag] = useState('');
  const [sel, setSel] = useState(new Set());
  const [busy, setBusy] = useState('');
  const fileRef = useRef(null);

  useEffect(() => { if (lv.taskSeq) reload(true); }, [lv.taskSeq]);
  useEffect(() => { if (data) seedMetrics(data.map((t) => t.id)); }, [data && data.length]);
  useEffect(() => { lsSet('cdc.tasks.q', q || null); }, [q]);

  const tags = useMemo(() => [...new Set((data || []).flatMap((t) => t.tags || []))].sort(), [data]);
  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (data || []).filter((t) => {
      const st = (statusOf(t) || {}).state;
      if (state && st !== state) return false;
      if (source && t.source.type !== source) return false;
      if (tag && !(t.tags || []).includes(tag)) return false;
      if (needle && !`${t.id} ${t.name} ${t.description || ''} ${t.source.table} ${t.index.uid} ${(t.tags || []).join(' ')}`.toLowerCase().includes(needle)) return false;
      return true;
    });
  }, [data, q, state, source, tag, lv.status]);

  // drop selections that are no longer visible
  useEffect(() => {
    const vis = new Set(rows.map((r) => r.id));
    if ([...sel].some((id) => !vis.has(id))) setSel(new Set([...sel].filter((id) => vis.has(id))));
  }, [rows]);

  const allChecked = rows.length > 0 && rows.every((r) => sel.has(r.id));
  const toggleAll = () => setSel(allChecked ? new Set() : new Set(rows.map((r) => r.id)));
  const toggle = (id) => { const s = new Set(sel); s.has(id) ? s.delete(id) : s.add(id); setSel(s); };

  const batch = async (action) => {
    const ids = [...sel];
    if (action === 'delete' || action === 'resync') {
      const ok = await confirmDialog({
        title: `${ACTION_LABELS[action]} ${ids.length} task${ids.length > 1 ? 's' : ''}?`,
        message: action === 'delete' ? 'Task definitions and checkpoints are removed. Index documents are kept.' : 'Every selected task copies all rows from its source again.',
        danger: true, confirmLabel: ACTION_LABELS[action], typed: action === 'delete' ? `delete ${ids.length}` : undefined,
      });
      if (!ok) return;
    }
    setBusy(action);
    try {
      const { results = [] } = await api.batch(ids, action);
      const failed = results.filter((r) => !r.ok);
      const okN = results.length - failed.length;
      toast({
        tone: failed.length ? (okN ? 'warn' : 'bad') : 'ok',
        title: `${ACTION_LABELS[action]}: ${okN} of ${results.length} succeeded`,
        message: failed.length ? failed.slice(0, 3).map((f) => `${f.id}: ${f.error}`).join(' · ') : undefined,
      });
      if (action === 'delete') setSel(new Set());
      reload(true);
    } catch (e) { toastError(e, `Batch ${action} failed`); }
    setBusy('');
  };

  const doExport = async () => {
    try { downloadJSON(`meilisearch-cdc-tasks-${new Date().toISOString().slice(0, 10)}.json`, await api.exportTasks()); toast({ title: 'Tasks exported', message: 'Secrets are redacted in the export.' }); }
    catch (e) { toastError(e, 'Export failed'); }
  };
  const doImport = async (e) => {
    const f = e.currentTarget.files && e.currentTarget.files[0];
    e.currentTarget.value = '';
    if (!f) return;
    let list;
    try { list = JSON.parse(await f.text()); } catch { toast({ tone: 'bad', title: 'Import failed', message: `${f.name} is not valid JSON` }); return; }
    if (!Array.isArray(list)) list = [list];
    try {
      const r = await api.importTasks(list);
      toast({ tone: r.errors && r.errors.length ? 'warn' : 'ok', title: `Imported ${r.created} task${r.created === 1 ? '' : 's'}`, message: r.errors && r.errors.length ? r.errors.slice(0, 3).join(' · ') : undefined });
      reload(true);
    } catch (err) { toastError(err, 'Import failed'); }
  };

  const filtered = q || state || source || tag;
  const selectable = can('task.action');
  const stateOpts = Object.keys(STATES).filter((s) => s !== 'running');

  return html`
    <${PageHeader} title="Tasks" doc="tasks" subtitle="Each task captures changes from one source table and keeps a Meilisearch index in sync."
      actions=${html`
        ${can('task.import') && html`<input type="file" accept="application/json,.json" ref=${fileRef} class="sr-only" onChange=${doImport} aria-label="Import tasks JSON file" tabindex="-1" />`}
        ${can('task.import') && html`<${Button} icon="upload" onClick=${() => fileRef.current.click()} title="Import tasks from JSON">Import</${Button}>`}
        <${Button} icon="download" onClick=${doExport} title="Export tasks as JSON">Export</${Button}>
        ${can('task.create') && html`<a class="btn btn-primary keep-label" href="#/tasks/new"><${Icon} name="plus" /><span>New task</span></a>`}`} />

    <div class="toolbar" role="search">
      <div class="input-icon"><${Icon} name="search" size=${16} />
        <input class="input" type="search" placeholder="Search name, table, index, tag…" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} aria-label="Search tasks" data-testid="task-search" /></div>
      <select class="select" value=${state} onChange=${(e) => setState(e.currentTarget.value)} aria-label="Filter by state" data-testid="filter-state">
        <option value="">All states</option>${stateOpts.map((s) => html`<option value=${s}>${STATES[s].label}</option>`)}</select>
      <select class="select" value=${source} onChange=${(e) => setSource(e.currentTarget.value)} aria-label="Filter by source type" data-testid="filter-source">
        <option value="">All sources</option>${SOURCES.map((s) => html`<option value=${s.type}>${s.label}</option>`)}</select>
      <select class="select" value=${tag} onChange=${(e) => setTag(e.currentTarget.value)} aria-label="Filter by tag" data-testid="filter-tag">
        <option value="">All tags</option>${tags.map((t) => html`<option value=${t}>${t}</option>`)}</select>
      ${filtered && html`<${Button} kind="ghost" size="sm" icon="x" onClick=${() => { setQ(''); setState(''); setSource(''); setTag(''); }}>Clear</${Button}>`}
    </div>

    <${ErrorBox} error=${error && !data && error} onRetry=${() => reload()} />
    <div class="card">
      ${!data ? html`<${SkeletonRows} rows=${6} cols=${6} />`
        : !rows.length ? (filtered
          ? html`<${Empty} icon="filter" title="No matching tasks">Try a different search or clear the filters.</${Empty}>`
          : html`<${Empty} icon="plug" title="No tasks yet" action=${can('task.create') && html`<a class="btn btn-primary" href="#/tasks/new"><${Icon} name="plus" /><span>New task</span></a>`}>Create a task to start syncing a table into Meilisearch.</${Empty}>`)
        : html`<div class="table-wrap"><table class="table rtable rtable-tasks" data-testid="tasks-table">
          <thead><tr>
            ${selectable && html`<th class="col-check"><input type="checkbox" class="checkbox" checked=${allChecked} onChange=${toggleAll} aria-label="Select all visible tasks" /></th>`}
            <th>Task</th><th>State</th><th class="hide-sm">Index</th><th class="hide-sm">Progress · lag</th><th class="num-col hide-sm">Rate</th><th class="hide-md">Tags</th><th class="col-actions"><span class="sr-only">Actions</span></th>
          </tr></thead>
          <tbody>${rows.map((t, i) => {
            const st = statusOf(t) || {};
            const snap = st.snapshot || {};
            const snapping = (st.state === 'snapshotting' || st.state === 'resyncing') && snap.total > 0;
            const info = stateInfo(st.state);
            return html`<tr key=${t.id} class=${sel.has(t.id) ? 'is-selected' : ''} style=${`--i:${Math.min(i, 12)}`} data-task-id=${t.id}>
              ${selectable && html`<td class="col-check"><input type="checkbox" class="checkbox" checked=${sel.has(t.id)} onChange=${() => toggle(t.id)} aria-label=${`Select ${t.name}`} /></td>`}
              <td class="rt-title"><div class="cell-title"><${SourceIcon} type=${t.source.type} size="sm" />
                <div style="min-width:0"><a href=${`#/tasks/${encodeURIComponent(t.id)}`}>${t.name || t.id}</a><small title=${`${sourceInfo(t.source.type).label} · ${t.source.table}`}>${sourceInfo(t.source.type).label} · ${t.source.table}</small></div></div></td>
              <td data-label="State" class="rt-state"><${StateBadge} state=${st.state} /></td>
              <td data-label="Index" class="hide-sm"><a class="mono" href=${`#/indexes/${encodeURIComponent(t.index.uid)}${t.index.destinationId ? `?destination=${encodeURIComponent(t.index.destinationId)}` : ''}`}>${t.index.uid}</a>
                <div><${DestinationBadge} list=${dests} id=${t.index.destinationId} /></div></td>
              <td data-label=${snapping ? "Snapshot" : "Lag"} class="hide-sm">${snapping ? html`<div class="progress-cell"><${Progress} value=${snap.done} max=${snap.total} shimmer label=${`Snapshot progress of ${t.name}`} />
                  <span>${fmtCompact(snap.done)} / ${fmtCompact(snap.total)}</span></div>`
                : isActive(st.state) ? html`<span class="num">${fmtLag(st.lagMs)}</span>` : html`<span class="subtle">—</span>`}</td>
              <td data-label="Rate" class="num-col hide-sm"><div class="row" style="justify-content:flex-end;flex-wrap:nowrap;gap:8px">
                <span class="hide-sm"><${Sparkline} values=${(live.hist[t.id] || []).map((p) => p.rate)} width=${64} height=${22} tone=${info.tone === 'ok' || info.tone === 'sync' ? 'primary' : 'muted'} /></span>
                <span class="num">${fmtRate(st.rate || 0)}</span></div></td>
              <td data-label="Tags" class="hide-md rt-tags"><div class="tags">${(t.tags || []).map((g) => html`<button type="button" class="tag" style="border:0;cursor:pointer" onClick=${() => setTag(g)} aria-label=${`Filter by tag ${g}`}>${g}</button>`)}</div></td>
              <td class="col-actions"><${QuickActions} task=${t} onChanged=${() => reload(true)} /></td>
            </tr>`;
          })}</tbody>
        </table></div>`}
    </div>
    ${data && html`<p class="subtle" style="margin-top:10px;font-size:12.5px">${rows.length} of ${data.length} tasks</p>`}

    ${sel.size > 0 && html`<div class="batch-bar" role="toolbar" aria-label="Batch actions" data-testid="batch-bar">
      <span class="batch-count"><b>${sel.size}</b> selected</span>
      ${BATCH.map((a) => html`<${Button} key=${a} size="sm" icon=${BATCH_ICON[a]} loading=${busy === a} disabled=${!!busy} onClick=${() => batch(a)} aria-label=${`${ACTION_LABELS[a]} selected tasks`}>${ACTION_LABELS[a]}</${Button}>`)}
      ${can('task.delete') && html`<${Button} size="sm" kind="danger-ghost" icon="trash" loading=${busy === 'delete'} disabled=${!!busy} onClick=${() => batch('delete')} aria-label="Delete selected tasks">Delete</${Button}>`}
      <${Button} size="sm" kind="ghost" icon="x" onClick=${() => setSel(new Set())} aria-label="Clear selection" />
    </div>`}
  `;
}
