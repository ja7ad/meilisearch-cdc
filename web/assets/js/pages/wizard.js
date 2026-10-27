// New / edit task wizard: source -> connection -> mapping -> index -> sync & safety -> review.
import { html, useEffect, useMemo, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { can } from '../auth.js';
import { navigate } from '../router.js';
import { useFetch } from '../hooks.js';
import { SourceIcon } from '../components.js';
import { Icon } from '../icons.js';
import { Alert, Button, CopyField, Empty, ErrorBox, Field, Forbidden, JsonView, PageHeader, Skeleton, SkeletonRows, Tag, Toggle, toast } from '../ui.js';
import { SOURCES, cls, fmtInt, isDuration, pretty, sourceInfo } from '../util.js';
import { TABLE_FIELDS, connectionHost } from '../connection.js';
import { SecretInput, secretError, signedCurl } from '../signing.js';
import { ConnectorEditor } from './connectors.js';
import { hostOf, singleDestination, useDestinations } from '../destinations.js';
import { HealthDot } from './destinations.js';
import { SettingsEditor } from '../settings-editor.js';

const STEPS = [
  { id: 'source', label: 'Connector', hint: 'Where the data lives' },
  { id: 'connection', label: 'Source', hint: 'Table and options' },
  { id: 'mapping', label: 'Mapping', hint: 'Rows to documents' },
  { id: 'index', label: 'Index', hint: 'Meilisearch target' },
  { id: 'sync', label: 'Sync & safety', hint: 'Batching and guards' },
  { id: 'review', label: 'Review', hint: 'Check and create' },
];

const ID_RE = /^[a-zA-Z0-9_-]{1,64}$/;
const UID_RE = /^[a-zA-Z0-9_-]{1,400}$/;

function blankTask() {
  return {
    id: '', name: '', description: '', tags: [],
    saveAsConnector: false, connectorName: '',
    source: { connectorId: '', type: '', dsn: '', database: '', schema: '', table: '', slot: '', publication: '', serverId: '', region: '', endpoint: '', accessKeyId: '', secretAccessKey: '', brokers: [], groupId: '', driver: 'pgx', cursorColumn: '', pollInterval: '2s', webhookSecret: '' },
    index: { uid: '', writeMode: 'replace', manageSettings: false, destinationId: '' },
    mapping: { primaryKey: [], primaryKeyField: 'id', keepKeyColumns: false, include: [], exclude: [], rename: {}, softDeleteColumn: '', softDeleteValue: '', geo: { lat: '', lng: '' }, timeFormat: 'rfc3339' },
    sync: {
      mode: 'snapshot_stream', batchSize: 1000, flushInterval: '1s', maxInflight: 4,
      retry: { maxAttempts: 8, initialBackoff: '200ms', maxBackoff: '30s' },
      safety: { maxDeleteRatio: 0.2, minDocsForDeleteGuard: 1000, allowTruncate: false, zeroDowntimeResync: true, dumpBeforeResync: false },
    },
  };
}

/** TaskView from the API -> editable wizard state */
function fromTask(v) {
  const b = blankTask();
  const t = JSON.parse(JSON.stringify(v));
  delete t.status;
  return {
    ...b, ...t,
    tags: t.tags || [],
    source: { ...b.source, ...t.source, connectorId: t.source.connectorId || '', serverId: t.source.serverId || '', brokers: t.source.brokers || [] },
    index: { ...b.index, ...t.index },
    mapping: { ...b.mapping, ...t.mapping, geo: t.mapping.geo || { lat: '', lng: '' }, rename: t.mapping.rename || {}, include: t.mapping.include || [], exclude: t.mapping.exclude || [], primaryKey: t.mapping.primaryKey || [] },
    sync: { ...b.sync, ...t.sync, retry: { ...b.sync.retry, ...(t.sync && t.sync.retry) }, safety: { ...b.sync.safety, ...(t.sync && t.sync.safety) } },
  };
}


/** Wizard state -> model.Task JSON (omits empty optional fields). */
export function buildTask(s, settingsText, staticRows) {
  // With a connector the task only carries table level fields (docs/api.md, Connectors).
  // Tasks always reference a connector and only carry table level fields: credentials live in Connectors.
  const src = { connectorId: s.source.connectorId };
  const fieldsOf = TABLE_FIELDS[s.source.type] || ['table'];
  for (const f of fieldsOf) {
    const v = s.source[f];
    if (f === 'brokers') { if (v && v.length) src.brokers = v; continue; }
    if (f === 'serverId') { if (v !== '' && v != null) src.serverId = Number(v); continue; }
    if (v !== '' && v != null) src[f] = typeof v === 'string' ? v.trim() : v;
  }
  if (!src.table) src.table = '';
  const m = s.mapping;
  const mapping = { primaryKeyField: m.primaryKeyField || 'id', keepKeyColumns: !!m.keepKeyColumns, timeFormat: m.timeFormat || 'rfc3339' };
  if (m.primaryKey.length) mapping.primaryKey = m.primaryKey;
  if (m.include.length) mapping.include = m.include;
  if (m.exclude.length) mapping.exclude = m.exclude;
  const rename = Object.fromEntries(Object.entries(m.rename || {}).filter(([k, v]) => v && v.trim() && v.trim() !== k).map(([k, v]) => [k, v.trim()]));
  if (Object.keys(rename).length) mapping.rename = rename;
  if (m.softDeleteColumn) { mapping.softDeleteColumn = m.softDeleteColumn; if (m.softDeleteValue) mapping.softDeleteValue = m.softDeleteValue; }
  if (m.geo && (m.geo.lat || m.geo.lng)) mapping.geo = { lat: m.geo.lat, lng: m.geo.lng };
  const st = {};
  for (const [k, v] of staticRows) {
    if (!k.trim()) continue;
    try { st[k.trim()] = JSON.parse(v); } catch { st[k.trim()] = v; }
  }
  if (Object.keys(st).length) mapping.static = st;
  const index = { uid: s.index.uid.trim(), manageSettings: !!s.index.manageSettings, writeMode: s.index.writeMode };
  if (s.index.destinationId) index.destinationId = s.index.destinationId;
  if (settingsText.trim()) { try { index.settings = JSON.parse(settingsText); } catch { /* reported by validation */ } }
  const out = {
    name: s.name.trim(), source: src, index, mapping,
    sync: {
      mode: s.sync.mode, batchSize: Number(s.sync.batchSize) || 0, flushInterval: s.sync.flushInterval, maxInflight: Number(s.sync.maxInflight) || 0,
      retry: { maxAttempts: Number(s.sync.retry.maxAttempts) || 0, initialBackoff: s.sync.retry.initialBackoff, maxBackoff: s.sync.retry.maxBackoff },
      safety: { ...s.sync.safety, maxDeleteRatio: Number(s.sync.safety.maxDeleteRatio) || 0, minDocsForDeleteGuard: Number(s.sync.safety.minDocsForDeleteGuard) || 0 },
    },
  };
  if (s.id) out.id = s.id.trim();
  if (s.description) out.description = s.description.trim();
  if (s.tags.length) out.tags = s.tags;
  if (s.desiredState) out.desiredState = s.desiredState;
  return out;
}

function validate(s, settingsText, staticRows) {
  const e = {};
  if (!s.source.connectorId) e['source.type'] = 'Choose or create a connector';
  const src = s.source;
  if (!s.name.trim()) e.name = 'Name is required';
  if (src.type === 'webhook' && secretError(src.webhookSecret)) e['source.webhookSecret'] = secretError(src.webhookSecret);
  if (s.id && !ID_RE.test(s.id)) e.id = 'Use 1-64 letters, digits, - or _';
  if (!src.table.trim()) e['source.table'] = ({ kafka: 'Topic is required', mongodb: 'Collection is required', couchdb: 'Database is required', redis: 'Stream key is required', webhook: 'A name for the feed is required' })[src.type] || 'Table is required';
  const conn = !!src.connectorId;
  if (!conn && ['postgres', 'mysql', 'mariadb', 'mongodb', 'sqlpoll', 'couchdb', 'sqlserver', 'cockroachdb', 'redis'].includes(src.type) && !src.dsn.trim()) e['source.dsn'] = 'Connection string is required';
  if (!conn && src.type === 'mongodb' && !src.database.trim()) e['source.database'] = 'Database is required for MongoDB';
  if (!conn && src.type === 'dynamodb' && !src.region.trim()) e['source.region'] = 'Region is required for DynamoDB';
  if (!conn && src.type === 'kafka' && !src.brokers.length) e['source.brokers'] = 'At least one broker is required';
  if (!conn && src.type === 'sqlpoll' && !src.driver) e['source.driver'] = 'Driver is required';

  if (src.type === 'sqlpoll' && !src.cursorColumn.trim()) e['source.cursorColumn'] = 'Cursor column is required';
  if (src.type === 'sqlpoll' && !isDuration(src.pollInterval)) e['source.pollInterval'] = 'Use a duration such as 2s or 500ms';
  if (['mysql', 'mariadb'].includes(src.type) && src.serverId !== '' && !(Number(src.serverId) > 0 && Number(src.serverId) < 2 ** 32)) e['source.serverId'] = 'Must be a positive 32-bit integer';

  const m = s.mapping;
  if (!UID_RE.test(m.primaryKeyField || 'id')) e['mapping.primaryKeyField'] = 'Use letters, digits, - or _';
  if (m.geo && (!!m.geo.lat !== !!m.geo.lng)) e['mapping.geo'] = 'Pick both latitude and longitude columns, or neither';
  if (m.softDeleteValue && !m.softDeleteColumn) e['mapping.softDeleteColumn'] = 'Pick the column that carries the value';
  const keys = staticRows.map(([k]) => k.trim()).filter(Boolean);
  if (new Set(keys).size !== keys.length) e['mapping.static'] = 'Static field names must be unique';

  if (!UID_RE.test(s.index.uid.trim())) e['index.uid'] = 'Index uid is required: letters, digits, - or _';
  if (settingsText.trim()) {
    try { const v = JSON.parse(settingsText); if (!v || typeof v !== 'object' || Array.isArray(v)) e['index.settings'] = 'Settings must be a JSON object'; }
    catch (err) { e['index.settings'] = `Invalid JSON: ${err.message}`; }
  } else if (s.index.manageSettings) e['index.settings'] = 'Add the settings to manage, or turn off “Manage settings”';

  const y = s.sync;
  if (!(Number.isInteger(Number(y.batchSize)) && Number(y.batchSize) > 0)) e['sync.batchSize'] = 'Must be a positive integer';
  if (!(Number.isInteger(Number(y.maxInflight)) && Number(y.maxInflight) > 0)) e['sync.maxInflight'] = 'Must be a positive integer';
  if (!y.flushInterval || !isDuration(y.flushInterval)) e['sync.flushInterval'] = 'Use a duration such as 1s or 500ms';
  if (!(Number(y.retry.maxAttempts) > 0)) e['sync.retry.maxAttempts'] = 'Must be at least 1';
  if (!isDuration(y.retry.initialBackoff)) e['sync.retry.initialBackoff'] = 'Invalid duration';
  if (!isDuration(y.retry.maxBackoff)) e['sync.retry.maxBackoff'] = 'Invalid duration';
  const r = Number(y.safety.maxDeleteRatio);
  if (Number.isNaN(r) || r < 0 || r > 1) e['sync.safety.maxDeleteRatio'] = 'Between 0 and 1';
  return e;
}

const STEP_FIELDS = {
  source: ['source.type'],
  connection: ['name', 'id', 'source.'],
  mapping: ['mapping.'],
  index: ['index.'],
  sync: ['sync.'],
};
const stepErrors = (step, errs) => Object.keys(errs).filter((k) => (STEP_FIELDS[step] || []).some((p) => (p.endsWith('.') ? k.startsWith(p) : k === p)) && !(step === 'connection' && k === 'source.type'));

// ---------------------------------------------------------------- small inputs
function TextInput({ id, value, onInput, error, ...rest }) {
  return html`<input id=${id} class=${cls('input', rest.mono && 'mono')} value=${value ?? ''} aria-invalid=${error ? 'true' : undefined}
    aria-describedby=${error ? `${id}-err` : undefined} onInput=${(e) => onInput(e.currentTarget.value)} ...${rest} />`;
}
function F({ id, label, hint, error, required, children, class: c }) {
  return html`<${Field} label=${label} hint=${hint} error=${error} required=${required} htmlFor=${id} class=${c}>${children}</${Field}>`;
}

// ---------------------------------------------------------------- steps
function StepSource({ s, set, errors, connectors, onNewConnector, legacy }) {
  const [q, setQ] = useState('');
  const list = (connectors || []).filter((c) => !q || `${c.name} ${c.type} ${(c.tags || []).join(' ')} ${connectionHost(c.type, c.config)}`.toLowerCase().includes(q.toLowerCase()));
  return html`<div class="stack">
    <div class="row-between"><div><h2>Choose a connector</h2><p class="muted" style="margin-top:4px">Tasks read through a saved connector, so credentials are stored once and never repeated in tasks.</p></div>
      <div class="btn-group"><a class="btn btn-ghost btn-sm" href="#/connectors" target="_blank" rel="noopener"><span>Manage connectors</span><${Icon} name="external" size=${14} /></a>
        ${can('connector.write') && html`<${Button} kind="primary" size="sm" icon="plus" onClick=${onNewConnector} data-testid="wizard-new-connector">New connector</${Button}>`}</div></div>
    ${legacy && html`<${Alert} tone="warn" title="Inline connection">This task was created with an inline connection. Pick or create a connector to keep editing it; the connection settings are not shown here.</${Alert}>`}
    ${!connectors ? html`<div class="source-grid">${[0, 1, 2].map((k) => html`<div class="card skeleton-card" key=${k}><${Skeleton} w="50%" h=${16} /><${Skeleton} w="70%" /></div>`)}</div>`
      : !connectors.length ? html`<div class="card"><${Empty} icon="plug" title="No connector yet"
          action=${can('connector.write') && html`<${Button} kind="primary" icon="plus" onClick=${onNewConnector}>New connector</${Button}>`}>Create a connector with the database connection, then pick the table to sync.</${Empty}></div>`
      : html`${connectors.length > 4 && html`<div class="input-icon"><${Icon} name="search" size=${16} /><input class="input" type="search" placeholder="Search connectors" aria-label="Search connectors" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} /></div>`}
        <div class="source-grid" role="group" aria-label="Connectors">${list.map((c, i) => html`<button type="button" key=${c.id} class="source-card connector-pick" style=${`--i:${i}`}
            aria-pressed=${s.source.connectorId === c.id} data-connector=${c.id}
            onClick=${() => set((d) => { if (d.source.connectorId !== c.id) d.source.table = ''; d.source.connectorId = c.id; d.source.type = c.type; if (c.type === 'postgres' && !d.source.schema) d.source.schema = 'public'; })}>
          <${SourceIcon} type=${c.type} size="lg" /><b>${c.name}</b><p class="mono">${connectionHost(c.type, c.config) || sourceInfo(c.type).label}</p>
          <span class="subtle" style="font-size:12px">${sourceInfo(c.type).label} · used by ${(c.usedBy || []).length} task${(c.usedBy || []).length === 1 ? '' : 's'}</span>
        </button>`)}</div>
        ${!list.length && html`<p class="subtle">No connector matches “${q}”.</p>`}`}
    ${errors['source.type'] && html`<p class="field-error" role="alert">${errors['source.type']}</p>`}
  </div>`;
}

function TestResult({ result, onClose }) {
  if (!result) return null;
  if (!result.ok) return html`<div class="test-result"><${Alert} tone="bad" title="Connection failed" action=${html`<${Button} size="sm" kind="ghost" icon="x" aria-label="Dismiss" onClick=${onClose} />`}>${result.error}</${Alert}></div>`;
  const sc = result.schema || { columns: [] };
  return html`<div class="test-result stack" data-testid="test-result">
    <${Alert} tone="ok" title="Connected">Read ${sc.columns.length} columns${sc.rowCount >= 0 ? html` · about ${fmtInt(sc.rowCount)} rows` : ''}${sc.inferred ? ' · schema inferred by sampling' : ''}. Document primary key: <code>${result.primaryKey}</code></${Alert}>
    <div class="grid grid-2">
      <div class="card"><div class="card-head"><h3><${Icon} name="database" />Columns</h3>${sc.inferred && html`<${Tag} tone="violet">inferred</${Tag}>`}</div>
        <div class="table-wrap" style="max-height:320px;overflow:auto"><table class="table table-compact" data-testid="schema-table">
          <thead><tr><th>Name</th><th>Type</th><th>Null</th></tr></thead>
          <tbody>${sc.columns.map((c, i) => html`<tr key=${c.name} style=${`--i:${Math.min(i, 10)}`}><td class="mono">${c.name} ${c.primaryKey && html`<${Tag} tone="primary">PK</${Tag}>`}</td><td class="subtle mono">${c.type}</td><td>${c.nullable ? 'yes' : ''}</td></tr>`)}</tbody>
        </table></div></div>
      <div class="card"><div class="card-head"><h3><${Icon} name="doc" />Sample documents</h3><span class="subtle">${(result.sample || []).length} transformed</span></div>
        <div class="card-body"><${JsonView} value=${result.sample || []} maxHeight=${262} label="Sample documents" /></div></div>
    </div>
  </div>`;
}

function TablePicker({ connectorId, value, onPick }) {
  const [tables, setTables] = useState(null);
  useEffect(() => { let on = true; api.connectorTables(connectorId).then((l) => on && setTables(l)).catch(() => on && setTables([])); return () => { on = false; }; }, [connectorId]);
  if (!tables) return html`<p class="subtle" style="font-size:12.5px"><span class="spinner" aria-hidden="true"></span> Discovering tables…</p>`;
  if (!tables.length) return null;
  const q = (value || '').toLowerCase();
  const matches = tables.filter((t) => !q || t.name.toLowerCase().includes(q)).slice(0, 14);
  return html`<div class="table-picker"><span class="subtle" style="font-size:12.5px">${matches.length ? 'Tables in this connector' : 'No discovered table matches; the name is used as typed.'}</span>
    <div class="chips" role="group" aria-label="Tables in this connector">${matches.map((t) => html`<button type="button" class="chip" aria-pressed=${t.name === value} data-table=${t.name}
      onClick=${() => onPick(t)}>${t.schema && !['public', ''].includes(t.schema) ? html`<span class="subtle">${t.schema}.</span>` : ''}${t.name}${t.rows >= 0 ? html`<span class="subtle">${fmtInt(t.rows)}</span>` : ''}</button>`)}</div></div>`;
}

const TABLE_HINTS = {
  kafka: 'The Debezium topic that carries this table’s change events, usually <server>.<schema>.<table>.',
  mongodb: 'The collection to watch with a change stream.',
  dynamodb: 'The DynamoDB table; its stream must be enabled with NEW_AND_OLD_IMAGES.',
  couchdb: 'The CouchDB database whose _changes feed is followed.',
  redis: 'The stream key the producers append change entries to.',
  webhook: 'A name for this HTTP feed; it appears in the ingest URL and logs.',
  sqlpoll: 'The table re-read on every poll.',
};
const CURSOR_NAMES = ['updated_at', 'modified_at', 'last_modified', 'changed_at', 'version', 'row_version', 'rowversion'];
const POLL_INTERVALS = [['500ms', '500 ms'], ['1s', '1 second'], ['2s', '2 seconds (default)'], ['5s', '5 seconds'], ['10s', '10 seconds'], ['30s', '30 seconds'], ['1m', '1 minute']];
/** Columns ranked by how likely they track changes: known names, then timestamps, then integers. */
export function rankCursorColumns(columns = []) {
  const score = (c) => {
    const n = c.name.toLowerCase(); const t = String(c.type || '').toLowerCase();
    let x = 0;
    const i = CURSOR_NAMES.indexOf(n);
    if (i >= 0) x += 100 - i;
    else if (/(updated|modified|changed)/.test(n)) x += 80;
    if (/timestamp|datetime|date|time/.test(t)) x += 50;
    else if (/int|serial|number|numeric|decimal|^n$/.test(t)) x += 30;
    if (c.primaryKey) x -= 45;
    return x;
  };
  return columns.map((c) => ({ ...c, score: score(c) })).filter((c) => c.score > -20 && /time|date|int|serial|number|numeric|decimal|^n$/i.test(String(c.type || '')) || c.score > 0)
    .sort((a, b) => b.score - a.score);
}

function CursorField({ value, onChange, schema, loading, error, gotoSoftDelete }) {
  const ranked = rankCursorColumns((schema && schema.columns) || []);
  const best = ranked.find((c) => c.score > 0);
  const [manual, setManual] = useState(false);
  useEffect(() => { if (!value && best && !manual) onChange(best.name); }, [best && best.name]);
  const col = ((schema && schema.columns) || []).find((c) => c.name === value);
  const looksLikeId = col && (col.primaryKey || /^(id|.*_id)$/i.test(col.name)) && !/time|date/i.test(col.type || '');
  const known = ranked.some((c) => c.name === value);
  return html`<div class="span-2 stack-sm cursor-field">
    <div class="row-between"><label class="field-label" for="f-cursor">Change tracking column <span class="subtle" style="font-weight:500">(cursor)</span><span class="req" aria-hidden="true"> *</span></label>
      <details class="info-pop"><summary aria-label="Cursor column example"><${Icon} name="info" size=${15} /></summary>
        <div class="info-pop-body"><b>Example</b><p>With <code>updated_at</code>, each poll runs <code>WHERE updated_at &gt; '2025-06-01 10:00:03' ORDER BY updated_at</code>.</p>
          <p>A trigger or your ORM must bump <code>updated_at</code> on every write.</p></div></details></div>
    ${!manual && ranked.length ? html`<div class="row" style="flex-wrap:nowrap">
        <select id="f-cursor" class=${cls('select', error && 'is-invalid')} value=${value} onChange=${(e) => { if (e.currentTarget.value === '__manual') { setManual(true); return; } onChange(e.currentTarget.value); }}>
          ${!value && html`<option value="">Choose a column…</option>`}
          ${ranked.map((c) => html`<option value=${c.name}>${c.name} · ${c.type}${best && c.name === best.name ? ' — suggested' : ''}</option>`)}
          ${value && !known && html`<option value=${value}>${value}</option>`}
          <option value="__manual">Other… (type a name)</option>
        </select>
        ${best && value === best.name && html`<span class="tag tag-ok" data-testid="cursor-suggested">Suggested</span>`}</div>`
      : html`<div class="row" style="flex-wrap:nowrap"><input id="f-cursor" class=${cls('input mono', error && 'is-invalid')} value=${value} placeholder="updated_at" autocapitalize="off" spellcheck="false" onInput=${(e) => onChange(e.currentTarget.value)} />
          ${loading ? html`<span class="subtle nowrap"><span class="spinner" aria-hidden="true"></span> reading columns…</span>` : ranked.length > 0 && html`<${Button} size="sm" kind="ghost" onClick=${() => setManual(false)}>Pick from list</${Button}>`}</div>`}
    ${error ? html`<p class="field-error" role="alert">${error}</p>` : null}
    <p class="field-hint">SQL polling has no change log, so it re-reads rows whose value in this column is greater than the last one it saw. Pick a column that increases every time a row is inserted or updated, e.g. an <code>updated_at</code> timestamp kept current by a trigger/ORM, or a version counter. An auto-increment id only catches new rows, not updates.</p>
    ${looksLikeId && html`<p class="field-warn" data-testid="cursor-id-warning"><${Icon} name="alert" size=${14} />Only new rows will sync; updates to existing rows won't be detected.</p>`}
    <p class="field-note-muted"><${Icon} name="info" size=${13} />Deleted rows can't be seen by polling. Use a soft-delete column (<button type="button" class="link-btn inline" onClick=${gotoSoftDelete} data-testid="goto-soft-delete">Mapping step → Soft delete</button>) to remove them from the index.</p>
  </div>`;
}

function StepConnection({ s, set, errors, test, testing, runTest, setTest, connectors, goto, autoSchema }) {
  const t = s.source.type;
  const src = s.source;
  const connector = (connectors || []).find((c) => c.id === src.connectorId);
  const has = (f) => (TABLE_FIELDS[t] || ['table']).includes(f);
  const up = (k) => (v) => set((d) => { d.source[k] = v; });
  const [tagText, setTagText] = useState(s.tags.join(', '));
  const tableLabel = { kafka: 'Topic', mongodb: 'Collection', dynamodb: 'DynamoDB table', couchdb: 'Database', redis: 'Stream key', webhook: 'Feed name' }[t] || 'Table';
  return html`<div class="stack">
    <div class="connector-banner" data-testid="wizard-connector">
      <${SourceIcon} type=${t} />
      <div class="grow" style="min-width:0"><span class="subtle" style="font-size:12px">Connector · ${sourceInfo(t).label}</span>
        <b>${connector ? connector.name : src.connectorId}</b><span class="mono subtle connector-banner-host">${connector ? connectionHost(t, connector.config) : ''}</span></div>
      <a class="btn btn-ghost btn-sm" href=${`#/connectors/${encodeURIComponent(src.connectorId)}`} target="_blank" rel="noopener" aria-label="Open connector">Open<${Icon} name="external" size=${14} /></a>
      <${Button} size="sm" icon="swap" onClick=${() => goto(0)} data-testid="change-connector">Change</${Button}>
    </div>
    <div class="form-section">
      <h3><${Icon} name="tag" />Basics</h3>
      <div class="form-grid">
        <${F} id="f-name" label="Name" required error=${errors.name}><${TextInput} id="f-name" value=${s.name} error=${errors.name} placeholder="Products catalog" onInput=${(v) => set((d) => { d.name = v; })} /></${F}>
        <${F} id="f-id" label="Task id" hint=${s.isEdit ? 'The id cannot be changed.' : 'Optional: generated from the name when empty.'} error=${errors.id}>
          <${TextInput} id="f-id" mono value=${s.id} error=${errors.id} disabled=${s.isEdit} placeholder="products-pg" autocapitalize="off" onInput=${(v) => set((d) => { d.id = v; })} /></${F}>
        <${F} id="f-desc" label="Description" class="span-2"><${TextInput} id="f-desc" value=${s.description} placeholder="What this pipeline is for" onInput=${(v) => set((d) => { d.description = v; })} /></${F}>
        <${F} id="f-tags" label="Tags" hint="Comma separated, used for filtering." class="span-2">
          <${TextInput} id="f-tags" value=${tagText} placeholder="shop, prod" autocapitalize="off" onInput=${(v) => { setTagText(v); set((d) => { d.tags = v.split(',').map((x) => x.trim()).filter(Boolean); }); }} /></${F}>
      </div>
    </div>
    <div class="form-section">
      <h3><${Icon} name="database" />What to sync</h3>
      <div class="form-grid">
        <${F} id="f-table" label=${tableLabel} required error=${errors['source.table']} class="span-2" hint=${TABLE_HINTS[t] || 'The table whose rows become documents.'}>
          <${TextInput} id="f-table" mono value=${src.table} error=${errors['source.table']} placeholder=${t === 'kafka' ? 'dbserver1.inventory.products' : 'products'} onInput=${up('table')} autocapitalize="off" spellcheck="false" />
          ${t !== 'webhook' && html`<${TablePicker} connectorId=${src.connectorId} value=${src.table} onPick=${(tb) => set((d) => { d.source.table = tb.name; if (tb.schema && (TABLE_FIELDS[t] || []).includes('schema')) d.source.schema = tb.schema; })} />`}
        </${F}>
        ${has('schema') && html`<${F} id="f-schema" label="Schema" hint=${t === 'sqlserver' ? 'The namespace that contains the table. Defaults to dbo.' : 'The namespace that contains the table. Defaults to public.'}><${TextInput} id="f-schema" mono value=${src.schema} placeholder=${t === 'sqlserver' ? 'dbo' : 'public'} onInput=${up('schema')} /></${F}>`}
        ${has('database') && html`<${F} id="f-db" label="Database" hint="Optional. Reads another database on the same server instead of the connector's default."><${TextInput} id="f-db" mono value=${src.database} onInput=${up('database')} /></${F}>`}
        ${has('groupId') && html`<${F} id="f-group" label="Consumer group" hint="Kafka remembers how far this group has read, so restarts resume where they stopped. Leave empty to generate one for this task; never share it with other consumers."><${TextInput} id="f-group" mono value=${src.groupId} onInput=${up('groupId')} /></${F}>`}
        ${has('slot') && html`<${F} id="f-slot" label="Replication slot" hint="Postgres keeps changes in this slot until the task has read them, so nothing is lost while it is stopped. Leave empty to create one per task."><${TextInput} id="f-slot" mono value=${src.slot} onInput=${up('slot')} /></${F}>`}
        ${has('publication') && html`<${F} id="f-pub" label="Publication" hint="Defines which tables Postgres publishes to the slot. Leave empty to create one for this table; reuse an existing one if your DBA manages them."><${TextInput} id="f-pub" mono value=${src.publication} onInput=${up('publication')} /></${F}>`}
        ${has('serverId') && html`<${F} id="f-sid" label="Replica server id" hint="The task reads the binlog like a replica and needs an id no other replica uses. Leave empty to generate one." error=${errors['source.serverId']}><${TextInput} id="f-sid" type="number" inputmode="numeric" min="1" value=${src.serverId} error=${errors['source.serverId']} onInput=${up('serverId')} /></${F}>`}
        ${has('cursorColumn') && html`<${CursorField} value=${src.cursorColumn} onChange=${up('cursorColumn')} schema=${autoSchema.schema} loading=${autoSchema.loading} error=${errors['source.cursorColumn']}
          gotoSoftDelete=${() => { goto(2); setTimeout(() => { const el = document.getElementById('f-sdc'); if (el) { el.scrollIntoView({ block: 'center' }); el.focus(); } }, 350); }} />`}
        ${has('pollInterval') && html`<${F} id="f-poll" label="Poll interval" error=${errors['source.pollInterval']} hint="How often to check for changes. Shorter means fresher results and more queries on the database.">
          <select id="f-poll" class="select" value=${({ '': '2s', '1m0s': '1m' })[src.pollInterval] ?? src.pollInterval} onChange=${(e) => up('pollInterval')(e.currentTarget.value)}>
            ${src.pollInterval && !['1m0s'].includes(src.pollInterval) && !POLL_INTERVALS.some(([v]) => v === src.pollInterval) && html`<option value=${src.pollInterval}>${src.pollInterval}</option>`}
            ${POLL_INTERVALS.map(([v, l]) => html`<option value=${v}>${l}</option>`)}</select></${F}>`}
        ${t === 'webhook' && html`<div class="span-2"><${Alert} tone="info" title="Push changes over HTTP">
          POST JSON changes such as <code>{"op":"upsert","row":{"id":1,"name":"…"}}</code> (or an array, or NDJSON) to
          <code>${location.origin}/api/v1/ingest/${s.id || '<task id>'}</code>. The request returns once the documents are confirmed in Meilisearch.
        </${Alert}></div>`}
      </div>
    </div>
    ${t === 'webhook' && html`<div class="form-section" data-testid="webhook-security">
      <h3><${Icon} name="shield" />Security</h3>
      <${F} id="f-whsec" label="Signing secret" error=${errors['source.webhookSecret']}
        hint="Recommended. Senders sign each request with this secret (HMAC-SHA256) instead of using an API token, so a leaked URL cannot write to your index. Unsigned, stale (older than 5 minutes) or tampered requests are refused with 401.">
        <${SecretInput} id="f-whsec" value=${src.webhookSecret} error=${errors['source.webhookSecret']} onChange=${up('webhookSecret')} testid="webhook-secret" /></${F}>
      ${src.webhookSecret ? html`<details class="details-block" data-testid="webhook-sign-example"><summary>How to sign a request</summary>
          <p class="muted">Send <code>X-CDC-Timestamp</code> (unix seconds) and <code>X-CDC-Signature: sha256=&lt;hex HMAC-SHA256 of timestamp + "." + raw body&gt;</code>.</p>
          <${CopyField} multiline label="Signed request example" testid="webhook-curl" value=${signedCurl(`${location.origin}/api/v1/ingest/${s.id || '<task id>'}`)} /></details>`
        : html`<p class="field-warn" data-testid="webhook-unsigned"><${Icon} name="alert" size=${14} />Without a secret, pushes need a moderator API token.</p>`}
    </div>`}
    <div class="row-between" style="border-top:1px solid var(--border);padding-top:18px">
      <p class="muted" style="max-width:520px">Test reads the table schema through the connector and transforms five sample rows with the current mapping.</p>
      <${Button} kind="secondary" icon="plug" loading=${testing} onClick=${runTest} data-testid="test-connection">Test connection</${Button}>
    </div>
    <${TestResult} result=${test} onClose=${() => setTest(null)} />
  </div>`;
}

function ColumnChips({ columns, selected, onToggle, pkCols = [], label }) {
  return html`<div class="chips" role="group" aria-label=${label}>
    ${columns.map((c) => html`<button type="button" key=${c} class="chip" aria-pressed=${selected.includes(c)} onClick=${() => onToggle(c)} data-chip=${c}>
      ${selected.includes(c) ? html`<${Icon} name="check" size=${14} />` : pkCols.includes(c) ? html`<${Icon} name="key" size=${14} />` : null}${c}</button>`)}
  </div>`;
}

const listText = (arr) => (arr || []).join(', ');
const parseList = (v) => v.split(',').map((x) => x.trim()).filter(Boolean);

function StepMapping({ s, set, errors, schema, testing, runTest, staticRows, setStaticRows }) {
  const m = s.mapping;
  const cols = schema ? schema.columns.map((c) => c.name) : [];
  const pkCols = schema ? schema.primaryKey || [] : [];
  const types = schema ? Object.fromEntries(schema.columns.map((c) => [c.name, c.type])) : {};
  const toggleIn = (key) => (c) => set((d) => { const a = d.mapping[key]; const i = a.indexOf(c); i >= 0 ? a.splice(i, 1) : a.push(c); });
  return html`<div class="stack">
    ${!schema && html`<${Alert} tone="info" title="Column list not loaded"
        action=${html`<${Button} size="sm" icon="plug" loading=${testing} onClick=${runTest}>Load columns</${Button}>`}>
      Test the connection to pick columns from the source schema. You can also type column names below.</${Alert}>`}
    <div class="form-section">
      <h3><${Icon} name="key" />Document identity</h3>
      <${Field} label="Primary key columns" hint=${pkCols.length ? `None selected uses the source primary key (${pkCols.join(', ')}). Several columns build a composite id.` : 'Empty uses the source primary key. Several columns build a composite id.'}>
        ${cols.length ? html`<${ColumnChips} columns=${cols} selected=${m.primaryKey} pkCols=${pkCols} onToggle=${toggleIn('primaryKey')} label="Primary key columns" />`
          : html`<input class="input mono" aria-label="Primary key columns" placeholder="id" value=${listText(m.primaryKey)} onChange=${(e) => set((d) => { d.mapping.primaryKey = parseList(e.currentTarget.value); })} />`}
      </${Field}>
      <div class="form-grid">
        <${F} id="f-pkf" label="Primary key field" hint="Document field that holds the id in Meilisearch." error=${errors['mapping.primaryKeyField']}>
          <${TextInput} id="f-pkf" mono value=${m.primaryKeyField} error=${errors['mapping.primaryKeyField']} placeholder="id" onInput=${(v) => set((d) => { d.mapping.primaryKeyField = v; })} /></${F}>
        <div style="padding-top:26px"><${Toggle} id="f-keep" label="Keep key columns" hint="Also copy the original key columns into the document." checked=${m.keepKeyColumns} onChange=${(v) => set((d) => { d.mapping.keepKeyColumns = v; })} /></div>
      </div>
    </div>

    <div class="form-section">
      <h3><${Icon} name="filter" />Fields</h3>
      ${cols.length ? html`<p class="explain">Include restricts the copied columns (none = all). Exclude drops columns after include. Rename sets the document field name.</p>
        <div class="card" style="box-shadow:none"><div class="table-wrap"><table class="table table-compact col-table" data-testid="mapping-columns">
          <thead><tr><th>Column</th><th class="hide-sm">Type</th><th style="text-align:center">Include</th><th style="text-align:center">Exclude</th><th>Rename to</th></tr></thead>
          <tbody>${cols.map((c, i) => html`<tr key=${c} style=${`--i:${Math.min(i, 10)}`} class=${m.exclude.includes(c) ? 'subtle' : ''}>
            <td class="mono">${c} ${pkCols.includes(c) && html`<${Tag} tone="primary">PK</${Tag}>`}</td>
            <td class="hide-sm subtle mono">${types[c]}</td>
            <td style="text-align:center"><input type="checkbox" class="checkbox" checked=${m.include.includes(c)} onChange=${() => toggleIn('include')(c)} aria-label=${`Include ${c}`} /></td>
            <td style="text-align:center"><input type="checkbox" class="checkbox" checked=${m.exclude.includes(c)} onChange=${() => toggleIn('exclude')(c)} aria-label=${`Exclude ${c}`} /></td>
            <td><input class="input input-sm mono" placeholder=${c} value=${m.rename[c] || ''} aria-label=${`Rename ${c}`} onInput=${(e) => { const v = e.currentTarget.value; set((d) => { d.mapping.rename[c] = v; }); }} /></td>
          </tr>`)}</tbody></table></div></div>`
      : html`<div class="form-grid">
          <${Field} label="Include columns" hint="Comma separated; empty copies all."><input class="input mono" aria-label="Include columns" value=${listText(m.include)} onChange=${(e) => set((d) => { d.mapping.include = parseList(e.currentTarget.value); })} /></${Field}>
          <${Field} label="Exclude columns" hint="Comma separated."><input class="input mono" aria-label="Exclude columns" value=${listText(m.exclude)} onChange=${(e) => set((d) => { d.mapping.exclude = parseList(e.currentTarget.value); })} /></${Field}>
        </div>`}
    </div>

    <div class="form-section">
      <h3><${Icon} name="sparkle" />Transforms</h3>
      <div class="form-grid">
        <${F} id="f-sdc" label="Soft delete column" error=${errors['mapping.softDeleteColumn']} hint="Rows matching the value (or non-null when no value) become deletes.">
          ${cols.length ? html`<select id="f-sdc" class="select" value=${m.softDeleteColumn} onChange=${(e) => set((d) => { d.mapping.softDeleteColumn = e.currentTarget.value; })}>
              <option value="">None</option>${cols.map((c) => html`<option value=${c}>${c}</option>`)}</select>`
            : html`<${TextInput} id="f-sdc" mono value=${m.softDeleteColumn} placeholder="deleted_at" onInput=${(v) => set((d) => { d.mapping.softDeleteColumn = v; })} />`}</${F}>
        <${F} id="f-sdv" label="Soft delete value" hint="Empty means “any non-null value”."><${TextInput} id="f-sdv" mono value=${m.softDeleteValue} disabled=${!m.softDeleteColumn} placeholder="true" onInput=${(v) => set((d) => { d.mapping.softDeleteValue = v; })} /></${F}>
        ${['lat', 'lng'].map((k) => html`<${F} id=${`f-geo-${k}`} label=${k === 'lat' ? 'Geo latitude column' : 'Geo longitude column'} error=${k === 'lat' ? errors['mapping.geo'] : undefined} hint=${k === 'lat' ? 'Builds the _geo field for geosearch.' : ''}>
          ${cols.length ? html`<select id=${`f-geo-${k}`} class="select" value=${m.geo[k]} onChange=${(e) => { const v = e.currentTarget.value; set((d) => { d.mapping.geo[k] = v; }); }}>
              <option value="">None</option>${cols.map((c) => html`<option value=${c}>${c}</option>`)}</select>`
            : html`<${TextInput} id=${`f-geo-${k}`} mono value=${m.geo[k]} onInput=${(v) => set((d) => { d.mapping.geo[k] = v; })} />`}</${F}>`)}
      </div>
      <${Field} label="Time format" hint=${{ rfc3339: 'ISO-8601 strings: readable, but not usable for range filters or sorting.', unix: 'Seconds since epoch: dates become filterable and sortable numbers.', unix_ms: 'Milliseconds since epoch.' }[m.timeFormat]}>
        <div class="seg" role="group" aria-label="Time format">${['rfc3339', 'unix', 'unix_ms'].map((f) => html`<button type="button" aria-pressed=${m.timeFormat === f} onClick=${() => set((d) => { d.mapping.timeFormat = f; })}>${f}</button>`)}</div>
      </${Field}>
    </div>

    <div class="form-section">
      <h3><${Icon} name="layers" />Static fields</h3>
      <p class="explain">Added to every document, e.g. a tenant id for multi-tenant indexes. Values are parsed as JSON when possible.</p>
      ${staticRows.map(([k, v], i) => html`<div class="kv-row" key=${i}>
        <input class="input mono" placeholder="field" aria-label=${`Static field ${i + 1} name`} value=${k} onInput=${(e) => { const r = staticRows.slice(); r[i] = [e.currentTarget.value, v]; setStaticRows(r); }} />
        <input class="input mono" placeholder="value" aria-label=${`Static field ${i + 1} value`} value=${v} onInput=${(e) => { const r = staticRows.slice(); r[i] = [k, e.currentTarget.value]; setStaticRows(r); }} />
        <${Button} kind="ghost" icon="x" aria-label=${`Remove static field ${i + 1}`} onClick=${() => setStaticRows(staticRows.filter((_, j) => j !== i))} />
      </div>`)}
      ${errors['mapping.static'] && html`<p class="field-error">${errors['mapping.static']}</p>`}
      <div><${Button} size="sm" icon="plus" onClick=${() => setStaticRows([...staticRows, ['', '']])}>Add static field</${Button}></div>
    </div>
  </div>`;
}


function DestinationPicker({ s, set, list }) {
  if (!list) return html`<p class="subtle">Loading destinations…</p>`;
  if (!list.length) return html`<${Alert} tone="warn" title="No Meilisearch destination">Add one under <a href="#/destinations">Destinations</a> before creating tasks.</${Alert}>`;
  const def = list.find((d) => d.isDefault);
  const cur = s.index.destinationId || (def ? def.id : '');
  if (singleDestination() || list.length === 1) {
    const d = list.find((x) => x.id === cur) || list[0];
    return html`<div class="dest-readonly" data-testid="wizard-destination"><${HealthDot} healthy=${d.health ? d.health.healthy : null} /><span>Writes to <b>${d.name}</b></span><span class="mono subtle">${hostOf(d.url)}</span></div>`;
  }
  return html`<${Field} label="Destination" hint="The Meilisearch instance this task writes to.">
    <div class="radio-cards dest-cards" role="radiogroup" aria-label="Destination" data-testid="wizard-destination">
      ${list.map((d) => html`<label class=${cls('radio-card', d.health && !d.health.healthy && 'is-down')} key=${d.id} data-destination-card=${d.id}>
        <input type="radio" name="destination" value=${d.id} checked=${cur === d.id} onChange=${() => set((x) => { x.index.destinationId = d.isDefault ? '' : d.id; })} />
        <b class="row" style="gap:7px"><${HealthDot} healthy=${d.health ? d.health.healthy : null} />${d.name}${d.isDefault && html`<span class="tag tag-primary">default</span>`}</b>
        <small class="mono">${hostOf(d.url)}</small>
        <small>${d.health && d.health.healthy ? `v${d.health.version} · ${fmtInt(d.health.indexes)} indexes` : (d.health && d.health.error) || 'unreachable'}</small>
      </label>`)}
    </div></${Field}>`;
}

function StepIndex({ s, set, errors, settingsText, setSettingsText, fields, dests }) {
  const destId = s.index.destinationId || ((dests || []).find((d) => d.isDefault) || {}).id || '';
  const existing = useFetch(() => (destId ? api.indexesOf(destId).catch(() => []) : Promise.resolve([])), [destId]);
  let parsed = {};
  try { parsed = settingsText.trim() ? JSON.parse(settingsText) : {}; } catch { parsed = {}; }
  const ixList = existing.data || [];
  const match = ixList.find((x) => x.uid === s.index.uid.trim());
  return html`<div class="stack">
    <${DestinationPicker} s=${s} set=${set} list=${dests} />
    <div class="form-grid">
      <${F} id="f-uid" label="Index uid" required error=${errors['index.uid']} hint=${match ? `Existing index · ${fmtInt(match.numberOfDocuments)} documents${match.managedBy && match.managedBy.length ? ` · also written by ${match.managedBy.join(', ')}` : ''}` : 'Created on first write if it does not exist.'}>
        <${TextInput} id="f-uid" mono value=${s.index.uid} error=${errors['index.uid']} placeholder="products" list="f-uid-list" autocapitalize="off" spellcheck="false" onInput=${(v) => set((d) => { d.index.uid = v; })} />
        <datalist id="f-uid-list">${ixList.map((x) => html`<option value=${x.uid}>${fmtInt(x.numberOfDocuments)} docs</option>`)}</datalist></${F}>
    </div>
    <${Field} label="Write mode">
      <div class="radio-cards" role="radiogroup" aria-label="Write mode">
        ${[['replace', 'Replace', 'Documents are replaced as a whole (POST /documents). Fields removed in the source disappear.'], ['upsert', 'Upsert', 'Fields are merged into existing documents (PUT /documents). Useful when several tasks write one index.']].map(([v, l, d]) => html`
          <label class="radio-card"><input type="radio" name="writeMode" value=${v} checked=${s.index.writeMode === v} onChange=${() => set((x) => { x.index.writeMode = v; })} /><b>${l}</b><small>${d}</small></label>`)}
      </div>
    </${Field}>
    <${Toggle} id="f-manage" label="Manage index settings" hint="Apply these settings when the task starts and report drift in the schema check." checked=${s.index.manageSettings} onChange=${(v) => set((d) => { d.index.manageSettings = v; })} />
    ${errors['index.settings'] && html`<p class="field-error" role="alert">${errors['index.settings']}</p>`}
    <${SettingsEditor} value=${parsed} idPrefix="wz" jsonTestId="settings-json" suggestions=${fields}
      onChange=${(v) => setSettingsText(Object.keys(v).length ? pretty(v) : '')} />
  </div>`;
}

function NumField({ id, label, value, onInput, error, hint, min = 1, step }) {
  return html`<${F} id=${id} label=${label} error=${error} hint=${hint}><input id=${id} class="input" type="number" min=${min} step=${step} value=${value} aria-invalid=${error ? 'true' : undefined} onInput=${(e) => onInput(e.currentTarget.value)} /></${F}>`;
}

function StepSync({ s, set, errors }) {
  const y = s.sync;
  const sf = (k) => (v) => set((d) => { d.sync.safety[k] = v; });
  return html`<div class="stack">
    <${Field} label="Sync mode">
      <div class="radio-cards" role="radiogroup" aria-label="Sync mode">
        ${[['snapshot_stream', 'Snapshot + stream', 'Copy existing rows, then stream every change. The usual choice.'], ['stream', 'Stream only', 'Start from the current position; existing rows are not copied.'], ['snapshot', 'Snapshot only', 'Copy existing rows once, then stop.']].map(([v, l, d]) => html`
          <label class="radio-card"><input type="radio" name="mode" value=${v} checked=${y.mode === v} onChange=${() => set((x) => { x.sync.mode = v; })} /><b>${l}</b><small>${d}</small></label>`)}
      </div>
    </${Field}>
    <div class="form-section">
      <h3><${Icon} name="gauge" />Batching</h3>
      <div class="form-grid" style="grid-template-columns:repeat(auto-fit,minmax(180px,1fr))">
        <${NumField} id="f-batch" label="Batch size" value=${y.batchSize} error=${errors['sync.batchSize']} hint="Documents per Meilisearch request." onInput=${(v) => set((d) => { d.sync.batchSize = v; })} />
        <${F} id="f-flush" label="Flush interval" error=${errors['sync.flushInterval']} hint="Max wait before a partial batch is sent."><${TextInput} id="f-flush" mono value=${y.flushInterval} error=${errors['sync.flushInterval']} onInput=${(v) => set((d) => { d.sync.flushInterval = v; })} /></${F}>
        <${NumField} id="f-inflight" label="Max in-flight" value=${y.maxInflight} error=${errors['sync.maxInflight']} hint="Meilisearch tasks enqueued but not confirmed. Higher = more throughput." onInput=${(v) => set((d) => { d.sync.maxInflight = v; })} />
      </div>
    </div>
    <div class="form-section">
      <h3><${Icon} name="restart" />Retry</h3>
      <div class="form-grid" style="grid-template-columns:repeat(auto-fit,minmax(180px,1fr))">
        <${NumField} id="f-attempts" label="Max attempts" value=${y.retry.maxAttempts} error=${errors['sync.retry.maxAttempts']} onInput=${(v) => set((d) => { d.sync.retry.maxAttempts = v; })} />
        <${F} id="f-ib" label="Initial backoff" error=${errors['sync.retry.initialBackoff']}><${TextInput} id="f-ib" mono value=${y.retry.initialBackoff} onInput=${(v) => set((d) => { d.sync.retry.initialBackoff = v; })} /></${F}>
        <${F} id="f-mb" label="Max backoff" error=${errors['sync.retry.maxBackoff']}><${TextInput} id="f-mb" mono value=${y.retry.maxBackoff} onInput=${(v) => set((d) => { d.sync.retry.maxBackoff = v; })} /></${F}>
      </div>
    </div>
    <div class="form-section">
      <h3><${Icon} name="shield" />Safety guards</h3>
      <div class="form-grid">
        <${F} id="f-ratio" label=${`Max delete ratio · ${Math.round(Number(y.safety.maxDeleteRatio || 0) * 100)}%`} error=${errors['sync.safety.maxDeleteRatio']}
          hint="Hold the task when one flush would delete more than this share of the index. 0 disables the guard.">
          <input id="f-ratio" type="range" class="range" min="0" max="1" step="0.01" value=${y.safety.maxDeleteRatio} onInput=${(e) => sf('maxDeleteRatio')(Number(e.currentTarget.value))} /></${F}>
        <${NumField} id="f-mindocs" label="Min docs for delete guard" value=${y.safety.minDocsForDeleteGuard} min="0" hint="The ratio check is skipped for smaller indexes." onInput=${sf('minDocsForDeleteGuard')} />
      </div>
      <div class="stack-sm" style="gap:14px">
        <${Toggle} id="f-trunc" label="Allow TRUNCATE" hint="A TRUNCATE in the source clears the index. When off, the task is held for an operator." checked=${y.safety.allowTruncate} onChange=${sf('allowTruncate')} />
        <${Toggle} id="f-zdr" label="Zero-downtime resync" hint="Rebuild into a shadow index and swap it in when complete, so search keeps working." checked=${y.safety.zeroDowntimeResync} onChange=${sf('zeroDowntimeResync')} />
        <${Toggle} id="f-dump" label="Dump before resync" hint="Ask Meilisearch for a dump before a resync starts." checked=${y.safety.dumpBeforeResync} onChange=${sf('dumpBeforeResync')} />
      </div>
    </div>
  </div>`;
}

function StepReview({ task, errors, serverError, goto, type, connector, destName }) {
  const bad = Object.keys(errors);
  const src = sourceInfo(task.source.type || type);
  return html`<div class="stack">
    ${bad.length ? html`<${Alert} tone="bad" title=${`${bad.length} field${bad.length > 1 ? 's need' : ' needs'} attention`}>
        <ul>${bad.map((k) => { const st = STEPS.findIndex((x) => stepErrors(x.id, { [k]: 1 }).length || (x.id === 'source' && k === 'source.type')); return html`<li><button type="button" class="btn btn-ghost btn-sm" style="height:auto;padding:0 2px" onClick=${() => goto(Math.max(0, st))}>${STEPS[Math.max(0, st)].label}</button>: ${errors[k]}</li>`; })}</ul></${Alert}>`
      : html`<${Alert} tone="ok" title="Ready">Everything looks valid.</${Alert}>`}
    <${ErrorBox} error=${serverError} />
    <div class="grid grid-2">
      <dl class="kv card card-pad">
        <dt>Name</dt><dd>${task.name || '—'}</dd>
        <dt>Source</dt><dd>${src.label} · <span class="mono">${task.source.table || '—'}</span></dd>
        ${task.source.connectorId && html`<dt>Connector</dt><dd><a href=${`#/connectors/${encodeURIComponent(task.source.connectorId)}`}>${connector ? connector.name : task.source.connectorId}</a></dd>`}
        <dt>Index</dt><dd class="mono">${task.index.uid || '—'}</dd>
        ${destName && html`<dt>Destination</dt><dd>${destName}</dd>`}
        <dt>Mode</dt><dd>${task.sync.mode} · ${task.index.writeMode}</dd>
      </dl>
      <dl class="kv card card-pad">
        <dt>Primary key</dt><dd class="mono">${(task.mapping.primaryKey || ['(source)']).join(' + ')} → ${task.mapping.primaryKeyField}</dd>
        <dt>Batching</dt><dd>${task.sync.batchSize} docs / ${task.sync.flushInterval} · ${task.sync.maxInflight} in flight</dd>
        <dt>Delete guard</dt><dd>${task.sync.safety.maxDeleteRatio ? `${Math.round(task.sync.safety.maxDeleteRatio * 100)}%` : 'off'}</dd>
        <dt>Settings</dt><dd>${task.index.settings ? `${Object.keys(task.index.settings).length} keys${task.index.manageSettings ? ', managed' : ''}` : 'none'}</dd>
      </dl>
    </div>
    <${JsonView} value=${task} maxHeight=${420} label="Task JSON" />
  </div>`;
}

// ---------------------------------------------------------------- page
export function WizardPage({ id, preset }) {
  const isEdit = !!id;
  const conns = useFetch(() => api.connectors().catch(() => []), []);
  const [newConn, setNewConn] = useState(false);
  const [autoSchema, setAutoSchema] = useState({ schema: null, loading: false, key: '' });
  const dests = useDestinations();
  const [s, setS] = useState(() => ({ ...blankTask(), isEdit }));
  const [loading, setLoading] = useState(isEdit);
  const [loadErr, setLoadErr] = useState(null);
  const [step, setStep] = useState(0);
  const [reached, setReached] = useState(isEdit ? STEPS.length - 1 : 0);
  const [shown, setShown] = useState({});
  const [settingsText, setSettingsText] = useState('');
  const [staticRows, setStaticRows] = useState([]);
  const [test, setTest] = useState(null);
  const [testing, setTesting] = useState(false);
  const [saving, setSaving] = useState('');
  const [serverError, setServerError] = useState(null);

  // #/tasks/new?connector=…&table=… (from the connector table browser)
  useEffect(() => {
    if (isEdit || !preset || !preset.connectorId) return;
    api.connector(preset.connectorId).then((c) => {
      set((d) => { if (preset.destinationId) d.index.destinationId = preset.destinationId; d.source.connectorId = c.id; d.source.type = c.type; d.source.table = preset.table || ''; if (preset.schema) d.source.schema = preset.schema; else if (c.type === 'postgres') d.source.schema = 'public'; if (!d.name && preset.table) d.name = preset.table.replace(/[_.-]+/g, ' ').replace(/^./, (x) => x.toUpperCase()); });
      setStep(1); setReached(1);
    }).catch((e) => toast({ tone: 'bad', title: 'Connector not found', message: e.message }));
  }, []);

  useEffect(() => {
    if (!isEdit) return;
    api.task(id).then((v) => {
      setS({ ...fromTask(v), isEdit: true });
      setSettingsText(v.index && v.index.settings ? pretty(v.index.settings) : '');
      setStaticRows(Object.entries((v.mapping && v.mapping.static) || {}).map(([k, val]) => [k, typeof val === 'string' ? val : JSON.stringify(val)]));
      setStep(1);
      setLoading(false);
    }).catch((e) => { setLoadErr(e); setLoading(false); });
  }, [id]);

  const set = (fn) => setS((prev) => { const d = JSON.parse(JSON.stringify(prev)); fn(d); return d; });
  const errors = useMemo(() => validate(s, settingsText, staticRows), [s, settingsText, staticRows]);
  const task = useMemo(() => buildTask(s, settingsText, staticRows), [s, settingsText, staticRows]);
  const schema = test && test.ok ? test.schema : null;
  const visibleErrors = (i) => (shown[i] ? Object.fromEntries(Object.entries(errors).filter(([k]) => STEPS[i].id === 'review' || stepErrors(STEPS[i].id, { [k]: 1 }).length || (STEPS[i].id === 'source' && k === 'source.type'))) : {});
  const stepHasErr = (i) => (STEPS[i].id === 'source' ? !!errors['source.type'] : stepErrors(STEPS[i].id, errors).length > 0);

  // document fields for the settings helper: renamed, minus excluded and key columns
  const fields = useMemo(() => {
    const m = s.mapping;
    const cols = schema ? schema.columns.map((c) => c.name) : [];
    const pk = m.primaryKey.length ? m.primaryKey : (schema && schema.primaryKey) || [];
    const out = [m.primaryKeyField || 'id'];
    for (const c of cols) {
      if (m.include.length && !m.include.includes(c)) continue;
      if (m.exclude.includes(c)) continue;
      if (pk.includes(c) && !m.keepKeyColumns) continue;
      if (m.geo && (c === m.geo.lat || c === m.geo.lng)) continue;
      out.push((m.rename[c] || '').trim() || c);
    }
    if (m.geo && m.geo.lat && m.geo.lng) out.push('_geo');
    for (const [k] of staticRows) if (k.trim()) out.push(k.trim());
    return [...new Set(out)];
  }, [s.mapping, schema, staticRows]);

  // SQL polling: read the columns as soon as a table is chosen, to suggest the cursor column
  useEffect(() => {
    const key = `${s.source.connectorId}|${s.source.table}`;
    if (s.source.type !== 'sqlpoll' || !s.source.connectorId || !s.source.table.trim() || autoSchema.key === key) return undefined;
    const t = setTimeout(async () => {
      setAutoSchema({ schema: null, loading: true, key });
      try {
        const r = await api.testTask(buildTask(s, settingsText, staticRows));
        setAutoSchema({ schema: r.ok ? r.schema : null, loading: false, key });
        if (r.ok && !test) setTest(r);
      } catch { setAutoSchema({ schema: null, loading: false, key }); }
    }, 450);
    return () => clearTimeout(t);
  }, [s.source.connectorId, s.source.table, s.source.type]);
  const runTest = async () => {
    setTesting(true);
    try {
      const r = await api.testTask(task);
      setTest(r);
      if (r.ok) toast({ title: 'Connection OK', message: `${(r.schema && r.schema.columns.length) || 0} columns found` });
    } catch (e) { setTest({ ok: false, error: e.message }); }
    setTesting(false);
  };

  const goto = (i) => {
    if (i > step) {
      // validate the steps being left before moving forward
      for (let k = step; k < i; k++) {
        if (stepHasErr(k)) { setShown((x) => ({ ...x, [k]: true })); setStep(k); return; }
      }
    }
    if (STEPS[i].id === 'index' && !s.index.uid && s.source.table) set((d) => { d.index.uid = d.source.table.split('.').pop().replace(/[^a-zA-Z0-9_-]/g, '_'); });
    setShown((x) => ({ ...x, [step]: true, ...(STEPS[i].id === 'review' ? { [i]: true } : {}) }));
    setStep(i);
    setReached((r) => Math.max(r, i));
    window.scrollTo({ top: 0, behavior: 'smooth' });
  };

  const save = async (start) => {
    setShown(Object.fromEntries(STEPS.map((_, i) => [i, true])));
    if (Object.keys(errors).length) return;
    setSaving(start ? 'start' : 'save');
    setServerError(null);
    try {
      const body = task;
      const v = isEdit ? await api.updateTask(id, body) : await api.createTask(body, start);
      toast({ title: isEdit ? 'Task saved' : start ? 'Task created and started' : 'Task created', message: v.name || v.id });
      navigate(`#/tasks/${encodeURIComponent(v.id)}`);
    } catch (e) { setServerError(e); setSaving(''); }
  };

  if (!can(isEdit ? 'task.edit' : 'task.create')) return html`<${PageHeader} title=${isEdit ? 'Edit task' : 'New task'} back=${{ href: '#/tasks', label: 'Tasks' }} /><${Forbidden} role="admin" what=${isEdit ? 'task editing' : 'task creation'} />`;
  if (loading) return html`<${PageHeader} title="Edit task" back=${{ href: '#/tasks', label: 'Tasks' }} /><div class="card"><${SkeletonRows} rows=${6} cols=${3} /></div>`;
  if (loadErr) return html`<${PageHeader} title="Edit task" back=${{ href: '#/tasks', label: 'Tasks' }} /><${ErrorBox} error=${loadErr} />`;

  const cur = STEPS[step];
  const errs = visibleErrors(step);
  const body = {
    source: () => html`<${StepSource} s=${s} set=${set} errors=${errs} connectors=${conns.data} onNewConnector=${() => setNewConn(true)} legacy=${isEdit && !s.source.connectorId} />`,
    connection: () => html`<${StepConnection} s=${s} set=${set} errors=${errs} test=${test} testing=${testing} runTest=${runTest} setTest=${setTest} connectors=${conns.data} goto=${goto} autoSchema=${test && test.ok ? { schema: test.schema, loading: false } : autoSchema} />`,
    mapping: () => html`<${StepMapping} s=${s} set=${set} errors=${errs} schema=${schema} testing=${testing} runTest=${runTest} staticRows=${staticRows} setStaticRows=${setStaticRows} />`,
    index: () => html`<${StepIndex} s=${s} set=${set} errors=${errs} settingsText=${settingsText} setSettingsText=${setSettingsText} fields=${fields} dests=${dests} />`,
    sync: () => html`<${StepSync} s=${s} set=${set} errors=${errs} />`,
    review: () => html`<${StepReview} task=${task} errors=${errors} serverError=${serverError} goto=${goto} type=${s.source.type}
      connector=${s.source.connectorId && (conns.data || []).find((c) => c.id === s.source.connectorId)}
      destName=${dests && dests.length > 1 ? ((dests.find((d) => d.id === s.index.destinationId) || dests.find((d) => d.isDefault)) || {}).name : ''} />`,
  }[cur.id];

  return html`
    <${PageHeader} title=${isEdit ? `Edit ${s.name || id}` : 'New task'} doc="wizard" docSource=${(s.source && s.source.type) || undefined} back=${isEdit ? { href: `#/tasks/${encodeURIComponent(id)}/config`, label: 'Back to task' } : { href: '#/tasks', label: 'Tasks' }}
      subtitle=${isEdit ? 'Saving restarts the task if it is running. Masked secrets are kept unless you replace them.' : 'Connect a source, shape documents and choose the Meilisearch index to keep in sync.'} />
    <div class="wizard">
      <ol class="steps" aria-label="Steps">
        ${STEPS.map((st, i) => html`<li key=${st.id}><button type="button" class=${cls('step-btn', i === step && 'is-active', i < step && !stepHasErr(i) && 'is-done', shown[i] && stepHasErr(i) && i !== STEPS.length - 1 && 'has-error')}
            disabled=${i > reached + 1 || (i > 0 && !s.source.connectorId)} aria-current=${i === step ? 'step' : undefined} onClick=${() => goto(i)} data-step=${st.id}>
          <span class="step-num">${i < step && !stepHasErr(i) ? html`<${Icon} name="check" size=${14} />` : i + 1}</span>
          <span class="step-text"><b>${st.label}</b><small>${st.hint}</small></span>
        </button></li>`)}
      </ol>
      <div class="wizard-panel">
        <div class="steps-compact" aria-hidden="true"><span>Step ${step + 1} of ${STEPS.length}</span><b>${cur.label}</b>
          ${step < STEPS.length - 1 && html`<span class="subtle">Next: ${STEPS[step + 1].label}</span>`}</div>
        <div class="wizard-progress" aria-hidden="true"><span style=${`width:${((step + 1) / STEPS.length) * 100}%`}></span></div>
        <section class="card card-pad" key=${cur.id} aria-labelledby="step-title">
          <h2 id="step-title" class="sr-only">${cur.label}</h2>
          ${body()}
        </section>
        <div class="wizard-nav">
          <${Button} icon="arrow-left" disabled=${step === 0 || (isEdit && step === 1)} onClick=${() => goto(step - 1)}>Back</${Button}>
          ${cur.id === 'review'
            ? html`<div class="btn-group">
                ${isEdit ? html`<${Button} kind="primary" icon="check" loading=${!!saving} onClick=${() => save(false)} data-testid="save-task">Save changes</${Button}>`
                  : html`<${Button} icon="check" loading=${saving === 'save'} disabled=${!!saving} onClick=${() => save(false)} data-testid="create-task">Create</${Button}>
                    <${Button} kind="primary" icon="play" loading=${saving === 'start'} disabled=${!!saving} onClick=${() => save(true)} data-testid="create-start-task">Create & start</${Button}>`}
              </div>`
            : html`<${Button} kind="primary" iconRight="arrow-right" disabled=${step === 0 && !s.source.connectorId} onClick=${() => goto(step + 1)} data-testid="wizard-next">Next: ${STEPS[step + 1].label}</${Button}>`}
        </div>
      </div>
    </div>
    <${ConnectorEditor} open=${newConn} connector=${null} onClose=${() => setNewConn(false)}
      onSaved=${(c) => { conns.setData((l) => [...(l || []), c]); set((d) => { d.source.connectorId = c.id; d.source.type = c.type; d.source.table = ''; if (c.type === 'postgres' && !d.source.schema) d.source.schema = 'public'; }); toast({ tone: 'info', title: 'Connector selected', message: c.name }); }} />`;
}
