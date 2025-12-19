// Webhook signing secrets (HMAC-SHA256, X-CDC-Timestamp / X-CDC-Signature),
// shared by webhook tasks (incoming pushes) and alert rules (outgoing posts).
import { html, useState } from '../vendor/htm-preact.js';
import { Icon } from './icons.js';
import { Button } from './ui.js';
import { cls, copyText } from './util.js';

/** The value the API returns in place of a stored secret. */
export const SECRET_MASK = '••••••';
export const MIN_SECRET_LEN = 16;

export function generateSecret() {
  const b = new Uint8Array(32);
  crypto.getRandomValues(b);
  return `whsec_${btoa(String.fromCharCode(...b)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')}`;
}

/** Validation message for a secret ('' when fine or empty). */
export function secretError(v) {
  if (!v || v === SECRET_MASK) return '';
  return v.trim().length < MIN_SECRET_LEN ? `Use at least ${MIN_SECRET_LEN} characters, or generate one` : '';
}

/**
 * Secret input: generate, copy, rotate and remove. A stored secret comes back
 * masked, so it can only be replaced, never read again.
 */
export function SecretInput({ id, value, onChange, error, testid = 'signing-secret' }) {
  const [copied, setCopied] = useState(false);
  const [fresh, setFresh] = useState(false);
  const set = (v, isNew) => { onChange(v); setFresh(!!isNew); setCopied(false); };
  const copy = async () => { if (await copyText(value)) { setCopied(true); setTimeout(() => setCopied(false), 1400); } };
  if (value === SECRET_MASK) {
    return html`<div class="secret-set" data-testid=${`${testid}-set`}>
      <span class="secret-set-label"><${Icon} name="lock" size=${15} />A secret is set. It is stored encrypted and never shown again.</span>
      <div class="row">
        <${Button} size="sm" icon="rotate" onClick=${() => set(generateSecret(), true)} data-testid=${`${testid}-rotate`}>Rotate</${Button}>
        <${Button} size="sm" kind="ghost" onClick=${() => set('')} data-testid=${`${testid}-remove`}>Remove</${Button}>
      </div>
    </div>`;
  }
  return html`<div class="stack-sm">
    <div class="row" style="flex-wrap:nowrap">
      <input id=${id} class=${cls('input mono grow', error && 'is-invalid')} value=${value} placeholder="Leave empty to turn signing off"
        autocomplete="off" autocapitalize="off" spellcheck="false" data-testid=${testid} aria-invalid=${error ? 'true' : null}
        onInput=${(e) => set(e.currentTarget.value)} />
      ${value ? html`<${Button} size="sm" kind=${copied ? 'secondary' : 'primary'} icon=${copied ? 'check' : 'copy'} onClick=${copy} data-testid=${`${testid}-copy`}>${copied ? 'Copied' : 'Copy'}</${Button}>`
        : html`<${Button} size="sm" icon="key" onClick=${() => set(generateSecret(), true)} data-testid=${`${testid}-generate`}>Generate</${Button}>`}
    </div>
    ${value && fresh && html`<p class="field-warn" data-testid=${`${testid}-copy-now`}><${Icon} name="alert" size=${14} />Copy it now: after saving, the secret is hidden.</p>`}
  </div>`;
}

/** Shell snippet that signs and sends a request, for docs next to the form. */
export function signedCurl(url, body = '{"op":"upsert","row":{"id":1,"name":"Desk lamp"}}') {
  return `SECRET='whsec_…'   # the signing secret
BODY='${body}'
TS=$(date +%s)
SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')
curl -X POST '${url}' \\
  -H 'Content-Type: application/json' \\
  -H "X-CDC-Timestamp: $TS" \\
  -H "X-CDC-Signature: sha256=$SIG" \\
  -d "$BODY"`;
}
