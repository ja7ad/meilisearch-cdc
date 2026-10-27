// Shared UI components: buttons, badges, modals, toasts, dialogs, skeletons,
// animated counters, progress bars, tabs, empty states, JSON viewer.
import { html, render, useEffect, useLayoutEffect, useRef, useState } from '../vendor/htm-preact.js';
import { Icon } from './icons.js';
import { HelpLink } from './docs.js';
import { useTween } from './hooks.js';
import { cls, copyText, fmtInt, pretty, reducedMotion, stateInfo } from './util.js';

// ---------------------------------------------------------------- basics

export function Button({ kind = 'secondary', size, icon, iconRight, loading, children, class: c, type = 'button', ...rest }) {
  return html`<button type=${type} class=${cls('btn', `btn-${kind}`, size && `btn-${size}`, !children && 'btn-icon', loading && 'is-loading', c)}
    disabled=${rest.disabled || loading} aria-busy=${loading ? 'true' : undefined} ...${rest}>
    ${loading ? html`<span class="spinner" aria-hidden="true"></span>` : icon && html`<${Icon} name=${icon} size=${size === 'sm' ? 15 : 17} />`}
    ${children && html`<span>${children}</span>`}
    ${iconRight && html`<${Icon} name=${iconRight} size=${15} />`}
  </button>`;
}

export function StateBadge({ state, size }) {
  const s = stateInfo(state);
  return html`<span class=${cls('badge', `tone-${s.tone}`, s.live && 'is-live', size === 'lg' && 'badge-lg')} data-state=${state}>
    <span class="dot" aria-hidden="true"></span>${s.label}</span>`;
}

export const Tag = ({ children, tone = 'neutral', title }) => html`<span class=${`tag tag-${tone}`} title=${title}>${children}</span>`;

export function Counter({ value, format = fmtInt, class: c }) {
  const v = useTween(value);
  return html`<span class=${cls('num', c)}>${format(v)}</span>`;
}

export function Progress({ value = 0, max = 1, tone = 'sync', shimmer, label }) {
  const pct = max > 0 ? Math.max(0, Math.min(100, (value / max) * 100)) : 0;
  return html`<div class=${cls('progress', `tone-${tone}`, shimmer && 'is-shimmer')} role="progressbar"
      aria-valuemin="0" aria-valuemax="100" aria-valuenow=${Math.round(pct)} aria-label=${label || 'Progress'}>
    <div class="progress-bar" style=${`width:${pct}%`}></div>
  </div>`;
}

export const Skeleton = ({ w = '100%', h = 14, r, class: c }) =>
  html`<span class=${cls('skeleton', c)} style=${`width:${typeof w === 'number' ? w + 'px' : w};height:${h}px;${r ? `border-radius:${r}px` : ''}`} aria-hidden="true"></span>`;

export function SkeletonRows({ rows = 5, cols = 5 }) {
  return html`<div class="skeleton-table" aria-busy="true" aria-label="Loading">
    ${Array.from({ length: rows }, (_, i) => html`<div class="skeleton-row" key=${i} style=${`--i:${i}`}>
      ${Array.from({ length: cols }, (_, j) => html`<${Skeleton} key=${j} w=${j === 0 ? '28%' : `${12 + ((i + j) % 3) * 6}%`} />`)}
    </div>`)}
  </div>`;
}

export function Empty({ icon = 'sparkle', title, children, action }) {
  return html`<div class="empty">
    <div class="empty-icon"><${Icon} name=${icon} size=${26} /></div>
    <h3>${title}</h3>
    ${children && html`<p>${children}</p>`}
    ${action}
  </div>`;
}

export function ErrorBox({ error, onRetry }) {
  if (!error) return null;
  return html`<div class="alert alert-bad" role="alert">
    <${Icon} name="error" />
    <div class="alert-body"><strong>${error.message || String(error)}</strong>
      ${error.details && error.details.length ? html`<ul>${error.details.map((d) => html`<li>${d}</li>`)}</ul>` : null}
    </div>
    ${onRetry && html`<${Button} size="sm" onClick=${onRetry}>Retry</${Button}>`}
  </div>`;
}

export function Alert({ tone = 'info', icon, title, children, action, ...rest }) {
  return html`<div class=${`alert alert-${tone}`} role=${tone === 'bad' ? 'alert' : 'status'} ...${rest}>
    <${Icon} name=${icon || { info: 'info', bad: 'error', warn: 'alert', ok: 'success' }[tone]} />
    <div class="alert-body">${title && html`<strong>${title}</strong>`}${children && html`<div>${children}</div>`}</div>
    ${action}
  </div>`;
}

export function Toggle({ checked, onChange, label, hint, id, disabled }) {
  const tid = id || `tg-${label}`.replace(/\W+/g, '-');
  return html`<label class=${cls('toggle', disabled && 'is-disabled')} for=${tid}>
    <input type="checkbox" role="switch" id=${tid} checked=${!!checked} disabled=${disabled}
      onChange=${(e) => onChange(e.currentTarget.checked)} />
    <span class="toggle-track" aria-hidden="true"><span class="toggle-thumb"></span></span>
    <span class="toggle-text"><span>${label}</span>${hint && html`<small>${hint}</small>`}</span>
  </label>`;
}

export function Field({ label, hint, error, children, required, class: c, htmlFor }) {
  return html`<div class=${cls('field', error && 'has-error', c)}>
    ${label && html`<label class="field-label" for=${htmlFor}>${label}${required && html`<span class="req" aria-hidden="true"> *</span>`}</label>`}
    ${children}
    ${error ? html`<p class="field-error" role="alert">${error}</p>` : hint && html`<p class="field-hint">${hint}</p>`}
  </div>`;
}

export function Tabs({ tabs, active, onChange, label = 'Sections' }) {
  const refs = useRef([]);
  const onKey = (e, i) => {
    const d = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    if (!d) return;
    e.preventDefault();
    const n = (i + d + tabs.length) % tabs.length;
    onChange(tabs[n].id);
    refs.current[n]?.focus();
  };
  return html`<div class="tabs" role="tablist" aria-label=${label}>
    ${tabs.map((t, i) => html`<button key=${t.id} ref=${(el) => (refs.current[i] = el)} role="tab" type="button"
        id=${`tab-${t.id}`} aria-selected=${active === t.id} tabindex=${active === t.id ? 0 : -1}
        class=${cls('tab', active === t.id && 'is-active')} onClick=${() => onChange(t.id)} onKeyDown=${(e) => onKey(e, i)}>
      ${t.icon && html`<${Icon} name=${t.icon} size=${16} />`}<span>${t.label}</span>
      ${t.count != null && t.count !== 0 && html`<span class="tab-count">${t.count}</span>`}
    </button>`)}
  </div>`;
}

export function JsonView({ value, maxHeight, label = 'JSON' }) {
  const [copied, setCopied] = useState(false);
  const text = typeof value === 'string' ? value : pretty(value);
  return html`<div class="json-view">
    <button type="button" class="btn btn-ghost btn-sm json-copy" aria-label=${`Copy ${label}`}
      onClick=${async () => { if (await copyText(text)) { setCopied(true); setTimeout(() => setCopied(false), 1200); } }}>
      <${Icon} name=${copied ? 'check' : 'copy'} size=${14} /><span>${copied ? 'Copied' : 'Copy'}</span></button>
    <pre style=${maxHeight ? `max-height:${maxHeight}px` : ''} tabindex="0" aria-label=${label}><code dangerouslySetInnerHTML=${{ __html: highlight(text) }}></code></pre>
  </div>`;
}

function esc(s) { return s.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[c]); }
function highlight(json) {
  return esc(json).replace(/("(\\u[a-fA-F0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+-]?\d+)?)/g, (m) => {
    let c = 'j-num';
    if (/^"/.test(m)) c = /:$/.test(m) ? 'j-key' : 'j-str';
    else if (/true|false/.test(m)) c = 'j-bool';
    else if (/null/.test(m)) c = 'j-null';
    return `<span class="${c}">${m}</span>`;
  });
}

export function Stat({ label, children, hint }) {
  return html`<div class="stat"><div class="stat-label">${label}</div><div class="stat-value">${children}</div>${hint && html`<div class="stat-hint">${hint}</div>`}</div>`;
}

export function PageHeader({ title, subtitle, actions, back, children, doc, docSource }) {
  return html`<header class="page-header">
    <div class="page-header-main">
      ${back && html`<a class="back-link" href=${back.href}><${Icon} name="arrow-left" size=${15} />${back.label}</a>`}
      ${doc || docSource ? html`<div class="title-row"><h1>${title}</h1><${HelpLink} doc=${doc} source=${docSource} label=${`${typeof title === 'string' ? title : 'This page'} documentation`} /></div>` : html`<h1>${title}</h1>`}
      ${subtitle && html`<p class="page-subtitle">${subtitle}</p>`}
      ${children}
    </div>
    ${actions && html`<div class="page-actions">${actions}</div>`}
  </header>`;
}

// ---------------------------------------------------------------- portal

/**
 * Renders children into #overlay-root (directly under <body>) so fixed overlays are never
 * trapped by a transformed or filtered ancestor. The standalone Preact build has no
 * createPortal, so this keeps a separate render root in sync with the parent.
 */
export function Portal({ children }) {
  const host = useRef(null);
  if (!host.current && typeof document !== 'undefined') host.current = document.createElement('div');
  useLayoutEffect(() => {
    const root = document.getElementById('overlay-root') || document.body;
    root.appendChild(host.current);
    return () => { render(null, host.current); host.current.remove(); };
  }, []);
  useLayoutEffect(() => { render(html`${children}`, host.current); });
  return null;
}

// ---------------------------------------------------------------- modal

let modalSeq = 0;
/** Modal keeps itself mounted for the exit animation. Esc and backdrop close it. */
export function Modal({ open, onClose, title, children, footer, size = 'md', dismissable = true, help }) {
  const [mounted, setMounted] = useState(open);
  const [closing, setClosing] = useState(false);
  const box = useRef(null);
  const prevFocus = useRef(null);
  const titleId = useRef(`modal-title-${++modalSeq}`).current;

  useLayoutEffect(() => {
    if (open) { setMounted(true); setClosing(false); return undefined; }
    if (!mounted) return undefined;
    setClosing(true);
    const t = setTimeout(() => { setMounted(false); setClosing(false); }, reducedMotion() ? 0 : 170);
    return () => clearTimeout(t);
  }, [open]);

  // layout effect: the Escape/Tab handler must be active as soon as the dialog is painted
  useLayoutEffect(() => {
    if (!open || !mounted) return undefined;
    prevFocus.current = document.activeElement;
    const el = box.current;
    const first = el && (el.querySelector('[data-autofocus]') || el.querySelector('input, select, textarea, button:not(.modal-close)') || el);
    // Deferred so the dialog is painted; never steal focus from a field the
    // user (or a password manager) already moved to.
    setTimeout(() => { if (first && el && !el.contains(document.activeElement)) first.focus(); }, 20);
    const onKey = (e) => {
      if (e.key === 'Escape' && dismissable) { e.stopPropagation(); onClose && onClose(); }
      if (e.key === 'Tab' && el) {
        const f = [...el.querySelectorAll('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')].filter((x) => !x.disabled && x.offsetParent !== null);
        if (!f.length) return;
        if (e.shiftKey && document.activeElement === f[0]) { e.preventDefault(); f[f.length - 1].focus(); }
        else if (!e.shiftKey && document.activeElement === f[f.length - 1]) { e.preventDefault(); f[0].focus(); }
      }
    };
    document.addEventListener('keydown', onKey, true);
    document.body.classList.add('modal-open');
    return () => {
      document.removeEventListener('keydown', onKey, true);
      document.body.classList.remove('modal-open');
      if (prevFocus.current && prevFocus.current.focus) prevFocus.current.focus();
    };
  }, [open, mounted]);

  if (!mounted) return null;
  return html`<${Portal}><div class=${cls('modal-root', closing && 'is-closing')}>
    <div class="modal-backdrop" onClick=${() => dismissable && onClose && onClose()}></div>
    <div class=${`modal modal-${size}`} role="dialog" aria-modal="true" aria-labelledby=${titleId} ref=${box} tabindex="-1">
      <header class="modal-header">
        <h2 id=${titleId}>${title}</h2>
        ${help && html`<${HelpLink} doc=${help.doc} source=${help.source} label=${help.label} />`}
        ${dismissable && html`<button type="button" class="btn btn-ghost btn-icon modal-close" aria-label="Close dialog" onClick=${onClose}><${Icon} name="x" /></button>`}
      </header>
      <div class="modal-body">${children}</div>
      ${footer && html`<footer class="modal-footer">${footer}</footer>`}
    </div>
  </div></${Portal}>`;
}

// ---------------------------------------------------------------- toasts

const toastSubs = new Set();
let toasts = [];
let toastId = 0;
const emitToasts = () => toastSubs.forEach((f) => f(toasts));

/** toast("Saved") or toast({title, message, tone: ok|bad|warn|info, timeout}) */
export function toast(t) {
  const o = typeof t === 'string' ? { title: t } : t;
  const id = ++toastId;
  toasts = [...toasts, { id, tone: 'ok', timeout: o.tone === 'bad' ? 7000 : 4000, ...o }].slice(-5);
  emitToasts();
  const item = toasts.find((x) => x.id === id);
  if (item && item.timeout) setTimeout(() => dismissToast(id), item.timeout);
  return id;
}
export const toastError = (e, title = 'Request failed') => toast({ tone: 'bad', title, message: e && (e.message || String(e)) });

export function dismissToast(id) {
  toasts = toasts.map((t) => (t.id === id ? { ...t, leaving: true } : t));
  emitToasts();
  setTimeout(() => { toasts = toasts.filter((t) => t.id !== id); emitToasts(); }, reducedMotion() ? 0 : 220);
}

export function Toaster() {
  const [list, setList] = useState(toasts);
  useEffect(() => { toastSubs.add(setList); return () => toastSubs.delete(setList); }, []);
  return html`<${Portal}><div class="toaster" role="region" aria-label="Notifications">
    <div aria-live="polite" aria-atomic="false">
      ${list.map((t) => html`<div key=${t.id} class=${cls('toast', `tone-${t.tone}`, t.leaving && 'is-leaving')} role=${t.tone === 'bad' ? 'alert' : 'status'}>
        <span class="toast-icon"><${Icon} name=${{ ok: 'success', bad: 'error', warn: 'alert', info: 'info' }[t.tone] || 'info'} /></span>
        <div class="toast-body"><strong>${t.title}</strong>${t.message && html`<p>${t.message}</p>`}
          ${t.action && (t.action.onClick
            ? html`<button type="button" class="toast-action link-btn" onClick=${() => { dismissToast(t.id); t.action.onClick(); }}>${t.action.label}<${Icon} name="arrow-right" size=${13} /></button>`
            : html`<a class="toast-action" href=${t.action.href} onClick=${() => dismissToast(t.id)}>${t.action.label}<${Icon} name="arrow-right" size=${13} /></a>`)}</div>
        <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Dismiss notification" onClick=${() => dismissToast(t.id)}><${Icon} name="x" size=${15} /></button>
      </div>`)}
    </div>
  </div></${Portal}>`;
}

// ---------------------------------------------------------------- confirm dialog

const dlgSubs = new Set();
let dlg = null;

/**
 * confirmDialog({title, message, confirmLabel, danger, typed, options}) -> Promise<false | {options}>
 * `typed` requires the user to type that exact string. `options` is a list of
 * toggles [{id, label, hint, checked}] returned with their values; `checked`
 * is the default.
 */
export function confirmDialog(o) {
  return new Promise((resolve) => {
    dlg = { ...o, resolve, open: true };
    dlgSubs.forEach((f) => f(dlg));
  });
}

export function DialogHost() {
  const [d, setD] = useState(null);
  const [typed, setTyped] = useState('');
  const [opts, setOpts] = useState({});
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    // Options start from their `checked` default.
    const f = (x) => { setD({ ...x }); setTyped(''); setOpts(Object.fromEntries((x.options || []).map((o) => [o.id, !!o.checked]))); setBusy(false); };
    dlgSubs.add(f);
    return () => dlgSubs.delete(f);
  }, []);
  const close = (result) => {
    if (!d || !d.open) return;
    d.resolve(result);
    setD({ ...d, open: false });
  };
  const ok = !d || !d.typed || typed === d.typed;
  const submit = (e) => { e && e.preventDefault(); if (ok) { setBusy(true); close({ options: opts }); } };
  return html`<${Modal} open=${!!(d && d.open)} onClose=${() => close(false)} title=${d ? d.title : ''} size="sm"
    footer=${d && html`<${Button} onClick=${() => close(false)}>Cancel</${Button}>
      <${Button} kind=${d.danger ? 'danger' : 'primary'} disabled=${!ok} loading=${busy} onClick=${submit} data-testid="confirm-ok">${d.confirmLabel || 'Confirm'}</${Button}>`}>
    ${d && html`<form onSubmit=${submit} class="confirm-form">
      ${d.message && html`<p class="confirm-message">${d.message}</p>`}
      ${(d.options || []).map((o) => html`<${Toggle} key=${o.id} id=${`opt-${o.id}`} label=${o.label} hint=${o.hint} checked=${opts[o.id]} onChange=${(v) => setOpts({ ...opts, [o.id]: v })} />`)}
      ${d.typed && html`<${Field} label=${html`Type <code class="typed-code">${d.typed}</code> to confirm`} htmlFor="confirm-typed">
        <input id="confirm-typed" class="input mono" data-autofocus autocomplete="off" spellcheck="false" value=${typed}
          onInput=${(e) => setTyped(e.currentTarget.value)} aria-describedby="confirm-typed-hint" />
        <p id="confirm-typed-hint" class="field-hint">${typed && !ok ? 'Does not match yet' : 'This cannot be undone.'}</p>
      </${Field}>`}
    </form>`}
  </${Modal}>`;
}

// ---------------------------------------------------------------- accounts & alerts helpers

const ROLE_TONE = { admin: 'primary', moderator: 'violet', viewer: 'neutral' };
export const RoleBadge = ({ role }) => html`<span class=${`tag tag-${ROLE_TONE[role] || 'neutral'} role-badge`} data-role=${role}>${role || '—'}</span>`;

const SEV_TONE = { critical: 'bad', warning: 'warn', info: 'info' };
export const sevTone = (s) => SEV_TONE[s] || 'info';
export function SeverityBadge({ severity, live }) {
  return html`<span class=${cls('badge', `tone-${sevTone(severity)}`, live && 'is-live')} data-severity=${severity}><span class="dot" aria-hidden="true"></span>${severity}</span>`;
}

/** Read-only value with a copy button (links, tokens, config snippets). */
export function CopyField({ value, label, mono = true, multiline = false, testid }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => { if (await copyText(value)) { setCopied(true); setTimeout(() => setCopied(false), 1400); } };
  return html`<div class=${cls('copy-field', multiline && 'is-multiline')}>
    ${multiline ? html`<pre class=${cls('copy-value', mono && 'mono')} tabindex="0" aria-label=${label} data-testid=${testid}>${value}</pre>`
      : html`<input class=${cls('input copy-value', mono && 'mono')} readonly value=${value} aria-label=${label} data-testid=${testid} onFocus=${(e) => e.currentTarget.select()} />`}
    <${Button} kind=${copied ? 'secondary' : 'primary'} icon=${copied ? 'check' : 'copy'} onClick=${copy} aria-label=${`Copy ${label}`}>${copied ? 'Copied' : 'Copy'}</${Button}>
  </div>`;
}

/**
 * Chips input: type and press Enter/comma to add, Backspace to remove the last.
 * validate(v) returns an error string or ''.
 */
export function ChipsInput({ id, values, onChange, placeholder, validate, label, inputMode }) {
  const [text, setText] = useState('');
  const [err, setErr] = useState('');
  const add = (raw) => {
    const vs = raw.split(/[,\s]+/).map((x) => x.trim()).filter(Boolean);
    if (!vs.length) return true;
    for (const v of vs) { const e = validate ? validate(v) : ''; if (e) { setErr(e); return false; } }
    onChange([...new Set([...values, ...vs])]);
    setText(''); setErr('');
    return true;
  };
  const onKey = (e) => {
    if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); add(text); }
    else if (e.key === 'Backspace' && !text && values.length) onChange(values.slice(0, -1));
  };
  return html`<div>
    <div class=${cls('chips-input', err && 'is-invalid')} onClick=${(e) => { const i = e.currentTarget.querySelector('input'); i && i.focus(); }}>
      ${values.map((v) => html`<span class="chip chip-static" key=${v}>${v}
        <button type="button" class="chip-x" aria-label=${`Remove ${v}`} onClick=${(e) => { e.stopPropagation(); onChange(values.filter((x) => x !== v)); }}><${Icon} name="x" size=${12} /></button></span>`)}
      <input id=${id} class="chips-text" value=${text} placeholder=${values.length ? '' : placeholder} inputmode=${inputMode}
        aria-label=${label} onInput=${(e) => { setText(e.currentTarget.value); setErr(''); }} onKeyDown=${onKey} onBlur=${() => text && add(text)} />
    </div>
    ${err && html`<p class="field-error" role="alert">${err}</p>`}
  </div>`;
}

/** Shows a 403 / forbidden state for pages a role cannot use. */
export function Forbidden({ role = 'admin', what = 'this page' }) {
  return html`<div class="card"><${Empty} icon="lock" title="Not available for your role">Only ${role}s can use ${what}.</${Empty}></div>`;
}
