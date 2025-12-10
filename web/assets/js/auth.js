// Current user, auth config and role-based permissions.
//
// can(action) is the single place that decides what the UI offers; the server
// still enforces every call (403s are shown as toasts).
import { useEffect, useState } from '../vendor/htm-preact.js';

export const ROLES = ['viewer', 'moderator', 'admin'];
const RANK = { viewer: 0, moderator: 1, admin: 2 };

/** Minimum role per UI action (mirrors the role table in docs/api.md). */
const PERMS = {
  'task.action': 'moderator', // start/stop/pause/resume/restart/resync/reset-checkpoint, batch of those
  'task.diff': 'moderator', // run diff / repair
  'task.dlq': 'moderator', // replay / discard dead letters
  'task.test': 'moderator', // test connection
  'meili.cancel': 'moderator',
  'connector.test': 'moderator',
  'connector.write': 'admin',
  'task.create': 'admin',
  'task.edit': 'admin',
  'task.delete': 'admin',
  'task.import': 'admin',
  'index.write': 'admin', // create/delete/settings/primary key/swap/compact
  'instance.write': 'admin', // dumps, snapshots, experimental features
  users: 'admin',
  'settings.notify': 'admin', // SMTP, Telegram
  'settings.mcp': 'admin',
  'alerts.write': 'admin',
  audit: 'admin',
};

export const auth = { ready: false, config: null, user: null };
const subs = new Set();
const emit = () => subs.forEach((f) => f({ ...auth }));

export function setAuth(patch) { Object.assign(auth, patch); emit(); }

export function useAuth() {
  const [s, set] = useState({ ...auth });
  useEffect(() => { subs.add(set); return () => subs.delete(set); }, []);
  return s;
}

export const roleOf = () => (auth.user && auth.user.role) || 'viewer';
export const can = (action) => !!auth.user && RANK[roleOf()] >= RANK[PERMS[action] || 'admin'];
export const needs = (action) => PERMS[action] || 'admin';
/** Props for a control the current role may not use: disabled + explanatory tooltip. */
export const gate = (action) => (can(action) ? {} : { disabled: true, title: `Requires ${needs(action)} role`, 'aria-disabled': 'true' });
export const isApiToken = () => !!auth.user && auth.user.id === 'api-token';

export function initials(u) {
  const src = (u && (u.name || u.email)) || '?';
  const parts = src.replace(/@.*/, '').split(/[\s._-]+/).filter(Boolean);
  return ((parts[0] || '?')[0] + (parts[1] ? parts[1][0] : '')).toUpperCase();
}

/** Password rules from the API: 8-128 characters. Returns {ok, score 0-4, label, hint}. */
export function passwordStrength(p) {
  const len = (p || '').length;
  if (!len) return { ok: false, score: 0, label: '', hint: 'Use 8 to 128 characters.' };
  if (len < 8) return { ok: false, score: 1, label: 'Too short', hint: `${8 - len} more character${8 - len === 1 ? '' : 's'} needed.` };
  if (len > 128) return { ok: false, score: 1, label: 'Too long', hint: 'Use at most 128 characters.' };
  let score = 1;
  if (len >= 12) score++;
  if (/[a-z]/.test(p) && /[A-Z]/.test(p)) score++;
  if (/\d/.test(p) && /[^A-Za-z0-9]/.test(p)) score++;
  const label = ['', 'Weak', 'Fair', 'Good', 'Strong'][Math.min(4, score)];
  return { ok: true, score: Math.min(4, score), label, hint: score < 3 ? 'Longer passphrases are stronger.' : 'Looks good.' };
}
