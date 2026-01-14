// Settings → Audit log (admin): who did what, newest first.
import { html, useMemo, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { useFetch } from '../hooks.js';
import { Icon } from '../icons.js';
import { Button, Empty, ErrorBox, SkeletonRows } from '../ui.js';
import { fmtTime, timeAgo } from '../util.js';

const tone = (a) => (/fail|delete|revoke/.test(a) ? 'bad' : /login|setup|accept/.test(a) ? 'ok' : /settings|role|password|mcp/.test(a) ? 'violet' : 'neutral');

export function AuditTab() {
  const { data, error, loading, reload } = useFetch(() => api.audit(500), []);
  const [action, setAction] = useState('');
  const [user, setUser] = useState('');
  const [q, setQ] = useState('');
  const actions = useMemo(() => [...new Set((data || []).map((e) => e.action))].sort(), [data]);
  const users = useMemo(() => [...new Set((data || []).map((e) => e.userEmail).filter(Boolean))].sort(), [data]);
  const rows = useMemo(() => (data || []).filter((e) => (!action || e.action === action) && (!user || e.userEmail === user)
    && (!q || `${e.target} ${e.details} ${e.ip}`.toLowerCase().includes(q.toLowerCase()))), [data, action, user, q]);
  return html`<section class="card tab-panel" data-testid="audit-tab">
    <div class="card-head"><h2><${Icon} name="history" />Audit log</h2>
      <${Button} size="sm" kind="ghost" icon="restart" loading=${loading} onClick=${() => reload()} aria-label="Refresh audit log">Refresh</${Button}></div>
    <div class="card-body toolbar" style="margin:0;padding-bottom:0">
      <select class="select" aria-label="Filter by action" value=${action} onChange=${(e) => setAction(e.currentTarget.value)} data-testid="audit-action">
        <option value="">All actions</option>${actions.map((a) => html`<option value=${a}>${a}</option>`)}</select>
      <select class="select" aria-label="Filter by user" value=${user} onChange=${(e) => setUser(e.currentTarget.value)} data-testid="audit-user">
        <option value="">All users</option>${users.map((u) => html`<option value=${u}>${u}</option>`)}</select>
      <div class="input-icon"><${Icon} name="search" size=${16} /><input class="input" type="search" placeholder="Target, details or IP" aria-label="Search audit log" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} /></div>
    </div>
    <${ErrorBox} error=${error && !data && error} onRetry=${() => reload()} />
    ${!data ? html`<${SkeletonRows} rows=${6} cols=${5} />`
      : !rows.length ? html`<${Empty} icon="history" title="No matching entries">Logins, user and settings changes, rule edits and task or index mutations are recorded here.</${Empty}>`
      : html`<div class="table-wrap"><table class="table table-compact rtable" data-testid="audit-table">
        <thead><tr><th>When</th><th>User</th><th>Action</th><th>Target</th><th class="hide-sm">Details</th><th class="hide-sm">IP</th></tr></thead>
        <tbody>${rows.map((e, i) => html`<tr key=${`${e.at}${i}`} style=${`--i:${Math.min(i, 12)}`}>
          <td data-label="When" class="nowrap" title=${fmtTime(e.at, true)}>${timeAgo(e.at)}</td>
          <td data-label="User">${e.userEmail || html`<span class="subtle">anonymous</span>`}</td>
          <td data-label="Action"><span class=${`tag tag-${tone(e.action)} mono`}>${e.action}</span></td>
          <td data-label="Target" class="mono audit-target">${e.target}</td>
          <td data-label="Details" class="hide-sm subtle">${e.details}</td>
          <td data-label="IP" class="hide-sm mono subtle">${e.ip}</td>
        </tr>`)}</tbody></table></div>
        <p class="subtle" style="padding:10px 16px;font-size:12.5px">${rows.length} of ${data.length} entries</p>`}
  </section>`;
}
