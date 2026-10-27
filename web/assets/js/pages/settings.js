// Settings: tabbed. General for everyone; Users, Email, Telegram, MCP and Audit log for admins.
import { html, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { HealthDot } from './destinations.js';
import { useDestinations } from '../destinations.js';
import { can, useAuth } from '../auth.js';
import { useLive } from '../sse.js';
import { useFetch } from '../hooks.js';
import { navigate } from '../router.js';
import { setTheme, useTheme } from '../theme.js';
import { Icon, Logo } from '../icons.js';
import { Alert, Button, ErrorBox, Forbidden, PageHeader, Skeleton, Tabs, Toggle, toast, toastError } from '../ui.js';
import { fmtBytes, fmtInt, lsSet, timeAgo } from '../util.js';
import { UsersTab } from './settings-users.js';
import { EmailTab, TelegramTab } from './settings-notify.js';
import { McpTab } from './settings-mcp.js';
import { AuditTab } from './settings-audit.js';
import { UPDATE_HOURS, checkForUpdate, setUpdatePrefs, useUpdate } from '../updates.js';

function AppearanceCard() {
  const t = useTheme();
  return html`<section class="card"><div class="card-head"><h2><${Icon} name="sun" />Appearance</h2></div>
    <div class="card-body stack">
      <div class="seg" role="group" aria-label="Theme">
        ${['light', 'dark'].map((x) => html`<button type="button" aria-pressed=${t === x} onClick=${() => setTheme(x)}>${x === 'light' ? 'Light' : 'Dark'}</button>`)}
        <button type="button" aria-pressed="false" onClick=${() => { setTheme(matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'); lsSet('cdc.theme', null); }}>Match system</button>
      </div>
      <p class="subtle" style="font-size:12.5px">Animations follow your system's “reduce motion” preference.</p>
    </div></section>`;
}

function ServiceCard() {
  const lv = useLive();
  const health = useFetch(() => api.health(), []);
  return html`<section class="card" style="overflow:hidden">
    <div class="about-hero"><${Logo} size=${48} /><div><h2>meilisearch-cdc</h2><p class="muted">Change data capture from databases into Meilisearch.</p></div></div>
    <dl class="kv card-body">
      <dt>Server</dt><dd>${health.data ? html`<span class="tag tag-ok">${health.data.status}</span>` : health.error ? html`<span class="tag tag-bad">unreachable</span>` : '…'}</dd>
      <dt>Live events</dt><dd>${lv.conn}</dd>
      <dt>Metrics</dt><dd><a href="/metrics" target="_blank" rel="noopener">/metrics</a> (Prometheus)</dd>
      <dt>API</dt><dd class="mono">/api/v1</dd>
    </dl></section>`;
}

/** Compact list of Meilisearch instances; per-instance actions live on each destination page. */
function InstancesCard() {
  const dests = useDestinations();
  return html`<section class="card" data-testid="instances-summary"><div class="card-head"><h2><${Icon} name="database" />Meilisearch instances</h2>
      <a class="btn btn-ghost btn-sm" href="#/destinations"><span>Destinations</span><${Icon} name="arrow-right" size=${14} /></a></div>
    ${!dests ? html`<div class="card-body"><${Skeleton} h=${40} /></div>`
      : !dests.length ? html`<p class="card-body muted">No Meilisearch instance yet. <a href="#/destinations">Add one</a>.</p>`
      : html`<ul class="instances-list">${dests.map((d) => html`<li key=${d.id}><${HealthDot} healthy=${d.health ? d.health.healthy : null} />
          <a href=${`#/destinations/${encodeURIComponent(d.id)}`}>${d.name}</a>${d.isDefault && html`<span class="tag tag-primary">default</span>`}
          <span class="subtle mono" style="margin-left:auto;font-size:12px">${d.health && d.health.version ? `v${d.health.version}` : d.health && !d.health.healthy ? 'unreachable' : ''}</span></li>`)}</ul>`}
    <p class="subtle" style="padding:0 20px 16px;font-size:12.5px">Backups, experimental features and the task queue are managed per instance on each destination page.</p>
  </section>`;
}

function UpdatesCard() {
  const u = useUpdate();
  const [busy, setBusy] = useState(false);
  const check = async () => {
    setBusy(true);
    try {
      const l = await checkForUpdate();
      toast(l ? { tone: 'info', title: `Version ${l.tag} is available` } : { tone: 'ok', title: 'You are up to date' });
    } catch (e) { toastError(e, 'Update check failed'); } finally { setBusy(false); }
  };
  return html`<section class="card" data-testid="updates-card"><div class="card-head"><h2><${Icon} name="restart" />Updates</h2></div>
    <div class="card-body stack">
      <${Toggle} label="Check for updates in the background" hint="Asks api.github.com for the latest release while the dashboard is open. Stored in this browser."
        checked=${u.prefs.enabled} onChange=${(v) => setUpdatePrefs({ enabled: v })} id="upd-enabled" />
      <label class="field"><span class="field-label">Check every</span>
        <select class="select" aria-label="Update check frequency" value=${u.prefs.hours} disabled=${!u.prefs.enabled} data-testid="update-hours"
          onChange=${(e) => setUpdatePrefs({ hours: Number(e.currentTarget.value) })}>
          ${UPDATE_HOURS.map((h) => html`<option value=${h}>${h === 1 ? 'hour' : `${h} hours`}</option>`)}</select></label>
      ${u.latest && html`<${Alert} tone="info" title=${`Version ${u.latest.tag} is available`}><a href=${u.latest.url} target="_blank" rel="noopener">View release</a></${Alert}>`}
      <div><${Button} icon="restart" loading=${busy} onClick=${check}>Check now</${Button}>
        ${u.prefs.last > 0 && html`<span class="subtle" style="margin-left:10px;font-size:12.5px">Last checked ${timeAgo(new Date(u.prefs.last).toISOString())}</span>`}</div>
    </div></section>`;
}

function GeneralTab() {
  return html`<div class="tab-panel">
    <div class="grid grid-2 stagger" style="align-items:start">
      <div class="stack"><${AppearanceCard} /><${InstancesCard} /></div>
      <div class="stack"><${ServiceCard} /><${UpdatesCard} /></div>
    </div>
  </div>`;
}

const TABS = [
  { id: 'general', label: 'General', icon: 'settings' },
  { id: 'users', label: 'Users', icon: 'users', perm: 'users' },
  { id: 'email', label: 'Email', icon: 'mail', perm: 'settings.notify' },
  { id: 'telegram', label: 'Telegram', icon: 'telegram', perm: 'settings.notify' },
  { id: 'mcp', label: 'MCP', icon: 'robot', perm: 'settings.mcp' },
  { id: 'audit', label: 'Audit log', icon: 'history', perm: 'audit' },
];

export function SettingsPage({ tab = 'general' }) {
  useAuth();
  const tabs = TABS.filter((t) => !t.perm || can(t.perm));
  const known = TABS.find((t) => t.id === tab);
  const allowed = tabs.some((t) => t.id === tab);
  const Panel = { general: GeneralTab, users: UsersTab, email: EmailTab, telegram: TelegramTab, mcp: McpTab, audit: AuditTab }[known ? tab : 'general'];
  return html`
    <${PageHeader} title="Settings" subtitle=${can('users') ? 'Instance controls, team access, notifications and AI management.' : 'Dashboard preferences and Meilisearch instance information.'} />
    ${tabs.length > 1 && html`<${Tabs} tabs=${tabs} active=${known ? tab : 'general'} label="Settings sections" onChange=${(t) => navigate(`#/settings${t === 'general' ? '' : `/${t}`}`)} />`}
    <div role="tabpanel" aria-labelledby=${`tab-${known ? tab : 'general'}`} key=${tab}>
      ${allowed || !known ? html`<${Panel} />` : html`<${Forbidden} role="admin" what=${`the ${known.label} settings`} />`}
    </div>`;
}
