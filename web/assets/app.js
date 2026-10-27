// meilisearch-cdc dashboard entry point: auth boot, app shell, navigation, user menu.
import { html, render, useEffect, useLayoutEffect, useRef, useState } from './vendor/htm-preact.js';
import { api, getDestination, getToken, setDestination, setToken } from './js/api.js';
import { hostOf, selectedDestination, singleDestination, useDestinations } from './js/destinations.js';
import { DestinationDetailPage, DestinationsPage, HealthDot } from './js/pages/destinations.js';
import { activeAlerts, connect, disconnect, seedActiveAlerts, useLive } from './js/sse.js';
import { PUBLIC_PAGES, currentHash, matchRoute, redirect, useRoute } from './js/router.js';
import { useFetch } from './js/hooks.js';
import { auth, can, initials, isApiToken, setAuth, useAuth } from './js/auth.js';
import { Icon, Logo } from './js/icons.js';
import { Button, DialogHost, Field, Modal, RoleBadge, Toaster, toast } from './js/ui.js';
import { cls, lsGet, lsSet } from './js/util.js';
import { useTheme, setTheme } from './js/theme.js';
import { promptInstall, registerServiceWorker, useInstallable } from './js/pwa.js';
import { AuthPage, PasswordField } from './js/pages/auth.js';
import { OnboardingPage, needsOnboarding, refreshConfig } from './js/pages/onboarding.js';
import { OverviewPage } from './js/pages/overview.js';
import { TasksPage } from './js/pages/tasks.js';
import { WizardPage } from './js/pages/wizard.js';
import { TaskDetailPage } from './js/pages/task-detail.js';
import { IndexesPage } from './js/pages/indexes.js';
import { IndexDetailPage } from './js/pages/index-detail.js';
import { MeiliTasksPage } from './js/pages/meili-tasks.js';
import { ConnectorDetailPage, ConnectorsPage } from './js/pages/connectors.js';
import { AlertsPage } from './js/pages/alerts.js';
import { SettingsPage } from './js/pages/settings.js';

const NAV = [
  { id: 'overview', label: 'Overview', icon: 'overview', href: '#/overview', match: ['overview'] },
  { id: 'tasks', label: 'Tasks', icon: 'tasks', href: '#/tasks', match: ['tasks', 'task', 'wizard'] },
  { id: 'connectors', label: 'Connectors', icon: 'plug', href: '#/connectors', match: ['connectors', 'connector'] },
  { id: 'destinations', label: 'Destinations', icon: 'database', href: '#/destinations', match: ['destinations', 'destination'] },
  { id: 'indexes', label: 'Indexes', icon: 'indexes', href: '#/indexes', match: ['indexes', 'index'] },
  { id: 'meili-tasks', label: 'Meilisearch tasks', icon: 'meili-tasks', href: '#/meili-tasks', match: ['meili-tasks'] },
  { id: 'alerts', label: 'Alerts', icon: 'bell', href: '#/alerts', match: ['alerts'] },
  { id: 'settings', label: 'Settings', icon: 'settings', href: '#/settings', match: ['settings'] },
];

function ThemeToggle() {
  const t = useTheme();
  const next = t === 'dark' ? 'light' : 'dark';
  return html`<button type="button" class="btn btn-ghost btn-icon theme-toggle" aria-label=${`Switch to ${next} theme`}
    title=${`Switch to ${next} theme`} data-testid="theme-toggle" onClick=${() => setTheme(next)}>
    <span class=${cls('theme-icon', t === 'dark' && 'is-dark')}><${Icon} name="sun" /><${Icon} name="moon" /></span>
  </button>`;
}

// ---------------------------------------------------------------- chrome
function HealthPill() {
  const lv = useLive();
  const dest = useDestinationId();
  const { data, error } = useFetch(() => api.health(), [dest], { interval: 15000 });
  // the SSE overview describes the default destination only
  const ov = !dest && lv.overview && lv.overview.meilisearch;
  const healthy = error ? false : ov ? ov.healthy : data ? data.meilisearch && data.meilisearch.status === 'available' : null;
  const version = (ov && ov.version) || (data && data.meilisearch && data.meilisearch.version);
  const isLive = lv.conn === 'live';
  // No destination yet (multi-instance mode before onboarding): not an outage.
  const unconfigured = !error && data && data.meilisearch && data.meilisearch.status === 'unconfigured';
  const tone = unconfigured || healthy == null ? 'muted' : healthy ? 'ok' : 'bad';
  const text = unconfigured ? 'No Meilisearch yet' : healthy == null ? 'Connecting…' : healthy ? `Meilisearch${version ? ` v${version}` : ''}` : 'Meilisearch unavailable';
  const inner = html`<span class="dot" aria-hidden="true"></span><span class="health-text">${text}</span>
    <span class=${cls('live-chip', isLive ? 'on' : 'off')}>${isLive ? 'Live' : lv.conn === 'connecting' ? '…' : 'Offline'}</span>`;
  if (unconfigured) {
    return html`<a class="health-pill tone-muted" href="#/destinations" data-testid="health-pill" title="Add a Meilisearch destination">${inner}</a>`;
  }
  return html`<div class=${cls('health-pill', `tone-${tone}`, isLive && healthy && 'is-live')} role="status" data-testid="health-pill"
      title=${isLive ? 'Live updates connected' : 'Live updates disconnected — retrying'}>${inner}</div>`;
}

function useDestinationId() {
  const [d, setD] = useState(getDestination());
  useEffect(() => { const on = () => setD(getDestination()); window.addEventListener('cdc:destination', on); return () => window.removeEventListener('cdc:destination', on); }, []);
  return d;
}

/** Top bar switcher: which Meilisearch instance the Indexes / Meilisearch pages work on. */
function DestinationSwitcher() {
  const list = useDestinations();
  useDestinationId();
  const [open, setOpen] = useState(false);
  const root = useRef(null);
  useEffect(() => {
    if (!open) return undefined;
    const onDoc = (e) => { if (root.current && !root.current.contains(e.target)) setOpen(false); };
    const onKey = (e) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onDoc); document.addEventListener('keydown', onKey);
    setTimeout(() => { const f = root.current && root.current.querySelector('[aria-checked="true"]'); f && f.focus(); }, 10);
    return () => { document.removeEventListener('mousedown', onDoc); document.removeEventListener('keydown', onKey); };
  }, [open]);
  if (singleDestination() || !list || !list.length) return null;
  const cur = selectedDestination(list);
  const pick = (d) => { setOpen(false); setDestination(d.isDefault ? '' : d.id); };
  return html`<div class="dest-switch" ref=${root}>
    <button type="button" class="dest-switch-btn" aria-haspopup="menu" aria-expanded=${open} onClick=${() => setOpen(!open)}
      aria-label=${`Meilisearch destination: ${cur ? cur.name : 'none'}`} data-testid="destination-switcher">
      <${HealthDot} healthy=${cur && cur.health ? cur.health.healthy : null} /><span class="dest-switch-name">${cur ? cur.name : 'Destination'}</span><${Icon} name="chevron-down" size=${14} />
    </button>
    ${open && html`<div class="menu dest-menu" role="menu" aria-label="Meilisearch destinations">
      ${list.map((d) => html`<button type="button" role="menuitemradio" aria-checked=${cur && cur.id === d.id} class="menu-item" onClick=${() => pick(d)} data-destination-option=${d.id}>
        <${HealthDot} healthy=${d.health ? d.health.healthy : null} /><span class="dest-opt"><b>${d.name}</b><small class="mono">${hostOf(d.url)}</small></span>
        ${d.isDefault && html`<span class="tag tag-primary">default</span>`}${cur && cur.id === d.id && html`<${Icon} name="check" size=${15} />`}</button>`)}
      <a class="menu-item" role="menuitem" href="#/destinations" onClick=${() => setOpen(false)}><${Icon} name="settings" size=${16} />Manage destinations</a>
    </div>`}
  </div>`;
}

function Sidebar({ route, collapsed, onCollapse, drawer, onNavigate }) {
  const lv = useLive();
  useAuth();
  const by = (lv.overview && lv.overview.tasks && lv.overview.tasks.byState) || {};
  const failing = (by.failed || 0) + (by.held || 0);
  const firing = activeAlerts();
  const critical = firing.some((e) => e.severity === 'critical');
  const conns = useFetch(() => api.connectors().catch(() => []), []);
  useEffect(() => {
    const on = () => conns.reload(true);
    window.addEventListener('cdc:connectors', on);
    return () => window.removeEventListener('cdc:connectors', on);
  }, []);
  const connCount = (conns.data || []).length;
  const dests = useDestinations();
  const destDown = (dests || []).filter((d) => d.health && !d.health.healthy).length;
  return html`<nav class=${cls('sidebar', drawer && 'is-open')} aria-label="Main" id="sidebar">
    <div class="sidebar-brand">
      <a href="#/overview" class="brand" aria-label="meilisearch-cdc home" onClick=${onNavigate}>
        <${Logo} size=${30} />
        <span class="brand-text"><b>Meili CDC</b><small>change data capture</small></span>
      </a>
    </div>
    <ul class="nav-list">
      ${NAV.map((n) => {
        const active = n.match.includes(route.page);
        const badge = n.id === 'tasks' ? failing : n.id === 'alerts' ? firing.length : n.id === 'connectors' ? connCount : 0;
        const label = n.id === 'tasks' ? `${badge} tasks need attention` : n.id === 'connectors' ? `${badge} connector${badge === 1 ? '' : 's'}` : `${badge} alert${badge === 1 ? '' : 's'} firing`;
        return html`<li key=${n.id}><a href=${n.href} class=${cls('nav-item', active && 'is-active')} aria-current=${active ? 'page' : undefined}
            title=${collapsed ? n.label : undefined} onClick=${onNavigate} data-nav=${n.id}>
          <${Icon} name=${n.icon} size=${19} /><span class="nav-label">${n.label}</span>
          ${n.id === 'destinations' && dests && dests.length > 0 && html`<span class="nav-dots" aria-label=${`${dests.length - destDown} of ${dests.length} destinations healthy`} data-testid="nav-dest-health">
            ${dests.slice(0, 5).map((d) => html`<span class=${`nav-dot ${d.health && d.health.healthy ? 'is-ok' : 'is-bad'}`}></span>`)}</span>`}
          ${badge > 0 && html`<span class=${cls('nav-badge', n.id === 'connectors' && 'is-count', n.id === 'alerts' && (critical ? 'is-critical' : 'is-warning'), n.id === 'alerts' && 'is-pulsing')}
            aria-label=${label} data-testid=${`nav-badge-${n.id}`}>${badge}</span>`}
        </a></li>`;
      })}
    </ul>
    <div class="sidebar-foot">
      ${can('task.create') && html`<a href="#/tasks/new" class="btn btn-primary sidebar-cta" onClick=${onNavigate} aria-label="New task"><${Icon} name="plus" /><span class="nav-label">New task</span></a>`}
      <button type="button" class="btn btn-ghost collapse-btn" onClick=${onCollapse} aria-label=${collapsed ? 'Expand sidebar' : 'Collapse sidebar'} aria-expanded=${!collapsed}>
        <${Icon} name=${collapsed ? 'chevron-right' : 'chevron-left'} /><span class="nav-label">Collapse</span>
      </button>
      ${auth.config && auth.config.version && html`<div class="sidebar-version nav-label" data-testid="app-version" title="meilisearch-cdc version">${auth.config.version}</div>`}
    </div>
  </nav>`;
}

// ---------------------------------------------------------------- user menu
function ProfileModal({ open, onClose }) {
  const { user } = useAuth();
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  useLayoutEffect(() => { if (open) { setName((user && user.name) || ''); setErr(''); } }, [open]);
  const save = async (e) => {
    e && e.preventDefault();
    setBusy(true);
    try { const r = await api.updateMe(name.trim()); setAuth({ user: r.user }); toast({ title: 'Profile updated' }); onClose(); }
    catch (er) { setErr(er.message); }
    setBusy(false);
  };
  return html`<${Modal} open=${open} onClose=${onClose} title="Profile" size="sm"
    footer=${html`<${Button} onClick=${onClose}>Cancel</${Button}><${Button} kind="primary" icon="check" loading=${busy} onClick=${save}>Save</${Button}>`}>
    <form class="stack" onSubmit=${save}>
      <div class="profile-head"><span class="avatar avatar-lg" aria-hidden="true">${initials(user)}</span>
        <div><b>${(user && user.email) || ''}</b><div><${RoleBadge} role=${user && user.role} /></div></div></div>
      <${Field} label="Display name" htmlFor="pf-name" error=${err}><input id="pf-name" class="input" value=${name} onInput=${(e) => setName(e.currentTarget.value)} autocomplete="name" /></${Field}>
      <button type="submit" hidden></button>
    </form>
  </${Modal}>`;
}

function ChangePasswordModal({ open, onClose }) {
  const [cur, setCur] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  useLayoutEffect(() => { if (open) { setCur(''); setPw(''); setPw2(''); setErr(''); } }, [open]);
  const valid = cur && pw.length >= 8 && pw.length <= 128 && pw === pw2;
  const save = async (e) => {
    e && e.preventDefault();
    if (!valid) return;
    setBusy(true);
    try { await api.changePassword(cur, pw); toast({ title: 'Password changed', message: 'Your other sessions were signed out.' }); onClose(); }
    catch (er) { setErr(er.message); }
    setBusy(false);
  };
  return html`<${Modal} open=${open} onClose=${onClose} title="Change password" size="sm"
    footer=${html`<${Button} onClick=${onClose}>Cancel</${Button}><${Button} kind="primary" icon="key" loading=${busy} disabled=${!valid} onClick=${save} data-testid="change-password-save">Change password</${Button}>`}>
    <form class="stack" onSubmit=${save}>
      ${err && html`<p class="field-error" role="alert">${err}</p>`}
      <${Field} label="Current password" htmlFor="cp-cur"><input id="cp-cur" type="password" class="input" autocomplete="current-password" value=${cur} onInput=${(e) => setCur(e.currentTarget.value)} /></${Field}>
      <${PasswordField} id="cp-new" label="New password" value=${pw} onInput=${setPw} />
      <${Field} label="Confirm new password" htmlFor="cp-new2" error=${pw2 && pw !== pw2 ? 'Passwords do not match' : ''}>
        <input id="cp-new2" type="password" class="input" autocomplete="new-password" value=${pw2} onInput=${(e) => setPw2(e.currentTarget.value)} /></${Field}>
      <button type="submit" hidden></button>
    </form>
  </${Modal}>`;
}

function UserMenu({ onSignOut }) {
  const { user } = useAuth();
  const installable = useInstallable();
  const [open, setOpen] = useState(false);
  const [modal, setModal] = useState('');
  const root = useRef(null);
  const btn = useRef(null);
  useEffect(() => {
    if (!open) return undefined;
    const onDoc = (e) => { if (root.current && !root.current.contains(e.target)) setOpen(false); };
    const onKey = (e) => { if (e.key === 'Escape') { setOpen(false); btn.current && btn.current.focus(); } };
    document.addEventListener('mousedown', onDoc);
    document.addEventListener('keydown', onKey);
    setTimeout(() => { const f = root.current && root.current.querySelector('[role=menuitem]'); f && f.focus(); }, 10);
    return () => { document.removeEventListener('mousedown', onDoc); document.removeEventListener('keydown', onKey); };
  }, [open]);
  if (!user) return null;
  const token = isApiToken();
  const onMenuKey = (e) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const items = [...root.current.querySelectorAll('[role=menuitem]')];
    const i = items.indexOf(document.activeElement);
    items[(i + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length].focus();
  };
  const pick = (m) => { setOpen(false); if (m === 'signout') onSignOut(); else if (m === 'install') promptInstall(); else setModal(m); };
  return html`<div class="user-menu" ref=${root}>
    <button type="button" class="user-btn" ref=${btn} aria-haspopup="menu" aria-expanded=${open} onClick=${() => setOpen(!open)}
      aria-label=${`Account: ${user.name || user.email || 'API token'}, ${user.role}`} data-testid="user-menu">
      <span class="avatar" aria-hidden="true">${token ? html`<${Icon} name="key" size=${15} />` : initials(user)}</span>
      <span class="user-btn-text"><b>${user.name || user.email || 'API token'}</b><small>${user.role}</small></span>
      <${Icon} name="chevron-down" size=${15} class="user-caret" />
    </button>
    ${open && html`<div class="menu" role="menu" aria-label="Account" onKeyDown=${onMenuKey}>
      <div class="menu-head"><span class="avatar avatar-lg" aria-hidden="true">${token ? html`<${Icon} name="key" size=${18} />` : initials(user)}</span>
        <div class="menu-head-text"><b>${user.name || 'API token'}</b>${user.email && html`<small>${user.email}</small>`}<span><${RoleBadge} role=${user.role} /></span></div></div>
      ${!token && html`<button type="button" role="menuitem" class="menu-item" onClick=${() => pick('profile')}><${Icon} name="user" size=${16} />Profile</button>
        <button type="button" role="menuitem" class="menu-item" onClick=${() => pick('password')}><${Icon} name="key" size=${16} />Change password</button>`}
      ${installable && html`<button type="button" role="menuitem" class="menu-item" onClick=${() => pick('install')} data-testid="install-app"><${Icon} name="download" size=${16} />Install app</button>`}
      <button type="button" role="menuitem" class="menu-item menu-danger" onClick=${() => pick('signout')}><${Icon} name="logout" size=${16} />Sign out</button>
    </div>`}
    <${ProfileModal} open=${modal === 'profile'} onClose=${() => setModal('')} />
    <${ChangePasswordModal} open=${modal === 'password'} onClose=${() => setModal('')} />
  </div>`;
}

// ---------------------------------------------------------------- pages
const TITLES = { destinations: 'Destinations', destination: 'Destination', welcome: 'Welcome', connectors: 'Connectors', connector: 'Connector', overview: 'Overview', tasks: 'Tasks', wizard: 'Task', task: 'Task', indexes: 'Indexes', index: 'Index', 'meili-tasks': 'Meilisearch tasks', alerts: 'Alerts', settings: 'Settings', setup: 'Set up', login: 'Sign in', forgot: 'Forgot password', reset: 'Reset password', invite: 'Accept invitation' };

function NotFound() {
  return html`<div class="page-center"><div class="empty"><div class="empty-icon"><${Icon} name="search" size=${26} /></div>
    <h1 class="empty-title">Page not found</h1><p>There is nothing at <code>${location.hash}</code>.</p><a class="btn btn-primary" href="#/overview">Go to overview</a></div></div>`;
}

function Page({ m }) {
  switch (m.page) {
    case 'overview': return html`<${OverviewPage} />`;
    case 'tasks': return html`<${TasksPage} />`;
    case 'wizard': return html`<${WizardPage} id=${m.id} preset=${m.connector ? { connectorId: m.connector, table: m.table, schema: m.schema, destinationId: m.presetDestination } : null} />`;
    case 'connectors': return html`<${ConnectorsPage} />`;
    case 'destinations': return html`<${DestinationsPage} />`;
    case 'destination': return html`<${DestinationDetailPage} id=${m.id} tab=${m.tab} />`;
    case 'connector': return html`<${ConnectorDetailPage} id=${m.id} />`;
    case 'task': return html`<${TaskDetailPage} id=${m.id} tab=${m.tab} />`;
    case 'indexes': return html`<${IndexesPage} instance=${m.instance} />`;
    case 'index': return html`<${IndexDetailPage} uid=${m.uid} tab=${m.tab} />`;
    case 'meili-tasks': return html`<${MeiliTasksPage} />`;
    case 'alerts': return html`<${AlertsPage} />`;
    case 'settings': return html`<${SettingsPage} tab=${m.tab} />`;
    default: return html`<${NotFound} />`;
  }
}

const loginHash = (cur = currentHash()) => {
  const page = matchRoute({ parts: cur.replace(/^#/, '').split('?')[0].split('/').filter(Boolean), query: new URLSearchParams() }).page;
  return PUBLIC_PAGES.has(page) ? '#/login' : `#/login?next=${encodeURIComponent(cur)}`;
};

function Shell({ m, reloadKey }) {
  const [collapsed, setCollapsed] = useState(lsGet('cdc.sidebar') === 'collapsed');
  const [drawer, setDrawer] = useState(false);
  useEffect(() => { setDrawer(false); }, [m.page, m.id, m.uid, m.tab]);
  useEffect(() => {
    if (!drawer) return undefined;
    const onKey = (e) => { if (e.key === 'Escape') setDrawer(false); };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [drawer]);
  const toggleCollapse = () => { const c = !collapsed; setCollapsed(c); lsSet('cdc.sidebar', c ? 'collapsed' : 'expanded'); };
  const signOut = async () => {
    try { await api.logout(); } catch { /* the session may already be gone */ }
    if (getToken()) setToken('');
    setAuth({ user: null });
    redirect('#/login');
    toast({ tone: 'info', title: 'Signed out' });
  };
  // tab changes inside the same page do not replay the page transition
  const pageKey = `${m.page}:${m.id || m.uid || ''}:${m.connector || ''}:${m.table || ''}:${reloadKey}`;
  return html`<div class=${cls('shell', collapsed && 'is-collapsed', drawer && 'drawer-open')}>
    <${Sidebar} route=${m} collapsed=${collapsed} onCollapse=${toggleCollapse} drawer=${drawer} onNavigate=${() => setDrawer(false)} />
    <div class="drawer-backdrop" onClick=${() => setDrawer(false)} aria-hidden="true"></div>
    <div class="main-col">
      <header class="topbar">
        <button type="button" class="btn btn-ghost btn-icon menu-btn" aria-label="Open navigation" aria-controls="sidebar"
          aria-expanded=${drawer} onClick=${() => setDrawer(true)} data-testid="menu-button"><${Icon} name="menu" /></button>
        <a href="#/overview" class="topbar-brand" aria-label="Home"><${Logo} size=${24} /></a>
        <div class="topbar-title">${TITLES[m.page] || ''}</div>
        <div class="topbar-right">
          <${DestinationSwitcher} />
          <${HealthPill} />
          <${ThemeToggle} />
          <${UserMenu} onSignOut=${signOut} />
        </div>
      </header>
      <main id="main" class="main" tabindex="-1">
        <div class="page" key=${pageKey}><${Page} m=${m} /></div>
      </main>
    </div>
  </div>`;
}

function App() {
  const route = useRoute();
  const m = matchRoute(route);
  const a = useAuth();
  const [bootError, setBootError] = useState(null);
  // While the server cannot be reached, retry by itself: right away when the
  // browser says it is back online (that event can come before the network
  // really works) and then every few seconds, backing off up to 30s.
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!bootError) { if (attempt) setAttempt(0); return undefined; }
    const retry = () => { setAttempt((n) => n + 1); boot(); };
    const timer = setTimeout(retry, Math.min(30_000, 2_000 * 2 ** Math.min(attempt, 4)));
    window.addEventListener('online', retry);
    return () => { clearTimeout(timer); window.removeEventListener('online', retry); };
  }, [bootError]);
  const [reloadKey, setReloadKey] = useState(0);

  const boot = async () => {
    setBootError(null);
    let config;
    try { config = await api.authConfig(); } catch (e) { setBootError(e); return; }
    let user = null;
    try { user = (await api.me()).user; } catch (e) { if (e.status !== 401) { setBootError(e); return; } }
    setAuth({ ready: true, config, user });
  };
  useEffect(() => { boot(); }, []);

  // live connection follows the session
  useEffect(() => {
    if (!a.user) { disconnect(); return; }
    connect();
    refreshConfig();
    api.activeAlerts().then(seedActiveAlerts).catch(() => {});
  }, [a.user && a.user.id]);

  useEffect(() => {
    const onAuth = () => {
      if (!auth.user) return;
      setAuth({ user: null });
      toast({ tone: 'warn', title: 'Session ended', message: 'Please sign in again.' });
      redirect(loginHash());
    };
    const onReload = () => setReloadKey((k) => k + 1);
    const onDest = (e) => { if (!(e.detail && e.detail.silent)) setReloadKey((k) => k + 1); };
    window.addEventListener('cdc:destination', onDest);
    const onAlert = (e) => {
      const ev = e.detail;
      if (ev.state !== 'firing') return;
      toast({ tone: ev.severity === 'critical' ? 'bad' : ev.severity === 'warning' ? 'warn' : 'info', title: `Alert: ${ev.ruleName}`, message: ev.message, timeout: 8000 });
    };
    window.addEventListener('cdc:auth-required', onAuth);
    window.addEventListener('cdc:reload', onReload);
    window.addEventListener('cdc:alert', onAlert);
    return () => {
      window.removeEventListener('cdc:auth-required', onAuth);
      window.removeEventListener('cdc:reload', onReload);
      window.removeEventListener('cdc:alert', onAlert);
      window.removeEventListener('cdc:destination', onDest);
    };
  }, []);

  // route guard
  const isPublic = PUBLIC_PAGES.has(m.page);
  let target = null;
  if (a.ready) {
    if (a.config.setupRequired) { if (m.page !== 'setup') target = '#/setup'; }
    else if (m.page === 'setup') target = a.user ? '#/overview' : '#/login';
    else if (!a.user && !isPublic) target = loginHash(route.hash);
    else if (a.user && m.page === 'login') target = m.next && !m.next.startsWith('#/login') ? m.next : '#/overview';
    else if (a.user && m.page === 'overview' && needsOnboarding(a) && !sessionStorage.getItem('cdc.onboarding.shown')) target = '#/welcome';
  }
  useEffect(() => {
    // a 401 handler may already have moved to the login page (keeping ?next=); do not overwrite it
    if (target && !(target.startsWith('#/login') && currentHash().startsWith('#/login'))) redirect(target);
  }, [target]);
  // ?destination=<id> on index pages selects that destination (links from tasks
  // and destinations). Applied before the page renders, so it loads once.
  if (m.destination && m.destination !== getDestination()) setDestination(m.destination, { silent: true });
  useEffect(() => { document.title = `${TITLES[m.page] || 'Not found'} · Meili CDC`; }, [m.page]);

  if (bootError) {
    return html`<div class="auth-screen"><div class="auth-card stack" data-testid="boot-error">
      <${Logo} size=${40} /><h1>Cannot reach the server</h1><p class="muted">${bootError.message}</p>
      ${!navigator.onLine && html`<p class="muted small">This device is offline; the dashboard reconnects by itself when the network is back.</p>`}
      <${Button} kind="primary" icon="restart" onClick=${boot}>Try again</${Button}></div></div>`;
  }
  if (!a.ready || target) return html`<div class="boot" aria-busy="true" aria-label="Loading dashboard"><span class="spinner"></span></div>`;
  return html`
    ${isPublic ? html`<${AuthPage} m=${m} config=${a.config} />` : m.page === 'welcome' ? html`<${OnboardingPage} />` : html`<${Shell} m=${m} reloadKey=${reloadKey} />`}
    <${Toaster} />
    <${DialogHost} />`;
}

registerServiceWorker();
render(html`<${App} />`, document.getElementById('app'));
