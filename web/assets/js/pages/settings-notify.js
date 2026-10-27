// Settings → Email (SMTP) and Telegram (admin).
import { html, useEffect, useLayoutEffect, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { useFetch } from '../hooks.js';
import { HelpLink } from '../docs.js';
import { Icon } from '../icons.js';
import { Alert, Button, ErrorBox, Field, Skeleton, toast } from '../ui.js';

const MASK = '••••••';

/** After a channel is configured, point to alert rules when none exist yet. */
async function suggestFirstRule(channel) {
  try {
    const rules = await api.alertRules();
    if (!rules.length) toast({ tone: 'info', title: `${channel} is ready for alerts`, message: 'Get notified when a pipeline fails or lags.', action: { label: 'Create an alert rule', href: '#/alerts' }, timeout: 9000 });
  } catch { /* optional hint */ }
}

function TestResult({ r }) {
  if (!r) return null;
  if (r.ok) return html`<${Alert} tone="ok" title="Delivered">${r.message}</${Alert}>`;
  // long SMTP errors carry a "(hint: …)": show the hint, keep the raw message under details
  const hint = /\(hint:\s*([^)]+)\)/.exec(r.error || '');
  return html`<${Alert} tone="bad" title="Failed" data-testid="test-error">
    <div class="err-wrap">${hint ? hint[1] : r.error}</div>
    ${hint && html`<details class="err-details"><summary>Technical details</summary><pre class="mono">${r.error}</pre></details>`}
  </${Alert}>`;
}

function StatusTag({ on, label }) {
  return on ? html`<span class="tag tag-ok" data-testid="notify-status"><span class="legend-dot tone-bg-ok"></span>${label || 'Configured'}</span>`
    : html`<span class="tag" data-testid="notify-status">Not configured</span>`;
}

export function EmailTab() {
  const { data, error, reload, setData } = useFetch(() => api.smtp(), []);
  const [f, setF] = useState(null);
  const [busy, setBusy] = useState('');
  const [saveErr, setSaveErr] = useState(null);
  const [to, setTo] = useState('');
  const [test, setTest] = useState(null);
  const [note, setNote] = useState('');
  useEffect(() => { if (data) setF({ ...data, port: data.port || 587, tls: data.tls || 'auto' }); }, [data]);
  if (error && !data) return html`<${ErrorBox} error=${error} onRetry=${() => reload()} />`;
  if (!f) return html`<div class="card card-pad stack">${[0, 1, 2].map((i) => html`<${Skeleton} key=${i} h=${38} />`)}</div>`;
  const up = (k) => (e) => setF({ ...f, [k]: e.currentTarget.value });
  // keep port and encryption consistent (465 = implicit TLS, 587 = STARTTLS)
  const setTls = (tls) => {
    const p = String(f.port || '');
    const port = tls === 'tls' && (!p || p === '587') ? 465 : tls === 'starttls' && (!p || p === '465') ? 587 : f.port;
    setF({ ...f, tls, port }); setNote('');
  };
  const setPort = (v) => {
    let tls = f.tls; let n = '';
    if (v === '465' && f.tls === 'starttls') { tls = 'tls'; n = 'Port 465 uses TLS — switched encryption.'; }
    if (v === '587' && f.tls === 'tls') { tls = 'starttls'; n = 'Port 587 uses STARTTLS — switched encryption.'; }
    setF({ ...f, port: v, tls }); setNote(n);
  };
  const save = async (e) => {
    e.preventDefault();
    setBusy('save'); setSaveErr(null);
    try {
      const r = await api.saveSmtp({ host: f.host.trim(), port: Number(f.port) || 587, username: f.username, password: f.password, from: f.from.trim(), tls: f.tls, publicUrl: f.publicUrl.trim() });
      setData(r);
      toast({ title: r.configured ? 'Email settings saved' : 'Email disabled', message: r.configured ? `Sending through ${r.host}` : 'Password reset by email is turned off.' });
      if (r.configured) suggestFirstRule('Email');
    } catch (er) { setSaveErr(er); }
    setBusy('');
  };
  const sendTest = async (e) => {
    e.preventDefault();
    setBusy('test'); setTest(null);
    try {
      const r = await api.testSmtp(to.trim());
      setTest({ ...r, message: `A test email was sent to ${to}.` });
      if (r.ok) toast({ title: 'Test email sent', message: `Check the inbox of ${to.trim()}.` });
    }
    catch (er) { setTest({ ok: false, error: er.message }); }
    setBusy('');
  };
  return html`<div class="grid notify-grid tab-panel">
    <form class="card" onSubmit=${save} data-testid="smtp-form" novalidate>
      <div class="card-head"><h2><${Icon} name="mail" />SMTP server<${HelpLink} doc="notify" label="Email alerts documentation" /></h2><${StatusTag} on=${data.configured} /></div>
      <div class="card-body stack">
        <p class="muted">Used for password resets, invitations and alert emails. Leave the host empty to disable email.</p>
        <${ErrorBox} error=${saveErr} />
        <div class="form-grid">
          <${Field} label="Host" htmlFor="sm-host"><input id="sm-host" class="input mono" autocomplete="off" inputmode="url" placeholder="smtp.example.com" value=${f.host} onInput=${up('host')} /></${Field}>
          <${Field} label="Port" htmlFor="sm-port" hint=${note ? '' : '465 for TLS, 587 for STARTTLS, 25 for a local relay.'}>
            <input id="sm-port" class="input" type="number" inputmode="numeric" min="1" max="65535" value=${f.port} onInput=${(e) => setPort(e.currentTarget.value)} />
            ${note && html`<p class="field-note" role="status" data-testid="port-note"><${Icon} name="info" size=${13} />${note}</p>`}</${Field}>
          <${Field} label="Username" htmlFor="sm-user"><input id="sm-user" class="input" autocomplete="off" value=${f.username} onInput=${up('username')} /></${Field}>
          <${Field} label="Password" htmlFor="sm-pass" hint=${f.password === MASK ? 'Stored. Leave as is to keep it.' : ''}>
            <input id="sm-pass" type="password" class="input" autocomplete="new-password" value=${f.password} onInput=${up('password')} /></${Field}>
          <${Field} label="From address" htmlFor="sm-from"><input id="sm-from" class="input" type="email" inputmode="email" placeholder="cdc@example.com" value=${f.from} onInput=${up('from')} /></${Field}>
          <${Field} label="Encryption" htmlFor="sm-tls" hint=${f.tls === 'auto' ? 'Port 465 uses TLS, any other port STARTTLS.' : ''}><select id="sm-tls" class="select" value=${f.tls} onChange=${(e) => setTls(e.currentTarget.value)}>
            <option value="auto">Auto (recommended)</option><option value="tls">TLS (port 465)</option><option value="starttls">STARTTLS (port 587)</option><option value="none">None (local relay only)</option></select></${Field}>
          <${Field} label="Public URL" htmlFor="sm-url" class="span-2" hint="Base URL of this dashboard used in emailed links. Falls back to CDC_PUBLIC_URL, then the request origin.">
            <input id="sm-url" class="input mono" type="url" inputmode="url" placeholder="https://cdc.example.com" value=${f.publicUrl} onInput=${up('publicUrl')} /></${Field}>
        </div>
      </div>
      <div class="card-foot"><${Button} kind="primary" type="submit" icon="check" loading=${busy === 'save'} data-testid="smtp-save">Save</${Button}></div>
    </form>
    <form class="card" onSubmit=${sendTest} novalidate>
      <div class="card-head"><h2><${Icon} name="send" />Send a test email</h2></div>
      <div class="card-body stack">
        <${Field} label="Recipient" htmlFor="sm-to"><input id="sm-to" class="input" type="email" inputmode="email" autocomplete="email" placeholder="you@example.com" value=${to} onInput=${(e) => setTo(e.currentTarget.value)} /></${Field}>
        <${Button} type="submit" icon="send" loading=${busy === 'test'} disabled=${!to.trim() || !data.configured} title=${data.configured ? undefined : 'Save SMTP settings first'} data-testid="smtp-test">Send test email</${Button}>
        ${busy === 'test' && html`<p class="subtle" role="status" data-testid="smtp-connecting"><span class="spinner" aria-hidden="true"></span> Connecting to ${data.host}:${data.port}… this can take up to 30 seconds.</p>`}
        <div aria-live="polite"><${TestResult} r=${test} /></div>
      </div>
    </form>
  </div>`;
}

export function TelegramTab() {
  const { data, error, reload, setData } = useFetch(() => api.telegram(), []);
  const [token, setTok] = useState('');
  const [apiUrl, setApiUrl] = useState('');
  const [busy, setBusy] = useState('');
  const [saveErr, setSaveErr] = useState(null);
  const [chat, setChat] = useState('');
  const [test, setTest] = useState(null);
  useLayoutEffect(() => { if (data) { setTok(data.botToken); setApiUrl(data.apiUrl); } }, [data]);
  if (error && !data) return html`<${ErrorBox} error=${error} onRetry=${() => reload()} />`;
  if (!data) return html`<div class="card card-pad stack">${[0, 1].map((i) => html`<${Skeleton} key=${i} h=${38} />`)}</div>`;
  const save = async (e) => {
    e.preventDefault();
    setBusy('save'); setSaveErr(null);
    try {
      const r = await api.saveTelegram({ botToken: token.trim(), apiUrl: apiUrl.trim() }); setData(r); toast({ title: r.configured ? `Connected to @${r.botName}` : 'Telegram disabled' });
      if (r.configured) suggestFirstRule('Telegram');
    }
    catch (er) { setSaveErr(er.status === 422 ? { message: `Telegram rejected the token: ${er.message}` } : er); }
    setBusy('');
  };
  const sendTest = async (e) => {
    e.preventDefault();
    setBusy('test'); setTest(null);
    try { const r = await api.testTelegram(chat.trim()); setTest({ ...r, message: `A test message was sent to chat ${chat}.` }); }
    catch (er) { setTest({ ok: false, error: er.message }); }
    setBusy('');
  };
  return html`<div class="grid notify-grid tab-panel">
    <form class="card" onSubmit=${save} data-testid="telegram-form" novalidate>
      <div class="card-head"><h2><${Icon} name="telegram" />Telegram bot<${HelpLink} doc="notify" label="Telegram alerts documentation" /></h2><${StatusTag} on=${data.configured} label=${data.botName ? `@${data.botName}` : 'Configured'} /></div>
      <div class="card-body stack">
        <${ErrorBox} error=${saveErr} />
        <${Field} label="Bot token" htmlFor="tg-token" hint=${token === MASK ? 'Stored. Leave as is to keep it, or clear it to disable Telegram.' : 'Looks like 123456789:AA… — validated with getMe when saved.'}>
          <input id="tg-token" type="password" class="input mono" autocomplete="off" value=${token} onInput=${(e) => setTok(e.currentTarget.value)} /></${Field}>
        <${Field} label="API URL" htmlFor="tg-url" hint="Change only when using a self-hosted Bot API server.">
          <input id="tg-url" class="input mono" type="url" inputmode="url" value=${apiUrl} onInput=${(e) => setApiUrl(e.currentTarget.value)} /></${Field}>
      </div>
      <div class="card-foot"><${Button} kind="primary" type="submit" icon="check" loading=${busy === 'save'} data-testid="telegram-save">Save</${Button}></div>
    </form>
    <div class="stack">
      <form class="card" onSubmit=${sendTest} novalidate>
        <div class="card-head"><h2><${Icon} name="send" />Send a test message</h2></div>
        <div class="card-body stack">
          <${Field} label="Chat id" htmlFor="tg-chat" hint="A user, group or channel id, e.g. -1001234567890."><input id="tg-chat" class="input mono" inputmode="numeric" value=${chat} onInput=${(e) => setChat(e.currentTarget.value)} /></${Field}>
          <${Button} type="submit" icon="send" loading=${busy === 'test'} disabled=${!chat.trim() || !data.configured} title=${data.configured ? undefined : 'Save a bot token first'} data-testid="telegram-test">Send test message</${Button}>
          <div aria-live="polite"><${TestResult} r=${test} /></div>
        </div>
      </form>
      <section class="card howto"><div class="card-head"><h2><${Icon} name="info" />How to set it up</h2></div>
        <ol class="card-body howto-steps">
          <li>Open Telegram, talk to <b>@BotFather</b> and send <code>/newbot</code>. Copy the token it gives you.</li>
          <li>Paste the token above and save. The bot name appears when it is valid.</li>
          <li>Add the bot to your group or channel (as admin for channels), then send it a message.</li>
          <li>Find the chat id: open <code>https://api.telegram.org/bot&lt;token&gt;/getUpdates</code> and read <code>chat.id</code> (groups and channels start with <code>-100</code>).</li>
          <li>Use that id in alert rules and send a test message here.</li>
        </ol></section>
    </div>
  </div>`;
}

