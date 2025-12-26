// First-run onboarding: connect Meilisearch → connect a data source → create the first sync.
import { html, useEffect, useLayoutEffect, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { auth, setAuth, useAuth } from '../auth.js';
import { ConnectionFields, pickConnection, validateConnection } from '../connection.js';
import { SourceIcon } from '../components.js';
import { destinationsChanged, singleDestination } from '../destinations.js';
import { navigate } from '../router.js';
import { Icon, Logo } from '../icons.js';
import { Alert, Button, ErrorBox, Field, toast } from '../ui.js';
import { SOURCES, cls, fmtInt, lsGet, lsSet } from '../util.js';
import { DestinationForm, destBody, destFormErrors } from './destinations.js';
import { connectorsChanged } from './connectors.js';

const dismissKey = () => `cdc.onboarding.dismissed.${(auth.user && auth.user.id) || 'anon'}`;
export const onboardingDismissed = () => lsGet(dismissKey(), '') === '1';
export const dismissOnboarding = () => lsSet(dismissKey(), '1');

/** Re-read /auth/config so onboarding counts are current. */
export async function refreshConfig() {
  try { const c = await api.authConfig(); setAuth({ config: c }); return c; } catch { return auth.config; }
}

export const needsOnboarding = (a) => !!(a.user && a.user.role === 'admin' && a.config && a.config.onboarding && !a.config.onboarding.complete && !onboardingDismissed());

// ---------------------------------------------------------------- steps
function StepDestination({ onDone, onSkip, existing }) {
  const [f, setF] = useState({ name: 'Meilisearch', url: '', apiKey: '', timeout: '30s', gzip: true, tags: '', description: '' });
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState('');
  const [test, setTest] = useState(null);
  const [err, setErr] = useState(null);
  const errs = destFormErrors(f);
  const runTest = async () => {
    setTried(true);
    if (errs.url) return;
    setBusy('test'); setTest(null);
    try { setTest(await api.testDestinationDraft(destBody(f))); } catch (e) { setTest({ ok: false, error: e.message }); }
    setBusy('');
  };
  const save = async () => {
    setTried(true);
    if (Object.keys(errs).length) return;
    setBusy('save'); setErr(null);
    try { const d = await api.createDestination(destBody(f)); destinationsChanged(); toast({ title: 'Meilisearch connected', message: d.name }); onDone({ destination: d }); }
    catch (e) { setErr(e); setBusy(''); }
  };
  return html`<div class="stack">
    <div class="ob-head"><span class="ob-icon"><${Icon} name="database" size=${22} /></span><div><h2>Connect Meilisearch</h2>
      <p class="muted">Where your documents are indexed. Use the instance URL and a key with admin access.</p></div></div>
    ${existing > 0 && html`<${Alert} tone="ok" title="Already connected">${existing} destination${existing > 1 ? 's are' : ' is'} configured. You can skip this step.</${Alert}>`}
    <${ErrorBox} error=${err} />
    <${DestinationForm} value=${f} onChange=${(v) => { setF(v); setTest(null); }} errors=${tried ? errs : {}} />
    ${test && (test.ok ? html`<${Alert} tone="ok" title="Connected" data-testid="ob-dest-test">Meilisearch v${test.version} · ${test.latencyMs} ms · ${fmtInt(test.indexes)} indexes</${Alert}>`
      : html`<${Alert} tone="bad" title="Connection failed" data-testid="ob-dest-test">${test.error}</${Alert}>`)}
    <div class="ob-actions">
      <button type="button" class="link-btn" onClick=${onSkip} data-testid="ob-skip">Skip for now</button>
      <span class="grow"></span>
      <${Button} icon="plug" loading=${busy === 'test'} onClick=${runTest}>Test connection</${Button}>
      <${Button} kind="primary" icon="check" loading=${busy === 'save'} onClick=${save} data-testid="ob-dest-save">Save and continue</${Button}>
    </div>
  </div>`;
}

function StepConnector({ onDone, onSkip }) {
  const [type, setType] = useState('');
  const [name, setName] = useState('');
  const [config, setConfig] = useState({ driver: 'pgx' });
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState('');
  const [test, setTest] = useState(null);
  const [err, setErr] = useState(null);
  const cErr = type ? validateConnection(type, config) : {};
  const body = () => ({ name: name.trim() || `${(SOURCES.find((s) => s.type === type) || {}).label} source`, type, config: pickConnection(type, config), tags: [] });
  const runTest = async () => {
    setTried(true);
    if (!type || Object.keys(cErr).length) return;
    setBusy('test'); setTest(null);
    try { setTest(await api.testConnectorDraft(body())); } catch (e) { setTest({ ok: false, error: e.message }); }
    setBusy('');
  };
  const save = async () => {
    setTried(true);
    if (!type || Object.keys(cErr).length) return;
    setBusy('save'); setErr(null);
    try { const c = await api.createConnector(body()); connectorsChanged(); toast({ title: 'Source connected', message: c.name }); onDone({ connector: c, tables: test && test.ok ? test.tables : null }); }
    catch (e) { setErr(e); setBusy(''); }
  };
  return html`<div class="stack">
    <div class="ob-head"><span class="ob-icon"><${Icon} name="plug" size=${22} /></span><div><h2>Connect a data source</h2>
      <p class="muted">Save the connection once as a connector; every table in it can then be synced.</p></div></div>
    <div class="source-grid source-grid-compact" role="group" aria-label="Source type">${SOURCES.filter((s) => s.type !== 'webhook').map((s, i) => html`<button type="button" key=${s.type} class="source-card is-compact" style=${`--i:${i}`}
      aria-pressed=${type === s.type} data-source=${s.type} onClick=${() => { setType(s.type); setTest(null); }}><${SourceIcon} type=${s.type} /><b>${s.label}</b></button>`)}</div>
    ${tried && !type && html`<p class="field-error" role="alert">Choose a source type</p>`}
    ${type && html`<div class="stack">
      <${ErrorBox} error=${err} />
      <${Field} label="Connector name" htmlFor="ob-cname"><input id="ob-cname" class="input" value=${name} placeholder="Shop database" onInput=${(e) => setName(e.currentTarget.value)} /></${Field}>
      <${ConnectionFields} key=${type} type=${type} value=${config} onChange=${(c) => { setConfig(c); setTest(null); }} errors=${tried ? cErr : {}} idPrefix="ob" />
      ${test && (test.ok ? html`<div class="stack-sm" data-testid="ob-conn-test"><${Alert} tone="ok" title="Connected">${test.version ? `${test.version} · ` : ''}${test.latencyMs} ms · ${(test.tables || []).length} tables</${Alert}>
          <div class="chips">${(test.tables || []).slice(0, 18).map((t) => html`<span class="chip chip-static">${t.name}</span>`)}</div></div>`
        : html`<${Alert} tone="bad" title="Connection failed" data-testid="ob-conn-test">${test.error}</${Alert}>`)}
    </div>`}
    <div class="ob-actions">
      <button type="button" class="link-btn" onClick=${onSkip} data-testid="ob-skip">Skip for now</button>
      <span class="grow"></span>
      <${Button} icon="plug" loading=${busy === 'test'} onClick=${runTest} disabled=${!type}>Test connection</${Button}>
      <${Button} kind="primary" icon="check" loading=${busy === 'save'} onClick=${save} data-testid="ob-conn-save">Save and continue</${Button}>
    </div>
  </div>`;
}

function StepSync({ ctx, onSkip }) {
  const [connectors, setConnectors] = useState(null);
  const [connector, setConnector] = useState(ctx.connector ? ctx.connector.id : '');
  const [tables, setTables] = useState(ctx.tables || null);
  const [table, setTable] = useState('');
  useEffect(() => { api.connectors().then((l) => { setConnectors(l); if (!connector && l[0]) setConnector(l[0].id); }).catch(() => setConnectors([])); }, []);
  useEffect(() => { if (connector) api.connectorTables(connector).then(setTables).catch(() => setTables([])); }, [connector]);
  const go = () => {
    const q = new URLSearchParams();
    if (connector) q.set('connector', connector);
    if (table) q.set('table', table);
    if (ctx.destination) q.set('destination', ctx.destination.id);
    navigate(`#/tasks/new${q.toString() ? `?${q}` : ''}`);
  };
  return html`<div class="stack">
    <div class="ob-head"><span class="ob-icon"><${Icon} name="zap" size=${22} /></span><div><h2>Create your first sync</h2>
      <p class="muted">Pick a table; the task wizard opens with everything filled in so you only review the mapping and index.</p></div></div>
    ${connectors && connectors.length > 1 && html`<${Field} label="Connector" htmlFor="ob-conn"><select id="ob-conn" class="select" value=${connector} onChange=${(e) => { setConnector(e.currentTarget.value); setTable(''); }}>
      ${connectors.map((c) => html`<option value=${c.id}>${c.name}</option>`)}</select></${Field}>`}
    ${connectors && !connectors.length && html`<${Alert} tone="info">No connector yet: the wizard lets you enter a connection directly.</${Alert}>`}
    ${tables && tables.length > 0 && html`<${Field} label="Table">
      <div class="chips" role="group" aria-label="Tables">${tables.slice(0, 30).map((t) => html`<button type="button" class="chip" aria-pressed=${table === t.name} onClick=${() => setTable(t.name)} data-table=${t.name}>
        ${t.name}${t.rows >= 0 ? html`<span class="subtle" style="margin-left:4px">${fmtInt(t.rows)}</span>` : ''}</button>`)}</div></${Field}>`}
    <div class="ob-actions">
      <button type="button" class="link-btn" onClick=${onSkip} data-testid="ob-skip">Skip for now</button>
      <span class="grow"></span>
      <${Button} kind="primary" icon="arrow-right" onClick=${go} disabled=${connectors && connectors.length > 0 && !table} data-testid="ob-open-wizard">Open the task wizard</${Button}>
    </div>
  </div>`;
}

// ---------------------------------------------------------------- page
export function OnboardingPage() {
  const a = useAuth();
  const ob = (a.config && a.config.onboarding) || { destinations: 0, connectors: 0, tasks: 0 };
  const single = singleDestination();
  const steps = [
    ...(single ? [] : [{ id: 'destination', label: 'Meilisearch' }]),
    { id: 'connector', label: 'Data source' },
    { id: 'sync', label: 'First sync' },
  ];
  const [i, setI] = useState(() => (!single && ob.destinations === 0 ? 0 : ob.connectors === 0 ? steps.findIndex((s) => s.id === 'connector') : steps.length - 1));
  const [ctx, setCtx] = useState({});
  useLayoutEffect(() => { sessionStorage.setItem('cdc.onboarding.shown', '1'); }, []);
  const next = (patch) => { if (patch) setCtx({ ...ctx, ...patch }); refreshConfig(); if (i < steps.length - 1) setI(i + 1); else finish(); };
  const finish = () => { dismissOnboarding(); navigate('#/overview'); };
  const cur = steps[i];
  return html`<div class="auth-screen ob-screen">
    <div class="auth-glow" aria-hidden="true"><span></span><span></span><span></span></div>
    <main class="auth-card is-wide ob-card" id="main" data-testid="onboarding">
      <div class="row-between">
        <div class="auth-brand"><${Logo} size=${34} /><span><b>Welcome to Meili CDC</b><small>Let’s get your first pipeline running</small></span></div>
        <button type="button" class="btn btn-ghost btn-icon" aria-label="Dismiss onboarding" title="Dismiss" onClick=${finish} data-testid="ob-dismiss"><${Icon} name="x" /></button>
      </div>
      <ol class="ob-steps" aria-label="Onboarding progress">${steps.map((s, k) => html`<li key=${s.id} class=${cls('ob-step', k === i && 'is-active', k < i && 'is-done')} aria-current=${k === i ? 'step' : undefined}>
        <span class="ob-dot">${k < i ? html`<${Icon} name="check" size=${13} />` : k + 1}</span><span class="ob-label">${s.label}</span></li>`)}</ol>
      <div class="ob-body" key=${cur.id}>
        ${cur.id === 'destination' ? html`<${StepDestination} existing=${ob.destinations} onDone=${next} onSkip=${() => next()} />`
          : cur.id === 'connector' ? html`<${StepConnector} onDone=${next} onSkip=${() => next()} />`
          : html`<${StepSync} ctx=${ctx} onSkip=${finish} />`}
      </div>
      <p class="subtle ob-foot">${h1Hint(ob)}</p>
    </main>
  </div>`;
}

const h1Hint = (ob) => `${ob.destinations} destination${ob.destinations === 1 ? '' : 's'} · ${ob.connectors} connector${ob.connectors === 1 ? '' : 's'} · ${ob.tasks} task${ob.tasks === 1 ? '' : 's'}`;

/** Overview card listing what is left to set up. */
export function GettingStarted() {
  const a = useAuth();
  const [hidden, setHidden] = useState(onboardingDismissed());
  useEffect(() => { refreshConfig(); }, []);
  const ob = a.config && a.config.onboarding;
  if (!ob || ob.complete || hidden || !a.user || a.user.role !== 'admin') return null;
  const single = singleDestination();
  const items = [
    ...(single ? [] : [{ done: ob.destinations > 0, title: 'Connect Meilisearch', href: '#/destinations', cta: 'Add destination' }]),
    { done: ob.connectors > 0, title: 'Connect a data source', href: '#/connectors', cta: 'Add connector' },
    { done: ob.tasks > 0, title: 'Create your first sync', href: '#/tasks/new', cta: 'New task' },
  ];
  const done = items.filter((x) => x.done).length;
  return html`<section class="card getting-started" data-testid="getting-started" aria-labelledby="gs-title">
    <div class="card-head"><h2 id="gs-title"><${Icon} name="sparkle" />Getting started <span class="count-pill">${done}/${items.length}</span></h2>
      <div class="btn-group"><a class="btn btn-secondary btn-sm" href="#/welcome"><span>Guided setup</span></a>
        <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Hide getting started" onClick=${() => { dismissOnboarding(); setHidden(true); }}><${Icon} name="x" size=${15} /></button></div></div>
    <div class="gs-progress" aria-hidden="true"><span style=${`width:${(done / items.length) * 100}%`}></span></div>
    <ol class="gs-list">${items.map((x, k) => html`<li key=${k} class=${x.done ? 'is-done' : ''}>
      <span class="gs-check">${x.done ? html`<${Icon} name="check" size=${14} />` : k + 1}</span><span class="grow">${x.title}</span>
      ${!x.done && html`<a class="btn btn-primary btn-sm" href=${x.href}>${x.cta}</a>`}</li>`)}</ol>
  </section>`;
}
