// Indexes: grouped per Meilisearch instance (multi-destination) or a flat list (single instance).
import { html, useEffect, useLayoutEffect, useMemo, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { can } from '../auth.js';
import { singleDestination, useDestinations } from '../destinations.js';
import { useFetch } from '../hooks.js';
import { redirect } from '../router.js';
import { Icon } from '../icons.js';
import { Alert, Button, Counter, Empty, ErrorBox, Field, Modal, PageHeader, Skeleton, Toggle, confirmDialog, toast, toastError } from '../ui.js';
import { cls, fmtCompact, fmtInt, timeAgo } from '../util.js';
import { HealthDot, ScopeBadge } from './destinations.js';

/** Typed confirmation, then delete. `destination` scopes the call (undefined = the switcher's choice). */
export async function deleteIndexFlow(ix, destination) {
  const ok = await confirmDialog({
    title: `Delete index “${ix.uid}”?`,
    message: `All ${fmtCompact(ix.numberOfDocuments || (ix.stats && ix.stats.numberOfDocuments) || 0)} documents and the index settings are permanently deleted.${ix.managedBy && ix.managedBy.length ? ` Tasks writing to it: ${ix.managedBy.join(', ')}.` : ''}`,
    typed: ix.uid, danger: true, confirmLabel: 'Delete index',
  });
  if (!ok) return false;
  try { const r = await api.deleteIndex(ix.uid, destination); toast({ title: 'Index deletion enqueued', message: `Meilisearch task #${r.taskUid}` }); return true; }
  catch (e) { toastError(e, 'Could not delete index'); return false; }
}

function InstanceSelect({ id, value, onChange, dests, label = 'Meilisearch instance' }) {
  return html`<${Field} label=${label} htmlFor=${id}>
    <select id=${id} class="select" value=${value} onChange=${(e) => onChange(e.currentTarget.value)}>
      ${dests.map((d) => html`<option value=${d.id} disabled=${d.health && !d.health.healthy}>${d.name}${d.isDefault ? ' (default)' : ''}${d.health && !d.health.healthy ? ' — unreachable' : ''}</option>`)}</select></${Field}>`;
}

function CreateIndexModal({ open, onClose, onDone, dests, initialDest }) {
  const [uid, setUid] = useState('');
  const [pk, setPk] = useState('');
  const [dest, setDest] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  useLayoutEffect(() => { if (open) { setUid(''); setPk(''); setErr(''); setDest(initialDest || ''); } }, [open]);
  const valid = /^[a-zA-Z0-9_-]{1,400}$/.test(uid);
  const submit = async (e) => {
    e && e.preventDefault();
    if (!valid) { setErr('Use letters, digits, - or _'); return; }
    setBusy(true);
    try {
      const r = await api.createIndex(uid, pk.trim(), dests ? dest : undefined);
      const name = dests ? (dests.find((d) => d.id === dest) || {}).name : '';
      toast({ title: `Index “${uid}” created${name ? ` on ${name}` : ''}`, message: `Meilisearch task #${r.taskUid}` }); onDone(); onClose();
    } catch (er) { setErr(er.message); }
    setBusy(false);
  };
  return html`<${Modal} open=${open} onClose=${onClose} title="Create index" size="sm"
    footer=${html`<${Button} onClick=${onClose}>Cancel</${Button}><${Button} kind="primary" icon="plus" loading=${busy} disabled=${!uid} onClick=${submit}>Create</${Button}>`}>
    <form onSubmit=${submit} class="stack">
      ${dests && html`<${InstanceSelect} id="ci-dest" value=${dest} onChange=${setDest} dests=${dests} />`}
      <${Field} label="Index uid" required htmlFor="ci-uid" error=${err}><input id="ci-uid" class="input mono" autocapitalize="off" data-autofocus value=${uid} onInput=${(e) => { setUid(e.currentTarget.value); setErr(''); }} placeholder="products" /></${Field}>
      <${Field} label="Primary key" htmlFor="ci-pk" hint="Optional: inferred from the first documents when empty."><input id="ci-pk" class="input mono" autocapitalize="off" value=${pk} onInput=${(e) => setPk(e.currentTarget.value)} placeholder="id" /></${Field}>
      <button type="submit" hidden></button>
    </form>
  </${Modal}>`;
}

function SwapModal({ open, onClose, groups, dests, initialDest, onDone }) {
  const [dest, setDest] = useState('');
  const [a, setA] = useState('');
  const [b, setB] = useState('');
  const [rename, setRename] = useState(false);
  const [busy, setBusy] = useState(false);
  useLayoutEffect(() => { if (open) { setDest(initialDest || ''); setA(''); setB(''); setRename(false); } }, [open]);
  const uids = ((groups.find((g) => g.destinationId === dest) || groups[0] || {}).indexes || []).map((i) => i.uid);
  const ok = a && b && a !== b && (rename ? !uids.includes(b) : uids.includes(b));
  const submit = async () => {
    setBusy(true);
    try { const r = await api.swap(a, b, rename, dests ? dest : undefined); toast({ title: rename ? `Renamed ${a} to ${b}` : `Swapped ${a} and ${b}`, message: `Meilisearch task #${r.taskUid}` }); onDone(); onClose(); }
    catch (e) { toastError(e, 'Swap failed'); }
    setBusy(false);
  };
  return html`<${Modal} open=${open} onClose=${onClose} title="Swap indexes" size="sm"
    footer=${html`<${Button} onClick=${onClose}>Cancel</${Button}><${Button} kind="primary" icon="swap" disabled=${!ok} loading=${busy} onClick=${submit} data-testid="swap-confirm">${rename ? 'Rename' : 'Swap'}</${Button}>`}>
    <p class="muted">Swapping exchanges the documents and settings of two indexes atomically — searches never see a half-built index.</p>
    ${dests && html`<${InstanceSelect} id="sw-dest" value=${dest} onChange=${(v) => { setDest(v); setA(''); setB(''); }} dests=${dests} />`}
    <${Field} label="First index" htmlFor="sw-a"><select id="sw-a" class="select" value=${a} onChange=${(e) => setA(e.currentTarget.value)}><option value="">Choose…</option>${uids.map((u) => html`<option value=${u}>${u}</option>`)}</select></${Field}>
    ${rename ? html`<${Field} label="New uid" htmlFor="sw-b" hint="Must not exist yet."><input id="sw-b" class="input mono" value=${b} onInput=${(e) => setB(e.currentTarget.value)} /></${Field}>`
      : html`<${Field} label="Second index" htmlFor="sw-b"><select id="sw-b" class="select" value=${b} onChange=${(e) => setB(e.currentTarget.value)}><option value="">Choose…</option>${uids.filter((u) => u !== a).map((u) => html`<option value=${u}>${u}</option>`)}</select></${Field}>`}
    <${Toggle} id="sw-rename" label="Rename instead of swap" hint="Moves the first index to a new uid." checked=${rename} onChange=${(v) => { setRename(v); setB(''); }} />
  </${Modal}>`;
}

export function IndexCard({ ix, i, destination, onChanged }) {
  const href = `#/indexes/${encodeURIComponent(ix.uid)}${destination ? `?destination=${encodeURIComponent(destination)}` : ''}`;
  return html`<article class="card index-card is-hoverable" key=${ix.uid} style=${`--i:${i}`} data-index=${ix.uid} data-index-dest=${destination || ''}>
    <div class="index-card-head">
      <span class=${`index-glyph ${ix.managedBy && ix.managedBy.length ? '' : 'alt'}`}><${Icon} name="indexes" size=${20} /></span>
      <div class="grow"><a class="index-uid" href=${href}>${ix.uid}</a>
        <span class="subtle" style="font-size:12px">primary key <span class="mono">${ix.primaryKey || '—'}</span> · updated ${timeAgo(ix.updatedAt)}</span></div>
      ${can('index.write') && html`<${Button} kind="ghost" size="sm" icon="trash" aria-label=${`Delete index ${ix.uid}`} onClick=${async () => { if (await deleteIndexFlow(ix, destination)) onChanged(); }} />`}
    </div>
    <div class="row-between">
      <div class="mini-stat"><span>Documents</span><b style="font-size:22px"><${Counter} value=${ix.numberOfDocuments} /></b></div>
      ${ix.isIndexing ? html`<span class="indexing"><span class="spinner"></span>Indexing</span>` : html`<span class="subtle" style="font-size:12px">idle</span>`}
    </div>
    <div class="row" style="gap:6px;font-size:12.5px">
      ${ix.managedBy && ix.managedBy.length ? html`<span class="subtle">Managed by</span>${(ix.managedByTasks || ix.managedBy.map((id) => ({ id, name: id }))).map((t) => html`<a class="tag tag-primary" href=${`#/tasks/${encodeURIComponent(t.id)}`} title=${t.id}>${t.name}</a>`)}`
        : html`<span class="subtle">Not managed by a task</span>`}
    </div>
  </article>`;
}

const skeletonGrid = () => html`<div class="grid grid-auto">${[0, 1, 2].map((k) => html`<div class="card skeleton-card" key=${k}><${Skeleton} w="50%" h=${18} /><${Skeleton} w="30%" h=${26} /><${Skeleton} w="70%" h=${12} /></div>`)}</div>`;

// ---------------------------------------------------------------- multi-instance page
function GroupedIndexes({ instance, dests }) {
  const { data, error, reload } = useFetch(() => api.indexesAll(), [], { interval: 10000 });
  const [q, setQ] = useState('');
  const [modal, setModal] = useState('');
  const [collapsed, setCollapsed] = useState({});
  const groups = data || [];
  const shown = groups.filter((g) => !instance || g.destinationId === instance);
  const match = (ix) => !q || ix.uid.toLowerCase().includes(q.toLowerCase());
  const setInstance = (v) => redirect(`#/indexes${v ? `?instance=${encodeURIComponent(v)}` : ''}`);
  const def = (dests.find((d) => d.isDefault) || dests[0] || {}).id;
  const totalDocs = groups.reduce((a, g) => a + g.indexes.reduce((x, i) => x + i.numberOfDocuments, 0), 0);
  return html`
    <${PageHeader} title="Indexes" subtitle=${data ? html`<${Counter} value=${totalDocs} format=${fmtCompact} /> documents in ${groups.reduce((a, g) => a + g.indexes.length, 0)} indexes across ${groups.length} Meilisearch instances.` : 'Indexes of every Meilisearch instance.'}
      actions=${can('index.write') && html`<${Button} icon="swap" onClick=${() => setModal('swap')} disabled=${!data}>Swap</${Button}>
        <${Button} kind="primary" icon="plus" class="keep-label" onClick=${() => setModal('create')}>Create index</${Button}>`} />
    <div class="toolbar">
      <div class="input-icon"><${Icon} name="search" size=${16} /><input class="input" type="search" placeholder="Filter indexes" aria-label="Filter indexes" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} /></div>
      <select class="select" aria-label="Meilisearch instance" value=${instance} onChange=${(e) => setInstance(e.currentTarget.value)} data-testid="instance-filter">
        <option value="">All instances</option>${dests.map((d) => html`<option value=${d.id}>${d.name}</option>`)}</select>
    </div>
    <${ErrorBox} error=${error && !data && error} onRetry=${() => reload()} />
    ${!data ? (error ? null : skeletonGrid()) : html`<div class="stack instance-groups">${shown.map((g) => {
      const list = g.indexes.filter(match).sort((x, y) => x.uid.localeCompare(y.uid));
      const docs = g.indexes.reduce((a, i) => a + i.numberOfDocuments, 0);
      const isOpen = !collapsed[g.destinationId];
      return html`<section class=${cls('instance-group', !g.healthy && 'is-down')} key=${g.destinationId} data-instance=${g.destinationId} aria-labelledby=${`ig-${g.destinationId}`}>
        <header class="instance-head">
          <button type="button" class="instance-toggle" aria-expanded=${isOpen} aria-controls=${`ig-body-${g.destinationId}`} onClick=${() => setCollapsed({ ...collapsed, [g.destinationId]: isOpen })}>
            <${Icon} name="chevron-down" size=${16} class="acc-caret" />
            <${HealthDot} healthy=${g.healthy} label=${`${g.destinationName} ${g.healthy ? 'healthy' : 'unreachable'}`} />
            <h2 id=${`ig-${g.destinationId}`}>${g.destinationName}</h2>
            ${g.isDefault && html`<span class="tag tag-primary">default</span>`}
            <span class="subtle instance-counts">${g.healthy ? `${fmtInt(g.indexes.length)} index${g.indexes.length === 1 ? '' : 'es'} · ${fmtCompact(docs)} documents` : 'unreachable'}</span>
          </button>
          <a class="btn btn-ghost btn-sm" href=${`#/destinations/${encodeURIComponent(g.destinationId)}`}><span>Instance</span><${Icon} name="arrow-right" size=${14} /></a>
        </header>
        ${isOpen && html`<div id=${`ig-body-${g.destinationId}`} class="instance-body">
          ${!g.healthy ? html`<${Alert} tone="bad" title="Unreachable">${g.error || 'This Meilisearch instance did not answer.'}</${Alert}>`
            : !list.length ? html`<p class="subtle instance-empty">${q ? 'No index matches the filter.' : 'No index yet on this instance.'}</p>`
            : html`<div class="grid grid-auto stagger">${list.map((ix, i) => html`<${IndexCard} key=${ix.uid} ix=${ix} i=${i} destination=${g.destinationId} onChanged=${() => reload(true)} />`)}</div>`}
        </div>`}
      </section>`;
    })}</div>`}
    <${CreateIndexModal} open=${modal === 'create'} onClose=${() => setModal('')} onDone=${() => reload(true)} dests=${dests} initialDest=${instance || def} />
    <${SwapModal} open=${modal === 'swap'} onClose=${() => setModal('')} groups=${groups} dests=${dests} initialDest=${instance || def} onDone=${() => reload(true)} />
  `;
}

// ---------------------------------------------------------------- single-instance page
function FlatIndexes() {
  const { data, error, reload } = useFetch(() => api.indexes(), [], { interval: 5000 });
  const [q, setQ] = useState('');
  const [modal, setModal] = useState('');
  const list = useMemo(() => (data || []).filter((i) => !q || i.uid.toLowerCase().includes(q.toLowerCase())).sort((a, b) => a.uid.localeCompare(b.uid)), [data, q]);
  const total = (data || []).reduce((a, i) => a + i.numberOfDocuments, 0);
  return html`
    <${PageHeader} title="Indexes" children=${html`<${ScopeBadge} />`} subtitle=${data ? html`<${Counter} value=${total} format=${fmtCompact} /> documents across ${data.length} Meilisearch indexes.` : 'Meilisearch indexes and the tasks that write to them.'}
      actions=${can('index.write') && html`<${Button} icon="swap" onClick=${() => setModal('swap')} disabled=${!data || data.length < 1}>Swap</${Button}>
        <${Button} kind="primary" icon="plus" class="keep-label" onClick=${() => setModal('create')}>Create index</${Button}>`} />
    <div class="toolbar"><div class="input-icon"><${Icon} name="search" size=${16} /><input class="input" type="search" placeholder="Filter indexes" aria-label="Filter indexes" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} /></div></div>
    ${error && error.status === 409 && !data ? html`<div class="card"><${Empty} icon="database" title="No Meilisearch destination yet"
        action=${html`<a class="btn btn-primary" href="#/destinations"><${Icon} name="plus" /><span>Add a Meilisearch destination</span></a>`}>Connect a Meilisearch instance to browse its indexes and tasks.</${Empty}></div>`
      : html`<${ErrorBox} error=${error && !data && error} onRetry=${() => reload()} />`}
    ${!data ? (error ? null : skeletonGrid())
      : !list.length ? html`<div class="card"><${Empty} icon="indexes" title=${q ? 'No matching index' : 'No indexes yet'}
          action=${!q && can('index.write') && html`<${Button} kind="primary" icon="plus" onClick=${() => setModal('create')}>Create index</${Button}>`}>${q ? 'Try another name.' : 'Indexes are created automatically when a task first writes, or you can create one now.'}</${Empty}></div>`
      : html`<div class="grid grid-auto stagger" data-testid="index-list">${list.map((ix, i) => html`<${IndexCard} key=${ix.uid} ix=${ix} i=${i} onChanged=${() => reload(true)} />`)}</div>`}
    <${CreateIndexModal} open=${modal === 'create'} onClose=${() => setModal('')} onDone=${() => reload(true)} />
    <${SwapModal} open=${modal === 'swap'} onClose=${() => setModal('')} groups=${[{ destinationId: '', indexes: data || [] }]} onDone=${() => reload(true)} />
  `;
}

export function IndexesPage({ instance = '' }) {
  const dests = useDestinations();
  const [mode, setMode] = useState(null);
  useEffect(() => { if (dests) setMode(!singleDestination() && dests.length > 1 ? 'grouped' : 'flat'); }, [dests && dests.length]);
  if (!mode) return html`<${PageHeader} title="Indexes" />${skeletonGrid()}`;
  return mode === 'grouped' ? html`<${GroupedIndexes} instance=${instance} dests=${dests} />` : html`<${FlatIndexes} />`;
}
