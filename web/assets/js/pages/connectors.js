// Connectors: reusable connection settings (list, editor, test, table browser).
import { html, useLayoutEffect, useMemo, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { can } from '../auth.js';
import { CONN_FIELDS, ConnectionFields, connectionHost, pickConnection, validateConnection } from '../connection.js';
import { SourceIcon } from '../components.js';
import { useFetch } from '../hooks.js';
import { navigate } from '../router.js';
import { Icon } from '../icons.js';
import { HelpLink } from '../docs.js';
import { Alert, Button, Empty, ErrorBox, Field, Modal, PageHeader, Skeleton, SkeletonRows, StateBadge, Tag, confirmDialog, toast, toastError } from '../ui.js';
import { SOURCES, debounce, fmtInt, sourceInfo, timeAgo } from '../util.js';

export const connectorsChanged = () => window.dispatchEvent(new CustomEvent('cdc:connectors'));

// ---------------------------------------------------------------- test result
export function ConnectorTestResult({ r, onPickTable }) {
  if (!r) return null;
  if (!r.ok) return html`<div class="test-result" data-testid="connector-test"><${Alert} tone="bad" title="Connection failed">${r.error}</${Alert}></div>`;
  const tables = r.tables || [];
  return html`<div class="test-result stack-sm" data-testid="connector-test">
    <${Alert} tone="ok" title="Connected">
      <span class="row" style="gap:8px">${r.version && html`<${Tag} tone="violet">${r.version}</${Tag}>`}<span class="num">${r.latencyMs} ms</span><span>· ${tables.length} table${tables.length === 1 ? '' : 's'} discovered</span></span></${Alert}>
    ${tables.length > 0 && html`<ul class="table-chips" aria-label="Discovered tables">${tables.slice(0, 40).map((t) => html`<li>${onPickTable
      ? html`<button type="button" class="chip" onClick=${() => onPickTable(t.name)}>${t.schema && t.schema !== 'public' ? `${t.schema}.` : ''}${t.name}</button>`
      : html`<span class="chip chip-static">${t.name}</span>`}</li>`)}</ul>`}
  </div>`;
}

// ---------------------------------------------------------------- editor
export function ConnectorEditor({ open, connector, onClose, onSaved }) {
  const editing = !!connector;
  const [f, setF] = useState({ type: '', name: '', description: '', tags: '', config: {} });
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState('');
  const [err, setErr] = useState(null);
  const [test, setTest] = useState(null);
  useLayoutEffect(() => {
    if (!open) return;
    setF(connector ? { type: connector.type, name: connector.name, description: connector.description || '', tags: (connector.tags || []).join(', '), config: { driver: 'pgx', ...connector.config } }
      : { type: '', name: '', description: '', tags: '', config: { driver: 'pgx' } });
    setTried(false); setErr(null); setTest(null); setBusy('');
  }, [open, connector && connector.id]);
  const connErr = f.type ? validateConnection(f.type, f.config) : {};
  const errs = { ...(f.type ? {} : { type: 'Choose a source type' }), ...(f.name.trim() ? {} : { name: 'Give the connector a name' }), ...connErr };
  const body = () => ({ ...(editing ? { id: connector.id } : {}), name: f.name.trim(), description: f.description.trim(), type: f.type, tags: f.tags.split(',').map((x) => x.trim()).filter(Boolean), config: pickConnection(f.type, f.config) });
  const runTest = async () => {
    setTried(true);
    if (Object.keys(connErr).length || !f.type) return;
    setBusy('test'); setTest(null);
    try { setTest(await api.testConnectorDraft(body())); } catch (e) { setTest({ ok: false, error: e.message }); }
    setBusy('');
  };
  const save = async () => {
    setTried(true);
    if (Object.keys(errs).length) return;
    setBusy('save'); setErr(null);
    try {
      const v = editing ? await api.updateConnector(connector.id, body()) : await api.createConnector(body());
      toast({ title: editing ? 'Connector saved' : 'Connector created', message: v.name });
      connectorsChanged();
      onSaved(v); onClose();
    } catch (e) { setErr(e); setBusy(''); }
  };
  const show = (k) => (tried ? errs[k] : '');
  return html`<${Modal} open=${open} onClose=${onClose} title=${editing ? `Edit ${connector.name}` : 'New connector'} size="lg" help=${{ doc: 'connectors', source: f.type, label: `${f.type ? sourceInfo(f.type).label : 'Connector'} documentation` }}
    footer=${html`${can('connector.test') && html`<${Button} icon="plug" loading=${busy === 'test'} onClick=${runTest} disabled=${!f.type} data-testid="connector-test-btn">Test connection</${Button}>`}
      <span class="grow"></span><${Button} onClick=${onClose}>Cancel</${Button}>
      <${Button} kind="primary" icon="check" loading=${busy === 'save'} onClick=${save} data-testid="connector-save">${editing ? 'Save connector' : 'Create connector'}</${Button}>`}>
    <div class="stack">
      <${ErrorBox} error=${err} />
      ${!editing ? html`<${Field} label="Type" error=${show('type')}>
          <div class="source-grid source-grid-compact" role="group" aria-label="Source type">${SOURCES.map((src, i) => html`<button type="button" key=${src.type} class="source-card is-compact" style=${`--i:${i}`}
            aria-pressed=${f.type === src.type} data-source=${src.type} onClick=${() => { setF({ ...f, type: src.type }); setTest(null); }}>
            <${SourceIcon} type=${src.type} /><b>${src.label}</b></button>`)}</div></${Field}>`
        : html`<div class="row" style="gap:10px"><${SourceIcon} type=${f.type} /><b>${sourceInfo(f.type).label}</b><span class="subtle">The type cannot be changed.</span></div>`}
      ${f.type && html`<div class="form-grid">
          <${Field} label="Name" required htmlFor="cn-name" error=${show('name')}><input id="cn-name" class="input" value=${f.name} placeholder="Shop Postgres (primary)" onInput=${(e) => setF({ ...f, name: e.currentTarget.value })} /></${Field}>
          <${Field} label="Tags" htmlFor="cn-tags" hint="Comma separated."><input id="cn-tags" class="input" autocapitalize="off" value=${f.tags} placeholder="shop, prod" onInput=${(e) => setF({ ...f, tags: e.currentTarget.value })} /></${Field}>
          <${Field} label="Description" htmlFor="cn-desc" class="span-2"><input id="cn-desc" class="input" value=${f.description} onInput=${(e) => setF({ ...f, description: e.currentTarget.value })} /></${Field}>
        </div>
        <div class="form-section"><h3><${SourceIcon} type=${f.type} size="sm" />Connection</h3>
          <${ConnectionFields} key=${f.type} type=${f.type} value=${f.config} onChange=${(c) => { setF({ ...f, config: c }); setTest(null); }} errors=${tried ? connErr : {}} masked=${editing} idPrefix="cn" /></div>
        <div aria-live="polite"><${ConnectorTestResult} r=${test} /></div>`}
    </div>
  </${Modal}>`;
}

// ---------------------------------------------------------------- delete
export async function deleteConnectorFlow(c) {
  const ok = await confirmDialog({ title: `Delete connector “${c.name}”?`, message: 'The stored connection settings and credentials are removed.', typed: c.name, danger: true, confirmLabel: 'Delete connector' });
  if (!ok) return false;
  try { await api.deleteConnector(c.id); toast({ title: 'Connector deleted', message: c.name }); connectorsChanged(); return true; }
  catch (e) {
    const used = (e.data && e.data.usedBy) || [];
    toast({ tone: 'bad', title: 'Connector is in use', message: used.length ? `Used by ${used.map((t) => t.name || t.id).join(', ')}. Move or delete those tasks first.` : e.message, timeout: 9000 });
    return false;
  }
}

// ---------------------------------------------------------------- list
function ConnectorCard({ c, i, onEdit, onChanged }) {
  const [busy, setBusy] = useState(false);
  const [tested, setLast] = useState(null);
  // The server keeps the latest test (also run on save); a test from this card wins until reload.
  const last = tested || c.lastTest || null;
  const s = sourceInfo(c.type);
  const test = async () => {
    setBusy(true);
    try { const r = await api.testConnector(c.id); setLast({ ...r, at: new Date().toISOString() }); toast({ tone: r.ok ? 'ok' : 'bad', title: r.ok ? `${c.name}: connected` : `${c.name}: failed`, message: r.ok ? `${r.latencyMs} ms${r.version ? ` · ${r.version}` : ''}` : r.error }); }
    catch (e) { toastError(e, 'Test failed'); }
    setBusy(false);
  };
  const used = c.usedBy || [];
  return html`<article class="card connector-card is-hoverable" style=${`--i:${i};--hue:${s.hue}`} data-connector=${c.id}>
    <div class="connector-head">
      <${SourceIcon} type=${c.type} />
      <div class="grow" style="min-width:0"><a class="connector-name" href=${`#/connectors/${encodeURIComponent(c.id)}`}>${c.name}</a>
        <span class="subtle connector-type">${s.label}</span></div>
      ${last && html`<span class=${`badge tone-${last.ok ? 'ok' : 'bad'}`} title=${`${last.ok ? `${last.latencyMs} ms` : last.error}${last.at ? ` · tested ${timeAgo(last.at)}` : ''}`} data-testid="connector-last-test"><span class="dot"></span>${last.ok ? 'OK' : 'Failed'}</span>`}
    </div>
    <div class="connector-host mono" title="Host">${connectionHost(c.type, c.config) || html`<span class="subtle">—</span>`}</div>
    ${(c.tags || []).length > 0 && html`<div class="tags">${c.tags.map((t) => html`<${Tag}>${t}</${Tag}>`)}</div>`}
    <div class="connector-used">${used.length ? html`<span class="subtle">Used by</span> ${used.slice(0, 3).map((t) => html`<a class="tag tag-primary" href=${`#/tasks/${encodeURIComponent(t.id)}`}>${t.name || t.id}</a>`)}${used.length > 3 && html`<span class="subtle">+${used.length - 3}</span>`}`
      : html`<span class="subtle">Not used by any task</span>`}</div>
    <div class="connector-actions">
      ${can('connector.test') && html`<${Button} size="sm" icon="plug" loading=${busy} onClick=${test} aria-label=${`Test ${c.name}`}>Test</${Button}>`}
      ${can('connector.write') && html`<${Button} size="sm" icon="edit" onClick=${() => onEdit(c)} aria-label=${`Edit ${c.name}`}>Edit</${Button}>
        <${Button} size="sm" kind="danger-ghost" icon="trash" onClick=${async () => { if (await deleteConnectorFlow(c)) onChanged(); }} aria-label=${`Delete ${c.name}`} />`}
      ${can('task.create') && html`<a class="btn btn-ghost btn-sm" href=${`#/connectors/${encodeURIComponent(c.id)}`}><span>Tables</span><${Icon} name="arrow-right" size=${14} /></a>`}
    </div>
  </article>`;
}

export function ConnectorsPage() {
  const { data, error, reload } = useFetch(() => api.connectors(), []);
  const [q, setQ] = useState('');
  const [type, setType] = useState('');
  const [editing, setEditing] = useState(null);
  const list = useMemo(() => (data || []).filter((c) => (!type || c.type === type)
    && (!q || `${c.name} ${c.description} ${(c.tags || []).join(' ')} ${connectionHost(c.type, c.config)}`.toLowerCase().includes(q.toLowerCase()))), [data, q, type]);
  const types = useMemo(() => [...new Set((data || []).map((c) => c.type))], [data]);
  return html`
    <${PageHeader} title="Connectors" doc="connectors" subtitle="Connection settings and credentials stored once and reused by many tasks. Rotating a password restarts the tasks that use it."
      actions=${can('connector.write') && html`<${Button} kind="primary" icon="plus" class="keep-label" onClick=${() => setEditing('new')} data-testid="new-connector">New connector</${Button}>`} />
    <div class="toolbar" role="search">
      <div class="input-icon"><${Icon} name="search" size=${16} /><input class="input" type="search" placeholder="Search name, host, tag…" aria-label="Search connectors" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} /></div>
      <select class="select" aria-label="Filter by type" value=${type} onChange=${(e) => setType(e.currentTarget.value)} data-testid="connector-type-filter">
        <option value="">All types</option>${SOURCES.filter((s) => types.includes(s.type)).map((s) => html`<option value=${s.type}>${s.label}</option>`)}</select>
    </div>
    <${ErrorBox} error=${error && !data && error} onRetry=${() => reload()} />
    ${!data ? html`<div class="grid grid-auto">${[0, 1, 2].map((i) => html`<div class="card skeleton-card" key=${i}><${Skeleton} w="55%" h=${18} /><${Skeleton} w="70%" /><${Skeleton} w="40%" /></div>`)}</div>`
      : !list.length ? html`<div class="card"><${Empty} icon="plug" title=${data.length ? 'No matching connector' : 'No connectors yet'}
          action=${!data.length && can('connector.write') && html`<${Button} kind="primary" icon="plus" onClick=${() => setEditing('new')}>New connector</${Button}>`}>
          ${data.length ? 'Try another search or type.' : 'Save a database connection once, then create tasks for any of its tables.'}</${Empty}></div>`
      : html`<div class="grid grid-auto stagger" data-testid="connector-list">${list.map((c, i) => html`<${ConnectorCard} key=${c.id} c=${c} i=${i} onEdit=${setEditing} onChanged=${() => reload(true)} />`)}</div>`}
    <${ConnectorEditor} open=${!!editing} connector=${editing && editing !== 'new' ? editing : null} onClose=${() => setEditing(null)} onSaved=${() => reload(true)} />
  `;
}

// ---------------------------------------------------------------- detail
function TablesBrowser({ id }) {
  const [q, setQ] = useState('');
  const [query, setQuery] = useState('');
  const [deb] = useState(() => debounce(setQuery, 220));
  const { data, error, loading } = useFetch(() => api.connectorTables(id, query), [id, query]);
  return html`<section class="card" aria-labelledby="tables-title">
    <div class="card-head"><h2 id="tables-title"><${Icon} name="database" />Tables</h2>
      <div class="input-icon" style="max-width:280px;flex:1"><${Icon} name="search" size=${15} /><input class="input input-sm" type="search" placeholder="Filter tables" aria-label="Filter tables"
        value=${q} onInput=${(e) => { setQ(e.currentTarget.value); deb(e.currentTarget.value); }} data-testid="tables-search" /></div></div>
    <${ErrorBox} error=${error} />
    ${!data ? html`<${SkeletonRows} rows=${5} cols=${4} />`
      : !data.length ? html`<${Empty} icon="search" title="No tables">${query ? `Nothing matches “${query}”.` : 'Discovery returned no tables.'}</${Empty}>`
      : html`<div class="table-wrap" style=${loading ? 'opacity:.6' : ''}><table class="table table-compact rtable" data-testid="tables-table">
        <thead><tr><th>Name</th><th>Schema</th><th>Kind</th><th class="num-col">Rows</th><th class="col-actions"><span class="sr-only">Actions</span></th></tr></thead>
        <tbody>${data.map((t, i) => html`<tr key=${`${t.schema}.${t.name}`} style=${`--i:${Math.min(i, 12)}`} data-table=${t.name}>
          <td data-label="Name" class="mono"><b>${t.name}</b></td><td data-label="Schema" class="mono subtle">${t.schema || '—'}</td>
          <td data-label="Kind"><${Tag}>${t.kind}</${Tag}></td><td data-label="Rows" class="num-col">${t.rows >= 0 ? fmtInt(t.rows) : html`<span class="subtle">unknown</span>`}</td>
          <td class="col-actions">${can('task.create') && html`<a class="btn btn-secondary btn-sm" href=${`#/tasks/new?connector=${encodeURIComponent(id)}&table=${encodeURIComponent(t.name)}${t.schema ? `&schema=${encodeURIComponent(t.schema)}` : ''}`}
            aria-label=${`Create task for ${t.name}`}><${Icon} name="plus" size=${14} /><span>Create task</span></a>`}</td>
        </tr>`)}</tbody></table></div>`}
  </section>`;
}

export function ConnectorDetailPage({ id }) {
  const { data: c, error, reload } = useFetch(() => api.connector(id), [id]);
  const [test, setTest] = useState(null);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState(false);
  if (error && !c) return html`<a class="back-link" href="#/connectors"><${Icon} name="arrow-left" size=${15} />Connectors</a><div style="margin-top:16px"><${ErrorBox} error=${error} onRetry=${() => reload()} /></div>`;
  if (!c) return html`<div class="stack"><${Skeleton} w=${90} h=${12} /><${Skeleton} w=${300} h=${28} /><div class="card"><${SkeletonRows} rows=${4} cols=${3} /></div></div>`;
  const s = sourceInfo(c.type);
  const runTest = async () => {
    setBusy(true);
    try { setTest(await api.testConnector(c.id)); } catch (e) { setTest({ ok: false, error: e.message }); }
    setBusy(false);
  };
  const used = c.usedBy || [];
  const cfgRows = (CONN_FIELDS[c.type] || []).filter((k) => c.config[k] != null && c.config[k] !== '' && !(Array.isArray(c.config[k]) && !c.config[k].length));
  return html`
    <a class="back-link" href="#/connectors"><${Icon} name="arrow-left" size=${15} />Connectors</a>
    <header class="detail-head" style="margin-top:10px">
      <div class="detail-title"><${SourceIcon} type=${c.type} size="lg" />
        <div style="min-width:0"><div class="title-row"><h1>${c.name}</h1><${HelpLink} source=${c.type} label=${`${s.label} documentation`} /></div>
          <div class="detail-meta"><span>${s.label}</span><span class="mono">${connectionHost(c.type, c.config)}</span>${(c.tags || []).map((t) => html`<${Tag}>${t}</${Tag}>`)}<span>updated ${timeAgo(c.updatedAt)}</span></div>
          ${c.description && html`<p class="muted" style="margin-top:6px">${c.description}</p>`}</div></div>
      <div class="btn-group">
        ${can('connector.test') && html`<${Button} size="sm" kind="primary" icon="plug" loading=${busy} onClick=${runTest} data-testid="connector-detail-test">Test connection</${Button}>`}
        ${can('connector.write') && html`<${Button} size="sm" icon="edit" onClick=${() => setEditing(true)}>Edit</${Button}>
          <${Button} size="sm" kind="danger-ghost" icon="trash" onClick=${async () => { if (await deleteConnectorFlow(c)) navigate('#/connectors'); }} data-testid="connector-delete">Delete</${Button}>`}
      </div>
    </header>
    ${test && html`<div style="margin-bottom:16px" aria-live="polite"><${ConnectorTestResult} r=${test} /></div>`}
    <div class="split" style="margin-bottom:16px">
      <section class="card"><div class="card-head"><h2><${Icon} name="settings" />Configuration</h2><span class="subtle">secrets are masked</span></div>
        <dl class="kv card-body">${cfgRows.length ? cfgRows.map((k) => html`<dt>${k}</dt><dd class="mono">${Array.isArray(c.config[k]) ? c.config[k].join(', ') : c.config[k]}</dd>`) : html`<dt>—</dt><dd class="subtle">No connection settings</dd>`}</dl></section>
      <section class="card"><div class="card-head"><h2><${Icon} name="tasks" />Used by</h2><span class="count-pill">${used.length}</span></div>
        ${used.length ? html`<ul class="used-list" data-testid="connector-used-by">${used.map((t) => html`<li><a href=${`#/tasks/${encodeURIComponent(t.id)}`}>${t.name || t.id}</a><${StateBadge} state=${t.state} /></li>`)}</ul>`
          : html`<p class="card-body subtle">No task uses this connector yet.</p>`}</section>
    </div>
    <${TablesBrowser} id=${c.id} />
    <${ConnectorEditor} open=${editing} connector=${c} onClose=${() => setEditing(false)} onSaved=${() => reload(true)} />
  `;
}

