// Index detail: stats, field distribution, search playground, documents, settings editor.
import { html, useEffect, useRef, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { ChangeList, SettingsEditor } from '../settings-editor.js';
import { ScopeBadge } from './destinations.js';
import { can } from '../auth.js';
import { useFetch } from '../hooks.js';
import { navigate } from '../router.js';
import { Icon } from '../icons.js';
import { Alert, Button, Counter, Empty, ErrorBox, JsonView, Modal, Progress, Skeleton, SkeletonRows, Tabs, Tag, confirmDialog, toast, toastError } from '../ui.js';
import { debounce, fmtBytes, fmtCompact, fmtInt, pretty, timeAgo } from '../util.js';
import { deleteIndexFlow } from './indexes.js';

const short = (v) => (v == null ? 'null' : typeof v === 'object' ? JSON.stringify(v) : String(v));

// ---------------------------------------------------------------- search
function SearchTab({ uid, filterable }) {
  const [q, setQ] = useState('');
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState('');
  const [limit, setLimit] = useState(20);
  const [res, setRes] = useState(null);
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const [raw, setRaw] = useState(false);
  const ctl = useRef(null);
  const run = useRef(null);
  if (!run.current) {
    run.current = debounce(async (body) => {
      if (ctl.current) ctl.current.abort();
      ctl.current = new AbortController();
      setBusy(true);
      try { setRes(await api.search(uid, body, { signal: ctl.current.signal })); setErr(null); }
      catch (e) { if (e.name !== 'AbortError') setErr(e); }
      setBusy(false);
    }, 220);
  }
  useEffect(() => {
    run.current({ q, filter: filter.trim() || undefined, sort: sort.split(',').map((s) => s.trim()).filter(Boolean), limit: Number(limit) || 20 });
  }, [q, filter, sort, limit]);
  useEffect(() => () => { run.current.cancel(); if (ctl.current) ctl.current.abort(); }, []);

  return html`<div class="stack tab-panel" data-testid="search-playground">
    <div class="search-bar">
      <div class="input-icon"><${Icon} name="search" size=${16} /><input class="input" type="search" placeholder="Search…" aria-label="Search query" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} data-testid="search-q" autofocus /></div>
      <input class="input mono" placeholder=${filterable.length ? `${filterable[0]} = …` : 'filter, e.g. year > 2000'} aria-label="Filter expression" value=${filter} onInput=${(e) => setFilter(e.currentTarget.value)} data-testid="search-filter" />
      <input class="input mono" placeholder="sort, e.g. price:asc" aria-label="Sort (comma separated field:asc|desc)" value=${sort} onInput=${(e) => setSort(e.currentTarget.value)} />
      <select class="select" aria-label="Result limit" value=${limit} onChange=${(e) => setLimit(e.currentTarget.value)}>${[10, 20, 50, 100].map((n) => html`<option value=${n}>${n}</option>`)}</select>
    </div>
    ${err && html`<${Alert} tone="bad" title="Search error">${err.message}</${Alert}>`}
    <div class="search-meta" role="status" aria-live="polite">
      ${busy && html`<span class="spinner" aria-hidden="true"></span>`}
      ${res && html`<span><b data-testid="search-total">${fmtInt(res.estimatedTotalHits ?? res.totalHits ?? res.hits.length)}</b> results</span>
        <span>in <b data-testid="search-time">${res.processingTimeMs}</b> ms</span>`}
      <label class="check-label" style="margin-left:auto"><input type="checkbox" class="checkbox" checked=${raw} onChange=${(e) => setRaw(e.currentTarget.checked)} />Raw JSON</label>
    </div>
    ${!res ? html`<div class="hits">${[0, 1, 2, 3].map((i) => html`<div class="hit" key=${i}><${Skeleton} w="60%" h=${16} /><div style="height:10px"></div><${Skeleton} h=${10} /><div style="height:6px"></div><${Skeleton} w="80%" h=${10} /></div>`)}</div>`
      : !res.hits.length ? html`<div class="card"><${Empty} icon="search" title="No results">Nothing matches “${q}”${filter ? ' with this filter' : ''}.</${Empty}></div>`
      : raw ? html`<${JsonView} value=${res} maxHeight=${560} label="Search response" />`
      : html`<div class="hits" data-testid="search-hits">${res.hits.map((h, i) => {
          const title = h.name || h.title || h.email || h.uid || h.id;
          const fields = Object.entries(h).filter(([k]) => !k.startsWith('_formatted')).slice(0, 7);
          return html`<article class="hit" key=${i} style=${`--i:${Math.min(i, 12)}`}>
            <div class="hit-title"><span>${short(title)}</span><span class="mono">#${short(h.id ?? i)}</span></div>
            <dl>${fields.map(([k, v]) => html`<dt>${k}</dt><dd title=${short(v)}>${short(v)}</dd>`)}</dl>
          </article>`;
        })}</div>`}
  </div>`;
}

// ---------------------------------------------------------------- documents
function DocumentsTab({ uid }) {
  const [offset, setOffset] = useState(0);
  const limit = 20;
  const { data, error, loading, reload } = useFetch(() => api.documents(uid, limit, offset), [uid, offset]);
  const [view, setView] = useState(null);
  const docs = (data && data.results) || [];
  const cols = docs.length ? Object.keys(docs[0]).slice(0, 6) : [];
  const total = data ? data.total : 0;
  return html`<section class="card tab-panel" data-testid="documents-tab">
    <${ErrorBox} error=${error} onRetry=${() => reload()} />
    ${!data ? html`<${SkeletonRows} rows=${8} cols=${5} />`
      : !docs.length ? html`<${Empty} icon="doc" title="No documents">This index is empty.</${Empty}>`
      : html`<div class="table-wrap" style=${loading ? 'opacity:.6;transition:opacity .2s' : 'transition:opacity .2s'}><table class="table table-compact rtable">
          <thead><tr>${cols.map((c) => html`<th>${c}</th>`)}<th class="col-actions"><span class="sr-only">View</span></th></tr></thead>
          <tbody>${docs.map((d, i) => html`<tr key=${`${offset}-${i}`} style=${`--i:${Math.min(i, 12)}`}>
            ${cols.map((c, k) => html`<td class=${k === 0 ? 'doc-cell rt-title' : 'doc-cell'} data-label=${k === 0 ? undefined : c} title=${short(d[c])}>${short(d[c])}</td>`)}
            <td class="col-actions"><${Button} size="sm" kind="ghost" icon="eye" aria-label="View document JSON" onClick=${() => setView(d)} /></td></tr>`)}</tbody>
        </table></div>`}
    <div class="pager">
      <span>${total ? `${fmtInt(offset + 1)}–${fmtInt(Math.min(offset + limit, total))} of ${fmtInt(total)}` : ''}</span>
      <${Button} size="sm" icon="chevron-left" disabled=${offset === 0 || loading} onClick=${() => setOffset(Math.max(0, offset - limit))} aria-label="Previous page">Prev</${Button}>
      <${Button} size="sm" iconRight="chevron-right" disabled=${offset + limit >= total || loading} onClick=${() => setOffset(offset + limit)} aria-label="Next page">Next</${Button}>
    </div>
    <${Modal} open=${!!view} onClose=${() => setView(null)} title="Document" size="lg">${view && html`<${JsonView} value=${view} maxHeight=${480} label="Document" />`}</${Modal}>
  </section>`;
}

// ---------------------------------------------------------------- settings
function SettingsTab({ uid, suggestions }) {
  const { data, error, reload } = useFetch(() => api.settings(uid), [uid]);
  const [draft, setDraft] = useState(null);
  const [changes, setChanges] = useState(null);
  const [busy, setBusy] = useState('');
  useEffect(() => { if (data) { setDraft(data); setChanges(null); } }, [data]);
  const admin = can('index.write');
  const dirty = data && draft && JSON.stringify(draft) !== JSON.stringify(data);
  const preview = async () => {
    setBusy('preview');
    try { setChanges((await api.previewSettings(uid, draft)).changes || []); } catch (e) { toastError(e, 'Preview failed'); }
    setBusy('');
  };
  const apply = async () => {
    setBusy('apply');
    try {
      const r = await api.applySettings(uid, draft);
      if (r.status === 'failed') toast({ tone: 'bad', title: 'Settings update failed', message: r.error });
      else { toast({ title: r.status === 'succeeded' ? 'Settings applied' : 'Settings update enqueued', message: `Meilisearch task #${r.taskUid}` }); setChanges(null); reload(true); }
    } catch (e) { toastError(e, 'Could not apply settings'); }
    setBusy('');
  };
  const reset = async () => {
    const ok = await confirmDialog({ title: 'Reset all settings?', message: `Every setting of “${uid}” returns to the Meilisearch default. Tasks that manage settings re-apply theirs on the next start.`, danger: true, confirmLabel: 'Reset settings' });
    if (!ok) return;
    try { const r = await api.resetSettings(uid); toast({ title: 'Settings reset enqueued', message: `Meilisearch task #${r.taskUid}` }); setTimeout(() => reload(true), 400); }
    catch (e) { toastError(e, 'Reset failed'); }
  };
  if (error && !data) return html`<${ErrorBox} error=${error} onRetry=${() => reload()} />`;
  return html`<div class="stack tab-panel" data-testid="settings-tab">
    <section class="card">
      <div class="card-head"><h3><${Icon} name="settings" />Index settings</h3>
        ${admin ? html`<div class="btn-group">${dirty && html`<span class="tag tag-primary unsaved">edited</span>`}<${Button} size="sm" kind="ghost" onClick=${reset}>Reset to defaults</${Button}></div>`
          : html`<span class="tag" title="Requires admin role">read only</span>`}</div>
      <div class="card-body">
        ${!draft ? html`<${Skeleton} h=${320} />` : html`<${SettingsEditor} value=${draft} onChange=${(v) => { setDraft(v); setChanges(null); }} idPrefix="ix"
          jsonTestId="index-settings-json" suggestions=${suggestions} readOnly=${!admin} />`}
      </div>
      ${admin && html`<div class="card-foot">
        <${Button} onClick=${() => { setDraft(data); setChanges(null); }} disabled=${!dirty}>Revert</${Button}>
        <${Button} kind="primary" icon="eye" loading=${busy === 'preview'} onClick=${preview} disabled=${!draft} data-testid="preview-settings">Preview changes</${Button}>
      </div>`}
    </section>
    ${changes !== null && html`<section class="card" aria-live="polite">
      <div class="card-head"><h3><${Icon} name="diff" />Changes</h3><${Tag} tone=${changes.length ? 'primary' : 'ok'}>${changes.length} change${changes.length === 1 ? '' : 's'}</${Tag}></div>
      <div class="card-body">${!changes.length ? html`<${Empty} icon="success" title="No changes">The index already has these settings.</${Empty}>`
        : html`<${ChangeList} changes=${changes} testid="settings-changes" />`}</div>
      ${changes.length > 0 && html`<div class="card-foot"><${Button} onClick=${() => setChanges(null)}>Cancel</${Button}>
        <${Button} kind="primary" icon="check" loading=${busy === 'apply'} onClick=${apply} data-testid="apply-settings">Apply ${changes.length} change${changes.length === 1 ? '' : 's'}</${Button}></div>`}
    </section>`}
  </div>`;
}

// ---------------------------------------------------------------- page
function PrimaryKeyEditor({ ix, onSaved }) {
  const [edit, setEdit] = useState(false);
  const [v, setV] = useState(ix.primaryKey || '');
  const [busy, setBusy] = useState(false);
  const save = async () => {
    setBusy(true);
    try { const r = await api.setPrimaryKey(ix.uid, v.trim()); toast({ title: 'Primary key updated', message: `Meilisearch task #${r.taskUid}` }); setEdit(false); onSaved(); }
    catch (e) { toastError(e, e.status === 409 ? 'Index has documents' : 'Could not change primary key'); }
    setBusy(false);
  };
  if (!edit) return html`<span class="row" style="gap:6px"><span class="mono" style="font-weight:600">${ix.primaryKey || '—'}</span>
    ${can('index.write') && html`<${Button} size="sm" kind="ghost" icon="edit" aria-label="Edit primary key" onClick=${() => { setV(ix.primaryKey || ''); setEdit(true); }} />`}</span>`;
  return html`<form class="row" style="gap:6px;flex-wrap:nowrap" onSubmit=${(e) => { e.preventDefault(); save(); }}>
    <input class="input input-sm mono" style="width:130px" value=${v} onInput=${(e) => setV(e.currentTarget.value)} aria-label="Primary key" autofocus />
    <${Button} size="sm" kind="primary" icon="check" type="submit" loading=${busy} aria-label="Save primary key" disabled=${!v.trim()} />
    <${Button} size="sm" kind="ghost" icon="x" aria-label="Cancel" onClick=${() => setEdit(false)} /></form>`;
}

export function IndexDetailPage({ uid, tab }) {
  const { data: ix, error, reload } = useFetch(() => api.index(uid), [uid], { interval: 5000 });
  const sett = useFetch(() => api.settings(uid), [uid]);
  const [compacting, setCompacting] = useState(false);
  if (error && !ix) return html`<a class="back-link" href="#/indexes"><${Icon} name="arrow-left" size=${15} />Indexes</a><div style="margin-top:16px"><${ErrorBox} error=${error} onRetry=${() => reload()} /></div>`;
  const stats = (ix && ix.stats) || {};
  const fd = Object.entries(stats.fieldDistribution || {}).sort((a, b) => b[1] - a[1]);
  const max = fd.length ? fd[0][1] : 1;
  const filterable = ((sett.data && sett.data.filterableAttributes) || []).map((f) => (typeof f === 'string' ? f : ((f && f.attributePatterns) || []).join(', ')));
  const tabs = [{ id: 'search', label: 'Search', icon: 'search' }, { id: 'documents', label: 'Documents', icon: 'doc' }, { id: 'settings', label: 'Settings', icon: 'settings' }];
  const active = tabs.some((t) => t.id === tab) ? tab : 'search';
  const compact = async () => {
    setCompacting(true);
    try { const r = await api.compact(uid); toast({ title: 'Compaction enqueued', message: `Meilisearch task #${r.taskUid}` }); } catch (e) { toastError(e, 'Compaction failed'); }
    setCompacting(false);
  };
  return html`
    <a class="back-link" href="#/indexes"><${Icon} name="arrow-left" size=${15} />Indexes</a>
    <header class="detail-head" style="margin-top:10px">
      <div class="detail-title"><span class="index-glyph" style="width:48px;height:48px;border-radius:14px"><${Icon} name="indexes" size=${24} /></span>
        <div style="min-width:0"><div class="row" style="gap:12px"><h1 class="mono">${uid}</h1>${ix && ix.stats && ix.stats.isIndexing && html`<span class="indexing"><span class="spinner"></span>Indexing</span>`}</div>
          <div class="detail-meta"><${ScopeBadge} />${ix ? html`<span>created ${timeAgo(ix.createdAt)}</span><span>updated ${timeAgo(ix.updatedAt)}</span>
            ${ix.managedBy && ix.managedBy.length ? html`<span>managed by ${ix.managedBy.map((t) => html` <a href=${`#/tasks/${encodeURIComponent(t)}`}>${t}</a>`)}</span>` : html`<span>not managed by a task</span>`}` : html`<${Skeleton} w=${220} />`}</div></div>
      </div>
      <div class="btn-group">
        ${can('index.write') && html`<${Button} size="sm" icon="compact" loading=${compacting} onClick=${compact}>Compact</${Button}>
        <${Button} size="sm" kind="danger-ghost" icon="trash" disabled=${!ix} onClick=${async () => { if (await deleteIndexFlow({ ...ix, numberOfDocuments: stats.numberOfDocuments })) navigate('#/indexes'); }} data-testid="delete-index">Delete</${Button}>`}
      </div>
    </header>

    <div class="split" style="margin-bottom:20px">
      <section class="card"><div class="card-head"><h3><${Icon} name="layers" />Field distribution</h3><span class="subtle">${fd.length} fields</span></div>
        <div class="card-body stack-sm" style="max-height:260px;overflow:auto">
          ${!ix ? [0, 1, 2, 3].map((i) => html`<${Skeleton} key=${i} h=${10} />`)
            : !fd.length ? html`<p class="subtle">No fields yet.</p>`
            : fd.map(([k, n]) => html`<div class="fd-row" key=${k}><span class="mono" title=${k}>${k}</span><${Progress} value=${n} max=${max} tone="primary" label=${`${k} present in ${n} documents`} /><span class="num">${fmtCompact(n)}</span></div>`)}
        </div></section>
      <section class="card card-pad"><dl class="kv">
        <dt>Documents</dt><dd style="font-size:20px;font-weight:700">${ix ? html`<${Counter} value=${stats.numberOfDocuments || 0} />` : html`<${Skeleton} w=${90} />`}</dd>
        <dt>Primary key</dt><dd>${ix ? html`<${PrimaryKeyEditor} ix=${ix} onSaved=${() => reload(true)} />` : '…'}</dd>
        ${stats.rawDocumentDbSize != null && html`<dt>Raw size</dt><dd>${fmtBytes(stats.rawDocumentDbSize)}</dd>`}
        ${stats.avgDocumentSize != null && html`<dt>Avg. document</dt><dd>${fmtBytes(stats.avgDocumentSize)}</dd>`}
        ${stats.numberOfEmbeddings != null && html`<dt>Embeddings</dt><dd>${fmtInt(stats.numberOfEmbeddings)}</dd>`}
        <dt>Filterable</dt><dd><div class="tags">${filterable.length ? filterable.map((f) => html`<${Tag} tone="primary">${f}</${Tag}>`) : html`<span class="subtle">none</span>`}</div></dd>
      </dl></section>
    </div>

    <${Tabs} tabs=${tabs} active=${active} label="Index sections" onChange=${(t) => navigate(`#/indexes/${encodeURIComponent(uid)}${t === 'search' ? '' : `/${t}`}`)} />
    <div role="tabpanel" aria-labelledby=${`tab-${active}`} key=${active}>
      ${active === 'search' ? html`<${SearchTab} uid=${uid} filterable=${filterable} />` : active === 'documents' ? html`<${DocumentsTab} uid=${uid} />` : html`<${SettingsTab} uid=${uid} suggestions=${fd.map(([k]) => k)} />`}
    </div>
  `;
}
