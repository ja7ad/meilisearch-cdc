// Alerts: firing alerts, rules (CRUD, enable, test) and the event history timeline.
import { html, useEffect, useLayoutEffect, useMemo, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { can } from '../auth.js';
import { useDestinations } from '../destinations.js';
import { activeAlerts, useLive } from '../sse.js';
import { useFetch, useTick } from '../hooks.js';
import { Icon } from '../icons.js';
import { Alert, Button, ChipsInput, Empty, ErrorBox, Field, Modal, PageHeader, SeverityBadge, Skeleton, Tag, Toggle, confirmDialog, sevTone, toast, toastError } from '../ui.js';
import { SecretInput, secretError } from '../signing.js';
import { cls, fmtLag, fmtTime, isDuration, stateInfo, timeAgo } from '../util.js';

export const ALERT_TYPES = [
  { id: 'task_state', label: 'Task state', hint: 'A task enters one of the selected states.' },
  { id: 'lag', label: 'Replication lag', hint: 'Lag stays above a threshold.' },
  { id: 'dlq', label: 'Dead letters', hint: 'New dead letters within a time window.' },
  { id: 'errors', label: 'Errors', hint: 'Errors within a time window.' },
  { id: 'stalled', label: 'Stalled stream', hint: 'A streaming task receives nothing while behind.' },
  { id: 'meili_down', label: 'Meilisearch down', hint: 'Meilisearch is unreachable or unhealthy.' },
];
const ALERT_STATES = ['failed', 'held', 'retrying', 'paused', 'stopped'];
const plural = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;

export function conditionText(c = {}) {
  const forTxt = c.for && c.for !== '0s' ? ` for ${c.for}` : '';
  switch (c.type) {
    case 'task_state': return `Task ${(c.states || []).join('/') || '?'}${forTxt}`;
    case 'lag': return `Lag over ${fmtLag(c.thresholdMs || 0)}${forTxt}`;
    case 'dlq': return `${c.count || 1}+ dead letter${c.count === 1 ? '' : 's'} in ${c.window || '?'}`;
    case 'errors': return `${c.count || 1}+ error${c.count === 1 ? '' : 's'} in ${c.window || '?'}`;
    case 'stalled': return `Stream stalled${forTxt}`;
    case 'meili_down': return `Meilisearch down${forTxt}`;
    default: return c.type || 'Unknown condition';
  }
}
export function ruleSummary(r, taskName = (id) => id, destName = (id) => id) {
  const scope = r.condition && r.condition.type === 'meili_down'
    ? ((r.destinationIds || []).length ? ` on ${r.destinationIds.map(destName).join(', ')}` : ' on all destinations')
    : (r.taskIds || []).length ? ` on ${r.taskIds.map(taskName).join(', ')}` : ' on all tasks';
  const ch = r.channels || {};
  const parts = [];
  if ((ch.emails || []).length) parts.push(plural(ch.emails.length, 'email'));
  if ((ch.telegramChatIds || []).length) parts.push(plural(ch.telegramChatIds.length, 'Telegram chat'));
  if (ch.webhookUrl) parts.push(ch.webhookSecret ? 'signed webhook' : 'webhook');
  return `${conditionText(r.condition)}${scope} · notify ${parts.join(', ') || 'nobody'}`;
}

// ---------------------------------------------------------------- active
export function ActiveAlertCard({ e, i = 0 }) {
  return html`<article class=${`card alert-card tone-${sevTone(e.severity)}`} style=${`--i:${i}`} data-testid="active-alert">
    <div class="alert-card-top"><span class="alert-pulse" aria-hidden="true"></span><${SeverityBadge} severity=${e.severity} />
      <span class="subtle nowrap" title=${fmtTime(e.at, true)}>since ${timeAgo(e.at)}</span></div>
    <h3>${e.ruleName}</h3>
    <p class="alert-card-msg">${e.message}</p>
    ${e.taskId && html`<a class="alert-card-task" href=${`#/tasks/${encodeURIComponent(e.taskId)}`}><${Icon} name="tasks" size=${14} />${e.taskName || e.taskId}</a>`}
    ${e.destinationId && html`<a class="alert-card-task" href=${`#/destinations/${encodeURIComponent(e.destinationId)}`}><${Icon} name="database" size=${14} />${e.destinationName || e.destinationId}</a>`}
  </article>`;
}

// ---------------------------------------------------------------- rule editor
const blankRule = () => ({
  destinationIds: [],
  name: '', enabled: true, condition: { type: 'task_state', states: ['failed', 'held'], thresholdMs: 60000, count: 1, window: '5m', for: '1m' },
  taskIds: [], severity: 'warning', channels: { emails: [], telegramChatIds: [], webhookUrl: '', webhookSecret: '' }, cooldown: '30m', notifyResolved: true,
});

/** Only the fields that belong to the condition type. */
function cleanCondition(c) {
  const out = { type: c.type };
  if (c.type === 'task_state') out.states = c.states;
  if (c.type === 'lag') out.thresholdMs = Number(c.thresholdMs) || 0;
  if (c.type === 'dlq' || c.type === 'errors') { out.count = Number(c.count) || 0; out.window = c.window; }
  out.for = c.for || '0s';
  return out;
}

function validateRule(r) {
  const e = {};
  const c = r.condition;
  if (!r.name.trim()) e.name = 'Give the rule a name';
  if (c.type === 'task_state' && !c.states.length) e.states = 'Pick at least one state';
  if (c.type === 'lag' && !(Number(c.thresholdMs) > 0)) e.threshold = 'Must be greater than 0';
  if ((c.type === 'dlq' || c.type === 'errors') && !(Number(c.count) > 0)) e.count = 'Must be at least 1';
  if ((c.type === 'dlq' || c.type === 'errors') && (!c.window || !isDuration(c.window))) e.window = 'Use a duration such as 5m';
  if (c.for && !isDuration(c.for)) e.for = 'Use a duration such as 1m';
  if (r.cooldown && !isDuration(r.cooldown)) e.cooldown = 'Use a duration such as 30m';
  if (r.channels.webhookUrl && !/^https?:\/\/\S+$/.test(r.channels.webhookUrl)) e.webhook = 'Use an http(s) URL';
  if (r.channels.webhookUrl && secretError(r.channels.webhookSecret)) e.webhookSecret = secretError(r.channels.webhookSecret);
  if (!r.channels.emails.length && !r.channels.telegramChatIds.length && !r.channels.webhookUrl) e.channels = 'Add at least one recipient: an email, a Telegram chat or a webhook';
  return e;
}

function UnavailableChannel({ name, href }) {
  return html`<div class="channel-off" data-testid=${`channel-off-${name.toLowerCase()}`}><${Icon} name="lock" size=${14} /><span><b>${name}</b> — not configured</span><a href=${href}>Configure</a></div>`;
}

function RuleEditor({ open, rule, tasks, dests, channels, onClose, onSaved }) {
  const [r, setR] = useState(blankRule());
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [serverErr, setServerErr] = useState(null);
  const ch = channels || { email: true, telegram: true, webhook: true };
  useLayoutEffect(() => {
    if (!open) return;
    const b = blankRule();
    setR(rule ? { ...b, ...JSON.parse(JSON.stringify(rule)), condition: { ...b.condition, ...rule.condition }, channels: { ...b.channels, ...rule.channels } } : b);
    setTried(false); setServerErr(null); setBusy(false);
  }, [open, rule && rule.id]);
  const set = (fn) => setR((prev) => { const d = JSON.parse(JSON.stringify(prev)); fn(d); return d; });
  const errs = validateRule(r);
  if (!ch.email && r.channels.emails.length) errs.channels = 'Email is not configured: remove the email recipients or configure SMTP';
  if (!ch.telegram && r.channels.telegramChatIds.length) errs.channels = 'Telegram is not configured: remove the chat ids or connect a bot';
  const show = (k) => (tried ? errs[k] : '');
  const c = r.condition;
  const save = async () => {
    setTried(true);
    if (Object.keys(errs).length) return;
    setBusy(true); setServerErr(null);
    const body = { name: r.name.trim(), enabled: r.enabled, condition: cleanCondition(c), taskIds: c.type === 'meili_down' ? [] : r.taskIds, severity: r.severity,
      channels: { ...r.channels, webhookSecret: r.channels.webhookUrl.trim() ? r.channels.webhookSecret || '' : '' }, cooldown: r.cooldown, notifyResolved: r.notifyResolved };
    if (c.type === 'meili_down' && (r.destinationIds || []).length) body.destinationIds = r.destinationIds;
    try {
      const saved = rule ? await api.updateRule(rule.id, body) : await api.createRule(body);
      toast({ title: rule ? 'Rule saved' : 'Rule created', message: saved.name });
      onSaved(saved); onClose();
    } catch (e) { setServerErr(e); setBusy(false); }
  };
  const emailOk = (v) => (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v) ? '' : `“${v}” is not an email address`);
  const chatOk = (v) => (/^-?\d+$|^@\w{4,}$/.test(v) ? '' : `“${v}” is not a chat id (a number such as -1001234567890, or @channel)`);
  return html`<${Modal} open=${open} onClose=${onClose} title=${rule ? 'Edit alert rule' : 'New alert rule'} size="lg"
    footer=${html`<${Button} onClick=${onClose}>Cancel</${Button}><${Button} kind="primary" icon="check" loading=${busy} onClick=${save} data-testid="rule-save">${rule ? 'Save rule' : 'Create rule'}</${Button}>`}>
    <div class="stack rule-editor">
      <${ErrorBox} error=${serverErr} />
      <div class="form-grid">
        <${Field} label="Name" htmlFor="ar-name" error=${show('name')} class="span-2"><input id="ar-name" class="input" value=${r.name} placeholder="Task failed or held" onInput=${(e) => { const v = e.currentTarget.value; set((d) => { d.name = v; }); }} /></${Field}>
        <${Field} label="Condition" htmlFor="ar-type" hint=${(ALERT_TYPES.find((t) => t.id === c.type) || {}).hint}>
          <select id="ar-type" class="select" value=${c.type} onChange=${(e) => { const v = e.currentTarget.value; set((d) => { d.condition.type = v; }); }}>
            ${ALERT_TYPES.map((t) => html`<option value=${t.id}>${t.label}</option>`)}</select></${Field}>
        <${Field} label="For" htmlFor="ar-for" error=${show('for')} hint="How long the condition must hold before firing.">
          <input id="ar-for" class="input mono" value=${c.for} placeholder="1m" onInput=${(e) => { const v = e.currentTarget.value; set((d) => { d.condition.for = v; }); }} /></${Field}>
      </div>
      ${c.type === 'task_state' && html`<${Field} label="States" error=${show('states')}>
        <div class="chips" role="group" aria-label="States">${ALERT_STATES.map((s) => html`<button type="button" class=${`chip tone-${stateInfo(s).tone}`} aria-pressed=${c.states.includes(s)} data-chip=${s}
          onClick=${() => set((d) => { const a = d.condition.states; const i = a.indexOf(s); i >= 0 ? a.splice(i, 1) : a.push(s); })}><span class="legend-dot tone-bg-${stateInfo(s).tone}"></span>${s}</button>`)}</div></${Field}>`}
      ${c.type === 'lag' && html`<${Field} label="Lag threshold (seconds)" htmlFor="ar-thr" error=${show('threshold')}>
        <input id="ar-thr" class="input" type="number" min="1" inputmode="numeric" value=${Math.round((c.thresholdMs || 0) / 1000)} onInput=${(e) => { const v = Number(e.currentTarget.value) * 1000; set((d) => { d.condition.thresholdMs = v; }); }} /></${Field}>`}
      ${(c.type === 'dlq' || c.type === 'errors') && html`<div class="form-grid">
        <${Field} label=${c.type === 'dlq' ? 'New dead letters' : 'Errors'} htmlFor="ar-count" error=${show('count')}><input id="ar-count" class="input" type="number" min="1" inputmode="numeric" value=${c.count} onInput=${(e) => { const v = Number(e.currentTarget.value); set((d) => { d.condition.count = v; }); }} /></${Field}>
        <${Field} label="Within" htmlFor="ar-win" error=${show('window')}><input id="ar-win" class="input mono" value=${c.window} placeholder="5m" onInput=${(e) => { const v = e.currentTarget.value; set((d) => { d.condition.window = v; }); }} /></${Field}>
      </div>`}
      ${c.type === 'meili_down' && html`<${Field} label="Destinations">
        <${Toggle} id="ar-alldest" label="All destinations" hint="Including destinations added later." checked=${!(r.destinationIds || []).length} onChange=${(v) => set((d) => { d.destinationIds = v ? [] : (dests[0] ? [dests[0].id] : []); })} />
        ${(r.destinationIds || []).length > 0 && html`<div class="chips" role="group" aria-label="Destinations" style="margin-top:10px">${dests.map((d) => html`<button type="button" class="chip" aria-pressed=${r.destinationIds.includes(d.id)} data-chip=${d.id}
          onClick=${() => set((x) => { const a = x.destinationIds; const i = a.indexOf(d.id); i >= 0 ? a.splice(i, 1) : a.push(d.id); })}>${d.name}</button>`)}</div>`}
      </${Field}>`}
      ${c.type !== 'meili_down' && html`<${Field} label="Tasks">
        <${Toggle} id="ar-all" label="All tasks" hint="Including tasks created later." checked=${!r.taskIds.length} onChange=${(v) => set((d) => { d.taskIds = v ? [] : (tasks[0] ? [tasks[0].id] : []); })} />
        ${r.taskIds.length > 0 && html`<div class="chips" role="group" aria-label="Tasks" style="margin-top:10px">${tasks.map((t) => html`<button type="button" class="chip" aria-pressed=${r.taskIds.includes(t.id)} data-chip=${t.id}
          onClick=${() => set((d) => { const a = d.taskIds; const i = a.indexOf(t.id); i >= 0 ? a.splice(i, 1) : a.push(t.id); })}>${t.name || t.id}</button>`)}</div>`}
      </${Field}>`}
      <${Field} label="Severity">
        <div class="radio-cards sev-cards" role="radiogroup" aria-label="Severity">
          ${[['info', 'Info', 'Good to know.'], ['warning', 'Warning', 'Needs attention soon.'], ['critical', 'Critical', 'Act now: data is not flowing.']].map(([v, l, d]) => html`
            <label class=${`radio-card tone-${sevTone(v)}`}><input type="radio" name="severity" value=${v} checked=${r.severity === v} onChange=${() => set((x) => { x.severity = v; })} />
              <b><span class="legend-dot" style="background:var(--c)"></span>${l}</b><small>${d}</small></label>`)}
        </div></${Field}>
      <div class="form-section">
        <h3><${Icon} name="send" />Channels</h3>
        ${show('channels') && html`<p class="field-error" role="alert">${show('channels')}</p>`}
        ${ch.email ? html`<${Field} label="Emails" hint="Press Enter after each address.">
          <${ChipsInput} id="ar-emails" label="Email recipients" values=${r.channels.emails} onChange=${(v) => set((d) => { d.channels.emails = v; })} placeholder="ops@example.com" validate=${emailOk} inputMode="email" />
        </${Field}>` : html`<${UnavailableChannel} name="Email" href="#/settings/email" />
          ${r.channels.emails.length > 0 && html`<p class="field-warn">This rule still lists ${r.channels.emails.length} email recipient${r.channels.emails.length > 1 ? 's' : ''}; they cannot be delivered. <button type="button" class="link-btn" onClick=${() => set((d) => { d.channels.emails = []; })}>Remove them</button></p>`}`}
        ${ch.telegram ? html`<${Field} label="Telegram chat ids">
          <${ChipsInput} id="ar-tg" label="Telegram chat ids" values=${r.channels.telegramChatIds} onChange=${(v) => set((d) => { d.channels.telegramChatIds = v; })} placeholder="-1001234567890" validate=${chatOk} />
        </${Field}>` : html`<${UnavailableChannel} name="Telegram" href="#/settings/telegram" />
          ${r.channels.telegramChatIds.length > 0 && html`<p class="field-warn" data-testid="telegram-warning">This rule still lists Telegram chats; they cannot be delivered. <button type="button" class="link-btn" onClick=${() => set((d) => { d.channels.telegramChatIds = []; })}>Remove them</button></p>`}`}
        <${Field} label="Webhook URL" htmlFor="ar-hook" error=${show('webhook')} hint="Receives the AlertEvent as JSON (POST).">
          <input id="ar-hook" class="input mono" type="url" inputmode="url" value=${r.channels.webhookUrl} placeholder="https://hooks.example.com/cdc" onInput=${(e) => { const v = e.currentTarget.value; set((d) => { d.channels.webhookUrl = v; }); }} /></${Field}>
        ${r.channels.webhookUrl.trim() && html`<${Field} label="Signing secret" htmlFor="ar-hook-secret" error=${show('webhookSecret')}
          hint=${html`Optional. Each delivery carries <code>X-CDC-Timestamp</code> and <code>X-CDC-Signature: sha256=…</code> (HMAC-SHA256 of timestamp + "." + body), so your endpoint can reject requests that did not come from this service.`}>
          <${SecretInput} id="ar-hook-secret" value=${r.channels.webhookSecret || ''} error=${show('webhookSecret')} onChange=${(v) => set((d) => { d.channels.webhookSecret = v; })} testid="alert-webhook-secret" /></${Field}>`}
      </div>
      <div class="form-grid">
        <${Field} label="Cooldown" htmlFor="ar-cool" error=${show('cooldown')} hint="Minimum time between repeated notifications while firing.">
          <input id="ar-cool" class="input mono" value=${r.cooldown} placeholder="30m" onInput=${(e) => { const v = e.currentTarget.value; set((d) => { d.cooldown = v; }); }} /></${Field}>
        <div class="stack-sm" style="padding-top:22px">
          <${Toggle} id="ar-resolved" label="Notify when resolved" checked=${r.notifyResolved} onChange=${(v) => set((d) => { d.notifyResolved = v; })} />
          <${Toggle} id="ar-enabled" label="Enabled" checked=${r.enabled} onChange=${(v) => set((d) => { d.enabled = v; })} />
        </div>
      </div>
    </div>
  </${Modal}>`;
}

// ---------------------------------------------------------------- rules
function DeliveryList({ items }) {
  return html`<ul class="deliveries">${items.map((d) => html`<li class=${d.ok ? 'is-ok' : 'is-bad'} title=${d.error || 'Delivered'}>
    <${Icon} name=${d.ok ? 'success' : 'error'} size=${14} /><span class="dl-ch">${d.channel}</span><span class="mono dl-target">${d.target}</span>
    ${!d.ok && d.error && html`<span class="dl-err">${d.error}</span>`}</li>`)}</ul>`;
}

function RuleCard({ r, i, taskName, destName, firing, onEdit, onChanged, channels }) {
  const ch = channels || { email: true, telegram: true };
  const undeliverable = [...((r.channels.emails || []).length && !ch.email ? ['Email not configured'] : []), ...((r.channels.telegramChatIds || []).length && !ch.telegram ? ['Telegram not configured'] : [])];
  const [busy, setBusy] = useState('');
  const [results, setResults] = useState(null);
  const admin = can('alerts.write');
  const [enabled, setEnabled] = useState(r.enabled);
  useEffect(() => setEnabled(r.enabled), [r.enabled]);
  const toggle = async (v) => {
    setBusy('toggle'); setEnabled(v);
    try { await api.updateRule(r.id, { ...r, enabled: v }); toast({ title: `${r.name} ${v ? 'enabled' : 'disabled'}` }); onChanged(); }
    catch (e) { setEnabled(!v); toastError(e, 'Could not update rule'); }
    setBusy('');
  };
  const test = async () => {
    setBusy('test');
    try {
      const res = await api.testRule(r.id);
      setResults(res.results || []);
      const bad = (res.results || []).filter((x) => !x.ok).length;
      toast({ tone: bad ? 'warn' : 'ok', title: bad ? `Test sent with ${bad} failure${bad > 1 ? 's' : ''}` : 'Test notification sent' });
    } catch (e) { toastError(e, 'Test failed'); }
    setBusy('');
  };
  const del = async () => {
    const ok = await confirmDialog({ title: `Delete rule “${r.name}”?`, message: 'The rule stops evaluating. Its past events stay in the history.', danger: true, confirmLabel: 'Delete rule' });
    if (!ok) return;
    try { await api.deleteRule(r.id); toast({ title: 'Rule deleted', message: r.name }); onChanged(); } catch (e) { toastError(e, 'Could not delete rule'); }
  };
  return html`<article class=${cls('card rule-card', !enabled && 'is-disabled', firing && `is-firing tone-${sevTone(r.severity)}`)} style=${`--i:${i}`} data-rule=${r.id}>
    <div class="rule-head">
      <div class="rule-title"><${SeverityBadge} severity=${r.severity} live=${firing} /><h3>${r.name}</h3>${firing > 0 && html`<${Tag} tone="bad">${firing} firing</${Tag}>`}</div>
      <label class="toggle rule-toggle" title=${admin ? '' : 'Requires admin role'}>
        <input type="checkbox" role="switch" checked=${enabled} disabled=${!admin} aria-label=${`Enable rule ${r.name}`} onChange=${(e) => toggle(e.currentTarget.checked)} />
        <span class="toggle-track" aria-hidden="true"><span class="toggle-thumb"></span></span></label>
    </div>
    ${undeliverable.length > 0 && html`<div class="tags">${undeliverable.map((w) => html`<span class="tag tag-warn" data-testid="rule-undeliverable"><${Icon} name="alert" size=${12} />${w}</span>`)}</div>`}
    <p class="rule-summary" data-testid="rule-summary">${ruleSummary(r, taskName, destName)}</p>
    <div class="rule-meta subtle">cooldown ${r.cooldown || '—'}${r.notifyResolved ? ' · notifies on resolve' : ''}</div>
    ${results && html`<div class="rule-results" data-testid="rule-test-results"><div class="row-between"><b>Test results</b>
      <button type="button" class="btn btn-ghost btn-sm btn-icon" aria-label="Hide test results" onClick=${() => setResults(null)}><${Icon} name="x" size=${14} /></button></div>
      ${results.length ? html`<${DeliveryList} items=${results} />` : html`<p class="subtle">No channels configured.</p>`}</div>`}
    ${admin && html`<div class="rule-actions">
      <${Button} size="sm" icon="send" loading=${busy === 'test'} onClick=${test} aria-label=${`Send test for ${r.name}`}>Send test</${Button}>
      <${Button} size="sm" icon="edit" onClick=${() => onEdit(r)} aria-label=${`Edit rule ${r.name}`}>Edit</${Button}>
      <${Button} size="sm" kind="danger-ghost" icon="trash" onClick=${del} aria-label=${`Delete rule ${r.name}`}>Delete</${Button}>
    </div>`}
  </article>`;
}

// ---------------------------------------------------------------- history
function Timeline({ events, taskName }) {
  if (!events.length) return html`<${Empty} icon="history" title="No events yet">Firing and resolved alerts appear here with their deliveries.</${Empty}>`;
  return html`<ol class="timeline" data-testid="alert-timeline">${events.map((e, i) => html`<li key=${e.id} class=${cls('tl-item', e.state === 'resolved' ? 'tone-ok' : `tone-${sevTone(e.severity)}`)} style=${`--i:${Math.min(i, 12)}`}>
    <span class="tl-dot" aria-hidden="true"><${Icon} name=${e.state === 'resolved' ? 'check' : 'bell'} size=${13} /></span>
    <div class="tl-body">
      <div class="tl-head"><b>${e.ruleName}</b><span class=${cls('tag', e.state === 'resolved' ? 'tag-ok' : `tag-${sevTone(e.severity) === 'info' ? 'violet' : sevTone(e.severity)}`)}>${e.state}</span>
        ${e.taskId && html`<a class="mono tl-task" href=${`#/tasks/${encodeURIComponent(e.taskId)}`}>${e.taskName || taskName(e.taskId)}</a>`}
        ${e.destinationId && html`<a class="tl-task dest-badge" href=${`#/destinations/${encodeURIComponent(e.destinationId)}`}><${Icon} name="database" size=${12} />${e.destinationName || e.destinationId}</a>`}
        <time class="subtle tl-time" datetime=${e.at} title=${fmtTime(e.at, true)}>${timeAgo(e.at)}</time></div>
      <p>${e.message}</p>
      ${(e.deliveries || []).length > 0 && html`<${DeliveryList} items=${e.deliveries} />`}
    </div>
  </li>`)}</ol>`;
}

// ---------------------------------------------------------------- page
export function AlertsPage() {
  const lv = useLive();
  useTick(15000);
  const rules = useFetch(() => api.alertRules(), []);
  const [stateF, setStateF] = useState('');
  const [ruleF, setRuleF] = useState('');
  const events = useFetch(() => api.alertEvents({ state: stateF, ruleId: ruleF }), [stateF, ruleF]);
  const tasks = useFetch(() => api.tasks().catch(() => []), []);
  const channels = useFetch(() => api.alertChannels().catch(() => ({ email: true, telegram: true, webhook: true })), []);
  const noChannel = channels.data && !channels.data.email && !channels.data.telegram;
  const [editing, setEditing] = useState(null); // null | 'new' | rule
  useEffect(() => { if (lv.alertSeq) events.reload(true); }, [lv.alertSeq]);
  const names = useMemo(() => Object.fromEntries((tasks.data || []).map((t) => [t.id, t.name || t.id])), [tasks.data]);
  const taskName = (id) => names[id] || id;
  const dests = useDestinations() || [];
  const destName = (id) => (dests.find((d) => d.id === id) || {}).name || id;
  const firing = activeAlerts();
  const firingBy = (id) => firing.filter((e) => e.ruleId === id).length;
  const admin = can('alerts.write');

  return html`
    <${PageHeader} title="Alerts" subtitle="Get notified by email, Telegram or webhook when pipelines fail, lag or reject documents."
      actions=${admin && channels.data && !noChannel && html`<${Button} kind="primary" icon="plus" class="keep-label" onClick=${() => setEditing('new')} data-testid="new-rule">New rule</${Button}>`} />
    ${noChannel && html`<section class="card channel-gate" data-testid="channel-gate" aria-labelledby="gate-title">
      <div class="gate-illu" aria-hidden="true"><span class="gate-bell"><${Icon} name="bell" size=${30} /></span><span class="gate-ring"></span></div>
      <div class="gate-body"><h2 id="gate-title">Set up a notification channel first</h2>
        <p class="muted">Alert rules deliver by email or Telegram (webhooks are optional extras). ${admin ? 'Connect at least one channel to create rules.' : 'Ask an admin to connect one.'}</p>
        ${admin && html`<div class="gate-cards">
          <a class="card gate-card is-hoverable" href="#/settings/email"><span class="kpi-icon"><${Icon} name="mail" size=${16} /></span><div><b>Configure Email (SMTP)</b><p class="muted">Send alerts, invitations and password resets by email.</p></div><${Icon} name="arrow-right" size=${16} /></a>
          <a class="card gate-card is-hoverable" href="#/settings/telegram"><span class="kpi-icon alt"><${Icon} name="telegram" size=${16} /></span><div><b>Connect a Telegram bot</b><p class="muted">Push alerts to a chat, group or channel in seconds.</p></div><${Icon} name="arrow-right" size=${16} /></a>
        </div>`}</div>
    </section>`}

    <section aria-labelledby="active-title">
      <div class="section-title" style="margin-top:0"><h2 id="active-title"><${Icon} name="bell" />Firing now${firing.length > 0 && html` <span class="count-pill tone-bad">${firing.length}</span>`}</h2></div>
      ${firing.length ? html`<div class="alert-cards stagger" data-testid="active-alerts">${firing.map((e, i) => html`<${ActiveAlertCard} key=${e.id} e=${e} i=${i} />`)}</div>`
        : html`<div class="card all-clear" data-testid="all-clear"><span class="all-clear-icon"><${Icon} name="shield" size=${22} /></span><div><b>All clear</b><p class="muted">No alert is firing.</p></div></div>`}
    </section>

    <section aria-labelledby="rules-title">
      <div class="section-title"><h2 id="rules-title"><${Icon} name="filter" />Rules</h2><span class="subtle">${rules.data ? `${rules.data.filter((r) => r.enabled).length} of ${rules.data.length} enabled` : ''}</span></div>
      <${ErrorBox} error=${rules.error && !rules.data && rules.error} onRetry=${() => rules.reload()} />
      ${!rules.data ? html`<div class="rule-list">${[0, 1].map((i) => html`<div class="card card-pad stack-sm" key=${i}><${Skeleton} w="40%" h=${16} /><${Skeleton} w="80%" /></div>`)}</div>`
        : !rules.data.length ? html`<div class="card"><${Empty} icon="bell" title="No alert rules" action=${admin && !noChannel && html`<${Button} kind="primary" icon="plus" onClick=${() => setEditing('new')}>Create a rule</${Button}>`}>
            Rules watch task states, lag, dead letters and Meilisearch health.</${Empty}></div>`
        : html`<div class="rule-list stagger" data-testid="rule-list">${rules.data.map((r, i) => html`<${RuleCard} key=${r.id} r=${r} i=${i} taskName=${taskName} destName=${destName} firing=${firingBy(r.id)} channels=${channels.data}
            onEdit=${setEditing} onChanged=${() => rules.reload(true)} />`)}</div>`}
    </section>

    <section aria-labelledby="hist-title">
      <div class="section-title"><h2 id="hist-title"><${Icon} name="history" />History</h2>
        <div class="row">
          <div class="seg" role="group" aria-label="Event state">${[['', 'All'], ['firing', 'Firing'], ['resolved', 'Resolved']].map(([v, l]) => html`<button type="button" aria-pressed=${stateF === v} onClick=${() => setStateF(v)}>${l}</button>`)}</div>
          <select class="select select-sm" aria-label="Filter by rule" value=${ruleF} onChange=${(e) => setRuleF(e.currentTarget.value)} style="width:auto">
            <option value="">All rules</option>${(rules.data || []).map((r) => html`<option value=${r.id}>${r.name}</option>`)}</select>
        </div></div>
      <div class="card card-pad">${!events.data ? html`<div class="stack-sm">${[0, 1, 2].map((i) => html`<${Skeleton} key=${i} h=${48} />`)}</div>` : html`<${Timeline} events=${events.data} taskName=${taskName} />`}</div>
    </section>

    <${RuleEditor} open=${!!editing} rule=${editing && editing !== 'new' ? editing : null} tasks=${tasks.data || []} dests=${dests} channels=${channels.data}
      onClose=${() => setEditing(null)} onSaved=${() => rules.reload(true)} />
  `;
}
