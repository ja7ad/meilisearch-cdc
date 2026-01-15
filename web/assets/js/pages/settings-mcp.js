// Settings → MCP: enable the Model Context Protocol endpoint, manage tokens and the role × tool access policy (admin).
import { html, useEffect, useLayoutEffect, useMemo, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { ROLES } from '../auth.js';
import { useFetch } from '../hooks.js';
import { Icon } from '../icons.js';
import { Alert, Button, CopyField, Empty, ErrorBox, Field, Modal, RoleBadge, Skeleton, SkeletonRows, Tag, Toggle, confirmDialog, toast, toastError } from '../ui.js';
import { fmtTime, isZeroTime, timeAgo } from '../util.js';

const EXPIRY = [['', 'Never'], ['168h', '7 days'], ['720h', '30 days'], ['2160h', '90 days']];
const GROUPS = [['viewer', 'Read', 'Inspect tasks, indexes and alerts.'], ['moderator', 'Operate', 'Run actions on existing tasks.'], ['admin', 'Admin', 'Change definitions and settings.']];

export const mcpEndpoint = (path = '/mcp') => `${location.origin}${path}`;
export const mcpSnippet = (url, token = '<token>') => JSON.stringify({ mcpServers: { 'meilisearch-cdc': { type: 'http', url, headers: { Authorization: `Bearer ${token}` } } } }, null, 2);

function TokenReveal({ reveal, endpoint, onClose }) {
  return html`<${Modal} open=${!!reveal} onClose=${onClose} title=${reveal ? (reveal.rotated ? 'Token rotated' : 'Token created') : ''} size="md"
    footer=${html`<${Button} kind="primary" onClick=${onClose} data-testid="token-done">I saved it</${Button}>`}>
    ${reveal && html`<div class="stack" data-testid="token-reveal">
      <${Alert} tone="warn" title="Copy it now">This is the only time the token is shown. Store it in your MCP client or a secret manager.${reveal.rotated ? ' The previous token no longer works.' : ''}</${Alert}>
      <${Field} label=${html`Token for <b>${reveal.info.name}</b> <${RoleBadge} role=${reveal.info.role} />`}><${CopyField} value=${reveal.token} label="MCP token" testid="mcp-token-value" /></${Field}>
      <${Field} label="Client configuration"><${CopyField} value=${mcpSnippet(endpoint, reveal.token)} label="client configuration with token" multiline /></${Field}>
    </div>`}
  </${Modal}>`;
}

function GenerateModal({ open, onClose, onCreated }) {
  const [name, setName] = useState('');
  const [role, setRole] = useState('viewer');
  const [exp, setExp] = useState('720h');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  useLayoutEffect(() => { if (open) { setName(''); setRole('viewer'); setExp('720h'); setErr(null); } }, [open]);
  const submit = async (e) => {
    e && e.preventDefault();
    if (!name.trim()) { setErr({ message: 'Give the token a name, e.g. the client that uses it.' }); return; }
    setBusy(true); setErr(null);
    try { const r = await api.createMcpToken({ name: name.trim(), role, expiresIn: exp }); onCreated(r); onClose(); }
    catch (er) { setErr(er); }
    setBusy(false);
  };
  return html`<${Modal} open=${open} onClose=${onClose} title="Generate MCP token" size="sm"
    footer=${html`<${Button} onClick=${onClose}>Cancel</${Button}><${Button} kind="primary" icon="key" loading=${busy} onClick=${submit} data-testid="token-generate">Generate</${Button}>`}>
    <form class="stack" onSubmit=${submit}>
      <${ErrorBox} error=${err} />
      <${Field} label="Name" htmlFor="mt-name" hint="Where it is used, e.g. “Claude Desktop (ops)”."><input id="mt-name" class="input" autocomplete="off" value=${name} onInput=${(e) => setName(e.currentTarget.value)} /></${Field}>
      <${Field} label="Role" htmlFor="mt-role" hint="The client sees the tools this role is granted below.">
        <select id="mt-role" class="select" value=${role} onChange=${(e) => setRole(e.currentTarget.value)}>${ROLES.map((r) => html`<option value=${r}>${r}</option>`)}</select></${Field}>
      <${Field} label="Expires" htmlFor="mt-exp"><select id="mt-exp" class="select" value=${exp} onChange=${(e) => setExp(e.currentTarget.value)}>${EXPIRY.map(([v, l]) => html`<option value=${v}>${l}</option>`)}</select></${Field}>
      <button type="submit" hidden></button>
    </form>
  </${Modal}>`;
}

function TokensCard({ endpoint }) {
  const { data, error, reload } = useFetch(() => api.mcpTokens(), []);
  const [gen, setGen] = useState(false);
  const [reveal, setReveal] = useState(null);
  const rotate = async (t) => {
    const ok = await confirmDialog({ title: `Rotate “${t.name}”?`, message: 'A new token is generated and the current one stops working immediately. Update the client afterwards.', confirmLabel: 'Rotate token' });
    if (!ok) return;
    try { const r = await api.rotateMcpToken(t.id); setReveal({ ...r, rotated: true }); reload(true); } catch (e) { toastError(e, 'Rotate failed'); }
  };
  const revoke = async (t) => {
    const ok = await confirmDialog({ title: `Revoke “${t.name}”?`, message: 'Clients using this token lose access immediately.', typed: t.name, danger: true, confirmLabel: 'Revoke token' });
    if (!ok) return;
    try { await api.revokeMcpToken(t.id); toast({ title: 'Token revoked', message: t.name }); reload(true); } catch (e) { toastError(e, 'Revoke failed'); }
  };
  const expired = (t) => !isZeroTime(t.expiresAt) && Date.parse(t.expiresAt) < Date.now();
  return html`<section class="card">
    <div class="card-head"><h2><${Icon} name="key" />Tokens</h2><${Button} kind="primary" icon="plus" onClick=${() => setGen(true)} data-testid="mcp-generate">Generate token</${Button}></div>
    <${ErrorBox} error=${error && !data && error} onRetry=${() => reload()} />
    ${!data ? html`<${SkeletonRows} rows=${2} cols=${5} />`
      : !data.length ? html`<${Empty} icon="key" title="No tokens">Generate a token for each MCP client so access can be revoked individually.</${Empty}>`
      : html`<div class="table-wrap"><table class="table rtable" data-testid="mcp-tokens">
        <thead><tr><th>Name</th><th>Role</th><th>Prefix</th><th class="hide-sm">Created</th><th>Last used</th><th>Expires</th><th class="col-actions"><span class="sr-only">Actions</span></th></tr></thead>
        <tbody>${data.map((t, i) => html`<tr key=${t.id} style=${`--i:${i}`} data-token=${t.name}>
          <td data-label="Name"><b>${t.name}</b></td>
          <td data-label="Role"><${RoleBadge} role=${t.role} /></td>
          <td data-label="Prefix" class="mono nowrap">${t.prefix}…</td>
          <td data-label="Created" class="hide-sm subtle"><span class="nowrap">${timeAgo(t.createdAt)}</span><small class="block">by ${t.createdBy}</small></td>
          <td data-label="Last used" class="nowrap">${isZeroTime(t.lastUsedAt) ? html`<span class="subtle">never</span>` : timeAgo(t.lastUsedAt)}</td>
          <td data-label="Expires" class="nowrap">${isZeroTime(t.expiresAt) ? html`<span class="subtle">never</span>` : expired(t) ? html`<${Tag} tone="bad">expired</${Tag}>` : fmtTime(t.expiresAt, true)}</td>
          <td class="col-actions"><div class="btn-group">
            <${Button} size="sm" icon="rotate" onClick=${() => rotate(t)} aria-label=${`Rotate ${t.name}`}>Rotate</${Button}>
            <${Button} size="sm" kind="danger-ghost" icon="trash" onClick=${() => revoke(t)} aria-label=${`Revoke ${t.name}`}>Revoke</${Button}></div></td>
        </tr>`)}</tbody></table></div>`}
    <${GenerateModal} open=${gen} onClose=${() => setGen(false)} onCreated=${(r) => { setReveal(r); reload(true); }} />
    <${TokenReveal} reveal=${reveal} endpoint=${endpoint} onClose=${() => setReveal(null)} />
  </section>`;
}

const ROLE_LABEL = { viewer: 'Viewer', moderator: 'Moderator', admin: 'Admin' };

/** Small accessible switch (checkbox with role=switch). `mixed` renders the indeterminate state. */
function Switch({ checked, mixed, onChange, label, testid }) {
  return html`<label class=${`mini-switch ${mixed ? 'is-mixed' : ''}`} title=${label}>
    <input type="checkbox" role="switch" checked=${checked} aria-checked=${mixed ? 'mixed' : String(!!checked)} aria-label=${label} data-testid=${testid}
      onChange=${(e) => onChange(e.currentTarget.checked)} />
    <span class="mini-track" aria-hidden="true"><span class="mini-thumb"></span></span></label>`;
}

/** Role × item grid. Header and group rows offer select all / none per role; on phones each row stacks. */
function Matrix({ kind, items, policy, onSet }) {
  const key = kind === 'tools' ? 'tools' : 'resources';
  const id = (it) => (kind === 'tools' ? it.name : it.uri);
  const groups = kind === 'tools' ? GROUPS.map(([g, label, hint]) => ({ id: g, label, hint, items: items.filter((t) => t.minRole === g) })).filter((g) => g.items.length) : [{ id: 'all', label: '', items }];
  const has = (role, it) => policy[role][key].includes(id(it));
  const title = (it) => (kind === 'tools' ? it.title || it.name : it.name);
  const state = (role, list) => { const n = list.filter((it) => has(role, it)).length; return { all: n === list.length && n > 0, mixed: n > 0 && n < list.length }; };
  const setMany = (role, list, on) => onSet(role, key, list.map(id), on);
  return html`<div class="matrix" data-testid=${`matrix-${kind}`}>
    <table class="matrix-table">
      <colgroup><col /><col class="matrix-colw" /><col class="matrix-colw" /><col class="matrix-colw" /></colgroup>
      <thead><tr><th scope="col" class="matrix-th-item">${kind === 'tools' ? 'Tool' : 'Resource'}</th>
        ${ROLES.map((r) => { const st = state(r, items); return html`<th scope="col" class="matrix-col">
          <div class="matrix-role"><${RoleBadge} role=${r} /><${Switch} checked=${st.all} mixed=${st.mixed} label=${`Allow all ${kind} for ${ROLE_LABEL[r]}`} onChange=${(on) => setMany(r, items, on)} /></div></th>`; })}</tr></thead>
      ${groups.map((g) => html`<tbody key=${g.id}>
        ${g.label && html`<tr class="matrix-group"><th scope="rowgroup" class="matrix-group-title"><b>${g.label}</b><span>${g.hint}</span></th>
          ${ROLES.map((r) => { const st = state(r, g.items); return html`<td class="matrix-col"><${Switch} checked=${st.all} mixed=${st.mixed} label=${`Allow all ${g.label} tools for ${ROLE_LABEL[r]}`} onChange=${(on) => setMany(r, g.items, on)} /></td>`; })}</tr>`}
        ${g.items.map((it) => html`<tr key=${id(it)} class="matrix-row" data-item=${id(it)}>
          <th scope="row" class="matrix-item">
            <span class="matrix-name">${title(it)}${it.mutating && html`<span class="mut-tag" title="Changes data">mutating</span>`}</span>
            ${title(it) !== id(it) && html`<code class="matrix-id">${id(it)}</code>`}
            <span class="matrix-desc">${it.description}</span></th>
          ${ROLES.map((r) => html`<td class="matrix-col" data-label=${ROLE_LABEL[r]}><${Switch} checked=${has(r, it)} label=${`Allow ${id(it)} for ${ROLE_LABEL[r]}`} onChange=${(on) => onSet(r, key, [id(it)], on)} /></td>`)}
        </tr>`)}
      </tbody>`)}
    </table>
  </div>`;
}

export function McpTab() {
  const { data, error, reload, setData } = useFetch(() => api.mcp(), []);
  const [draft, setDraft] = useState(null);
  const [busy, setBusy] = useState('');
  useEffect(() => { if (data) setDraft(JSON.parse(JSON.stringify(data.policy))); }, [data]);
  const dirty = useMemo(() => data && draft && JSON.stringify(draft) !== JSON.stringify(data.policy), [data, draft]);
  if (error && !data) return html`<${ErrorBox} error=${error} onRetry=${() => reload()} />`;
  if (!data || !draft) return html`<div class="card card-pad stack">${[0, 1, 2].map((i) => html`<${Skeleton} key=${i} h=${44} />`)}</div>`;
  const endpoint = mcpEndpoint(data.endpoint);
  const toggleEnabled = async (v) => {
    setBusy('enable');
    setData((d) => ({ ...d, enabled: v }));
    try { setData(await api.saveMcp({ enabled: v, policy: data.policy })); toast({ title: v ? 'MCP server enabled' : 'MCP server disabled', message: v ? endpoint : undefined }); }
    catch (e) { setData((d) => ({ ...d, enabled: !v })); toastError(e, 'Could not update the MCP server'); }
    setBusy('');
  };
  const onSet = (role, key, ids, on) => setDraft((d) => {
    const n = JSON.parse(JSON.stringify(d));
    const set = new Set(n[role][key]);
    for (const x of ids) { if (on) set.add(x); else set.delete(x); }
    n[role][key] = [...set];
    return n;
  });
  const save = async () => {
    setBusy('save');
    try { setData(await api.saveMcp({ enabled: data.enabled, policy: draft })); toast({ title: 'Access policy saved' }); }
    catch (e) { toastError(e, 'Could not save the policy'); }
    setBusy('');
  };
  const reset = async () => {
    const ok = await confirmDialog({ title: 'Reset the access policy?', message: 'Every role gets back the default tools and resources for its level.', confirmLabel: 'Reset to defaults' });
    if (!ok) return;
    setBusy('reset');
    try { setData(await api.resetMcpPolicy()); toast({ title: 'Policy reset to defaults' }); } catch (e) { toastError(e, 'Reset failed'); }
    setBusy('');
  };
  const risky = data.tools.filter((t) => t.mutating && draft.viewer.tools.includes(t.name));
  return html`<div class="stack tab-panel" data-testid="mcp-tab">
    <section class="card">
      <div class="card-head"><h2><${Icon} name="robot" />MCP server</h2>
        <${Toggle} id="mcp-enabled" label=${data.enabled ? 'Enabled' : 'Disabled'} checked=${data.enabled} onChange=${toggleEnabled} /></div>
      <div class="card-body stack">
        <p class="muted">Lets AI assistants (Claude, IDE agents and other MCP clients) inspect and operate meilisearch-cdc over the Model Context Protocol (Streamable HTTP). Every call is checked against the token's role and written to the audit log.</p>
        ${!data.enabled && html`<${Alert} tone="info">The endpoint answers 404 until it is enabled.</${Alert}>`}
        <div class="grid grid-2" style="align-items:start">
          <${Field} label="Endpoint"><${CopyField} value=${endpoint} label="MCP endpoint URL" testid="mcp-endpoint" /></${Field}>
          <${Field} label="Client configuration" hint="Replace <token> with a token generated below."><${CopyField} value=${mcpSnippet(endpoint)} label="client configuration" multiline testid="mcp-snippet" /></${Field}>
        </div>
      </div>
    </section>

    <${TokensCard} endpoint=${endpoint} />

    <section class="card">
      <div class="card-head"><h2><${Icon} name="matrix" />Access by role</h2>
        <div class="btn-group">${dirty && html`<span class="tag tag-primary unsaved">unsaved changes</span>`}
          <${Button} icon="restart" loading=${busy === 'reset'} onClick=${reset} data-testid="mcp-reset">Reset to defaults</${Button}>
          <${Button} kind="primary" icon="check" loading=${busy === 'save'} disabled=${!dirty} onClick=${save} data-testid="mcp-save">Save</${Button}></div></div>
      <div class="card-body stack">
        ${risky.length > 0 && html`<${Alert} tone="warn" title="Viewers can change data" data-testid="mcp-risky">
          Granted to viewer: ${risky.map((t, i) => html`${i ? ', ' : ''}<code>${t.name}</code>`)}. Viewer tokens are often shared widely; prefer moderator for these.</${Alert}>`}
        <h3>Tools</h3>
        <${Matrix} kind="tools" items=${data.tools} policy=${draft} onSet=${onSet} />
        <h3 style="margin-top:8px">Resources</h3>
        <${Matrix} kind="resources" items=${data.resources} policy=${draft} onSet=${onSet} />
      </div>
      ${dirty && html`<div class="unsaved-bar" role="status" data-testid="mcp-unsaved"><span><b>Unsaved changes</b> to the access policy</span>
        <${Button} size="sm" kind="ghost" onClick=${() => setDraft(JSON.parse(JSON.stringify(data.policy)))}>Discard</${Button}>
        <${Button} size="sm" kind="primary" icon="check" loading=${busy === 'save'} onClick=${save}>Save changes</${Button}></div>`}
    </section>
  </div>`;
}

