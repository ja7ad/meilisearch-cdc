// Public, full-screen auth pages: first-admin setup, sign in, forgot / reset password, accept invitation.
import { html, useEffect, useState } from '../../vendor/htm-preact.js';
import { api, setToken } from '../api.js';
import { auth, passwordStrength, setAuth } from '../auth.js';
import { navigate } from '../router.js';
import { Icon, Logo } from '../icons.js';
import { Alert, Button, Field, RoleBadge, Skeleton, toast } from '../ui.js';
import { cls } from '../util.js';

/** Password input with a show/hide toggle and an 8–128 strength hint. */
export function PasswordField({ id, label = 'Password', value, onInput, autocomplete = 'new-password', strength = true, error }) {
  const [show, setShow] = useState(false);
  const s = passwordStrength(value);
  return html`<${Field} label=${label} htmlFor=${id} error=${error}>
    <div class="input-affix">
      <input id=${id} type=${show ? 'text' : 'password'} class="input" autocomplete=${autocomplete} value=${value}
        onInput=${(e) => onInput(e.currentTarget.value)} aria-describedby=${strength ? `${id}-strength` : undefined} />
      <button type="button" class="btn btn-ghost btn-sm btn-icon affix-btn" aria-label=${show ? 'Hide password' : 'Show password'} aria-pressed=${show} onClick=${() => setShow(!show)}>
        <${Icon} name=${show ? 'eye' : 'lock'} size=${15} /></button>
    </div>
    ${strength && html`<div class="pw-strength" id=${`${id}-strength`} aria-live="polite">
      <div class=${`pw-meter s${s.score}`} aria-hidden="true"><span></span><span></span><span></span><span></span></div>
      <span class=${cls('pw-text', !s.ok && value && 'is-bad')}>${s.label && html`<b>${s.label}.</b> `}${s.hint}</span>
    </div>`}
  </${Field}>`;
}

function AuthLayout({ title, subtitle, children, footer, wide }) {
  return html`<div class="auth-screen">
    <div class="auth-glow" aria-hidden="true"><span></span><span></span><span></span></div>
    <main class=${cls('auth-card', wide && 'is-wide')} id="main">
      <div class="auth-brand"><${Logo} size=${36} /><span><b>Meili CDC</b><small>change data capture for Meilisearch</small></span></div>
      <div class="auth-head"><h1>${title}</h1>${subtitle && html`<p class="muted">${subtitle}</p>`}</div>
      ${children}
      ${footer && html`<div class="auth-foot">${footer}</div>`}
    </main>
  </div>`;
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

function SetupPage() {
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [tried, setTried] = useState(false);
  const errs = {
    email: !EMAIL_RE.test(email) ? 'Enter a valid email address' : '',
    pw: !passwordStrength(pw).ok ? 'Use 8 to 128 characters' : '',
    pw2: pw !== pw2 ? 'Passwords do not match' : '',
  };
  const submit = async (e) => {
    e.preventDefault();
    setTried(true);
    if (errs.email || errs.pw || errs.pw2) return;
    setBusy(true); setErr('');
    try {
      const r = await api.setup({ email: email.trim(), name: name.trim(), password: pw });
      setAuth({ user: r.user, config: { ...auth.config, setupRequired: false } });
      toast({ title: 'Welcome to meilisearch-cdc', message: 'Your admin account is ready.' });
      navigate('#/overview');
    } catch (er) { setErr(er.message); setBusy(false); }
  };
  return html`<${AuthLayout} title="Create the first admin" subtitle="No accounts exist yet. This admin can invite the rest of your team.">
    <form class="stack auth-form" onSubmit=${submit} novalidate>
      ${err && html`<${Alert} tone="bad">${err}</${Alert}>`}
      <${Field} label="Email" htmlFor="su-email" error=${tried && errs.email}><input id="su-email" type="email" class="input" autocomplete="email" value=${email} onInput=${(e) => setEmail(e.currentTarget.value)} /></${Field}>
      <${Field} label="Name" htmlFor="su-name"><input id="su-name" class="input" autocomplete="name" value=${name} onInput=${(e) => setName(e.currentTarget.value)} /></${Field}>
      <${PasswordField} id="su-pw" value=${pw} onInput=${setPw} error=${tried && errs.pw} />
      <${Field} label="Confirm password" htmlFor="su-pw2" error=${(tried || pw2) && errs.pw2}><input id="su-pw2" type="password" class="input" autocomplete="new-password" value=${pw2} onInput=${(e) => setPw2(e.currentTarget.value)} /></${Field}>
      <${Button} kind="primary" size="lg" type="submit" loading=${busy} class="btn-block" data-testid="setup-submit">Create admin account</${Button}>
    </form>
  </${AuthLayout}>`;
}

function LoginPage({ config, next }) {
  const [email, setEmail] = useState('');
  const [pw, setPw] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [wait, setWait] = useState(0);
  const [tokenMode, setTokenMode] = useState(false);
  const [token, setTok] = useState('');
  useEffect(() => {
    if (wait <= 0) return undefined;
    const t = setTimeout(() => setWait(wait - 1), 1000);
    return () => clearTimeout(t);
  }, [wait]);
  const done = (user) => { setAuth({ user }); toast({ title: `Signed in as ${user.name || user.email || 'API token'}` }); navigate(next && !next.startsWith('#/login') ? next : '#/overview'); };
  const submit = async (e) => {
    e.preventDefault();
    if (!email || !pw || wait > 0) return;
    setBusy(true); setErr('');
    try { const r = await api.login(email.trim(), pw); done(r.user); return; }
    catch (er) {
      if (er.status === 429) { setWait(er.retryAfter || 30); setErr(''); }
      else setErr(er.status === 401 ? 'Invalid email or password.' : er.message);
    }
    setBusy(false);
  };
  const useToken = async (e) => {
    e.preventDefault();
    if (!token.trim()) return;
    setBusy(true); setErr('');
    setToken(token.trim());
    try { const r = await api.me(); done(r.user); return; }
    catch (er) { setToken(''); setErr(er.status === 401 ? 'The admin token was rejected.' : er.message); }
    setBusy(false);
  };
  return html`<${AuthLayout} title="Sign in" subtitle="Manage your change-data-capture pipelines."
    footer=${html`<button type="button" class="link-btn" onClick=${() => { setTokenMode(!tokenMode); setErr(''); }} aria-expanded=${tokenMode}>
      <${Icon} name=${tokenMode ? 'user' : 'key'} size=${14} />${tokenMode ? 'Sign in with email instead' : 'Use an API token instead'}</button>`}>
    ${wait > 0 && html`<${Alert} tone="warn" title="Too many attempts">Too many attempts, try again in <b class="num" data-testid="retry-seconds">${wait}</b> s.</${Alert}>`}
    ${err && html`<div role="alert" class="auth-error" data-testid="login-error"><${Icon} name="error" size=${16} />${err}</div>`}
    ${tokenMode
      ? html`<form class="stack auth-form" onSubmit=${useToken} key="token">
          <p class="muted">For automation and break-glass access: the server's <code>CDC_ADMIN_TOKEN</code>. It is stored in this browser and acts as an admin.</p>
          <${Field} label="Admin token" htmlFor="lg-token"><input id="lg-token" type="password" class="input mono" autocomplete="off" value=${token} onInput=${(e) => setTok(e.currentTarget.value)} /></${Field}>
          <${Button} kind="primary" size="lg" type="submit" icon="key" loading=${busy} disabled=${!token.trim()} class="btn-block">Save token</${Button}>
        </form>`
      : html`<form class="stack auth-form" onSubmit=${submit} key="pw">
          <${Field} label="Email" htmlFor="lg-email"><input id="lg-email" type="email" class="input" autocomplete="username" value=${email} onInput=${(e) => setEmail(e.currentTarget.value)} autofocus /></${Field}>
          <div class="field">
            <div class="row-between"><label class="field-label" for="lg-pw">Password</label>
              ${config.smtpConfigured && html`<a href="#/forgot" class="small-link">Forgot password?</a>`}</div>
            <input id="lg-pw" type="password" class="input" autocomplete="current-password" value=${pw} onInput=${(e) => setPw(e.currentTarget.value)} />
          </div>
          <${Button} kind="primary" size="lg" type="submit" loading=${busy} disabled=${wait > 0 || !email || !pw} class="btn-block" data-testid="login-submit">
            ${wait > 0 ? `Try again in ${wait} s` : 'Sign in'}</${Button}>
        </form>`}
  </${AuthLayout}>`;
}

function ForgotPage({ config }) {
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const [err, setErr] = useState('');
  const back = html`<a href="#/login" class="link-btn"><${Icon} name="arrow-left" size=${14} />Back to sign in</a>`;
  if (!config.smtpConfigured) {
    return html`<${AuthLayout} title="Reset your password" footer=${back}>
      <${Alert} tone="info" title="Email is not configured">Ask an administrator to set a new password for you from Settings → Users.</${Alert}></${AuthLayout}>`;
  }
  const submit = async (e) => {
    e.preventDefault();
    if (!EMAIL_RE.test(email)) { setErr('Enter a valid email address'); return; }
    setBusy(true); setErr('');
    try { await api.forgot(email.trim()); setSent(true); }
    catch (er) { setErr(er.status === 409 ? 'Email is not configured on this server.' : er.message); }
    setBusy(false);
  };
  return html`<${AuthLayout} title="Reset your password" subtitle=${sent ? '' : 'Enter your account email and we will send you a reset link.'} footer=${back}>
    ${sent ? html`<div class="auth-success" role="status" data-testid="forgot-sent">
        <span class="auth-success-icon"><${Icon} name="mail" size=${24} /></span>
        <h2>Check your inbox</h2><p class="muted">If an account exists for <b>${email}</b>, we sent a link to reset the password. The link expires soon.</p></div>`
      : html`<form class="stack auth-form" onSubmit=${submit} novalidate>
          <${Field} label="Email" htmlFor="fg-email" error=${err}><input id="fg-email" type="email" class="input" autocomplete="email" value=${email} onInput=${(e) => setEmail(e.currentTarget.value)} /></${Field}>
          <${Button} kind="primary" size="lg" type="submit" icon="send" loading=${busy} class="btn-block">Send reset link</${Button}>
        </form>`}
  </${AuthLayout}>`;
}

function ResetPage({ token }) {
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [done, setDone] = useState(false);
  const ok = passwordStrength(pw).ok && pw === pw2;
  const submit = async (e) => {
    e.preventDefault();
    if (!ok) return;
    setBusy(true); setErr('');
    try { await api.reset(token, pw); setDone(true); }
    catch (er) { setErr(er.status === 400 && /token|link|expired/i.test(er.message) ? 'This reset link is invalid or has expired. Request a new one.' : er.message); }
    setBusy(false);
  };
  return html`<${AuthLayout} title="Choose a new password" footer=${html`<a href="#/login" class="link-btn"><${Icon} name="arrow-left" size=${14} />Back to sign in</a>`}>
    ${done ? html`<div class="auth-success" role="status" data-testid="reset-done">
        <span class="auth-success-icon"><${Icon} name="check" size=${24} /></span>
        <h2>Password updated</h2><p class="muted">Other sessions of this account were signed out.</p>
        <a class="btn btn-primary btn-lg btn-block" href="#/login">Sign in</a></div>`
      : html`<form class="stack auth-form" onSubmit=${submit}>
          ${err && html`<${Alert} tone="bad">${err}</${Alert}>`}
          <${PasswordField} id="rs-pw" label="New password" value=${pw} onInput=${setPw} />
          <${Field} label="Confirm new password" htmlFor="rs-pw2" error=${pw2 && pw !== pw2 ? 'Passwords do not match' : ''}>
            <input id="rs-pw2" type="password" class="input" autocomplete="new-password" value=${pw2} onInput=${(e) => setPw2(e.currentTarget.value)} /></${Field}>
          <${Button} kind="primary" size="lg" type="submit" loading=${busy} disabled=${!ok} class="btn-block" data-testid="reset-submit">Set password</${Button}>
        </form>`}
  </${AuthLayout}>`;
}

function InvitePage({ token }) {
  const [inv, setInv] = useState(undefined);
  const [name, setName] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  useEffect(() => { api.invitation(token).then(setInv).catch((e) => setInv(e.status === 404 ? null : { error: e.message })); }, [token]);
  const ok = passwordStrength(pw).ok && pw === pw2;
  const submit = async (e) => {
    e.preventDefault();
    if (!ok) return;
    setBusy(true); setErr('');
    try {
      const r = await api.acceptInvite(token, name.trim(), pw);
      setAuth({ user: r.user });
      toast({ title: 'Welcome aboard', message: `Signed in as ${r.user.email}` });
      navigate('#/overview');
    } catch (er) { setErr(er.message); setBusy(false); }
  };
  if (inv === undefined) return html`<${AuthLayout} title="Accept invitation"><div class="stack"><${Skeleton} h=${40} /><${Skeleton} h=${40} /></div></${AuthLayout}>`;
  if (!inv || inv.error) {
    return html`<${AuthLayout} title="Invitation not valid" footer=${html`<a href="#/login" class="link-btn"><${Icon} name="arrow-left" size=${14} />Go to sign in</a>`}>
      <${Alert} tone="bad">${inv ? inv.error : 'This invitation is invalid, already used or expired. Ask an admin for a new one.'}</${Alert}></${AuthLayout}>`;
  }
  return html`<${AuthLayout} title="You're invited" subtitle="Create your account to join this meilisearch-cdc workspace.">
    <div class="invite-info" data-testid="invite-info"><span class="avatar avatar-lg" aria-hidden="true"><${Icon} name="mail" size=${18} /></span>
      <div><b>${inv.email}</b><div class="row" style="gap:6px"><span class="subtle">Role</span><${RoleBadge} role=${inv.role} /></div></div></div>
    <form class="stack auth-form" onSubmit=${submit}>
      ${err && html`<${Alert} tone="bad">${err}</${Alert}>`}
      <${Field} label="Your name" htmlFor="iv-name"><input id="iv-name" class="input" autocomplete="name" value=${name} onInput=${(e) => setName(e.currentTarget.value)} /></${Field}>
      <${PasswordField} id="iv-pw" value=${pw} onInput=${setPw} />
      <${Field} label="Confirm password" htmlFor="iv-pw2" error=${pw2 && pw !== pw2 ? 'Passwords do not match' : ''}>
        <input id="iv-pw2" type="password" class="input" autocomplete="new-password" value=${pw2} onInput=${(e) => setPw2(e.currentTarget.value)} /></${Field}>
      <${Button} kind="primary" size="lg" type="submit" loading=${busy} disabled=${!ok} class="btn-block" data-testid="invite-submit">Create account</${Button}>
    </form>
  </${AuthLayout}>`;
}

export function AuthPage({ m, config }) {
  const page = {
    setup: () => html`<${SetupPage} />`,
    login: () => html`<${LoginPage} config=${config} next=${m.next} />`,
    forgot: () => html`<${ForgotPage} config=${config} />`,
    reset: () => html`<${ResetPage} token=${m.token} />`,
    invite: () => html`<${InvitePage} token=${m.token} />`,
  }[m.page];
  return html`<div class="auth-page" key=${m.page}>${page()}</div>`;
}
