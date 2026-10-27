// Background update check against the GitHub releases API. Preferences live in
// this browser's localStorage; the server is never involved.
import { useEffect, useState } from '../vendor/htm-preact.js';
import { auth } from './auth.js';
import { lsGet, lsSet } from './util.js';

const API = 'https://api.github.com/repos/ja7ad/meilisearch-cdc/releases/latest';
const KEY = { on: 'cdc.update.enabled', hours: 'cdc.update.hours', last: 'cdc.update.last', dismissed: 'cdc.update.dismissed' };
export const UPDATE_HOURS = [1, 6, 12, 24];

const subs = new Set();
let latest = null; // { tag, url, name }
let checking = false;
const emit = () => subs.forEach((f) => f());

export const updatePrefs = () => ({
  enabled: lsGet(KEY.on) !== 'off',
  hours: UPDATE_HOURS.includes(Number(lsGet(KEY.hours))) ? Number(lsGet(KEY.hours)) : 24,
  last: Number(lsGet(KEY.last)) || 0,
});

export function setUpdatePrefs({ enabled, hours }) {
  if (enabled != null) lsSet(KEY.on, enabled ? 'on' : 'off');
  if (hours != null) lsSet(KEY.hours, String(hours));
  emit();
}

const parse = (v) => {
  const m = /^v?(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?/.exec(String(v || ''));
  return m ? { n: [+m[1], +m[2], +m[3]], pre: !!m[4] } : null;
};

/** True when `b` is a newer release than `a` (prereleases rank below their release). */
export function isNewer(a, b) {
  const x = parse(a); const y = parse(b);
  if (!x || !y) return false;
  for (let i = 0; i < 3; i++) if (x.n[i] !== y.n[i]) return y.n[i] > x.n[i];
  return x.pre && !y.pre;
}

/** Asks GitHub for the latest release. Resolves to the update when newer than this build, else null. */
export async function checkForUpdate() {
  if (checking) return latest;
  checking = true;
  try {
    const res = await fetch(API, { headers: { Accept: 'application/vnd.github+json' } });
    if (!res.ok) throw new Error(`GitHub responded ${res.status}`);
    const r = await res.json();
    lsSet(KEY.last, String(Date.now()));
    latest = isNewer(auth.config && auth.config.version, r.tag_name) ? { tag: r.tag_name, url: r.html_url, name: r.name || r.tag_name } : null;
    emit();
    return latest;
  } finally { checking = false; }
}

/** Runs the periodic check while the dashboard is open. */
export function useUpdateChecker() {
  useEffect(() => {
    const tick = () => {
      const p = updatePrefs();
      if (p.enabled && Date.now() - p.last >= p.hours * 3600e3) checkForUpdate().catch(() => {});
    };
    tick();
    const t = setInterval(tick, 60e3);
    return () => clearInterval(t);
  }, []);
}

export function useUpdate() {
  const [, set] = useState(0);
  useEffect(() => { const f = () => set((n) => n + 1); subs.add(f); return () => subs.delete(f); }, []);
  return {
    prefs: updatePrefs(),
    latest,
    prompt: !!latest && updatePrefs().enabled && lsGet(KEY.dismissed) !== latest.tag,
    dismiss: () => { if (latest) lsSet(KEY.dismissed, latest.tag); emit(); },
  };
}
