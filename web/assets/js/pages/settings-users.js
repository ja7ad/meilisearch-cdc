// Settings → Users: accounts, roles, passwords and invitations (admin).
import { html, useEffect, useLayoutEffect, useState } from '../../vendor/htm-preact.js';
import { api } from '../api.js';
import { ROLES, auth, initials, passwordStrength } from '../auth.js';
import { useFetch } from '../hooks.js';
import { Icon } from '../icons.js';
import { Alert, Button, CopyField, Empty, ErrorBox, Field, Modal, RoleBadge, SkeletonRows, Tag, confirmDialog, toast, toastError } from '../ui.js';
import { PasswordField } from './auth.js';
import { fmtTime, isZeroTime, timeAgo } from '../util.js';

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const ROLE_HINT = {
  viewer: 'Reads everything except users, notifications settings and the audit log.',
  moderator: 'Viewer + task actions, diff/repair, DLQ, test connection, cancel Meilisearch tasks.',
  admin: 'Everything, including users, settings, alert rules and task definitions.',
};

// Why an action is locked for a row: the server enforces the same rules.
const OWNER_HINT = 'Only an operator on the server can transfer ownership (meilisearch-cdc admin users set-owner).';
const lockReason = {
  role: (u, self) => (self ? 'You cannot change your own role'
    : u.owner ? `The primary administrator's role cannot be changed. ${OWNER_HINT}` : undefined),
  del: (u, self) => (self ? 'You cannot delete yourself'
    : u.owner ? `The primary administrator cannot be deleted. ${OWNER_HINT}` : undefined),
  password: (u, self) => (!self && u.owner
    ? 'Only the primary administrator sets their password. An operator can reset it on the server (meilisearch-cdc admin users reset-password).' : undefined),
};

function RoleSelect({ id, value, onChange, disabled, title, label = 'Role' }) {
  return html`<select id=${id} class="select" value=${value} disabled=${disabled} title=${title} aria-label=${label} onChange=${(e) => onChange(e.currentTarget.value)}>
    ${ROLES.map((r) => html`<option value=${r}>${r[0].toUpperCase() + r.slice(1)}</option>`)}</select>`;
}

function AddUserModal({ open, onClose, onDone }) {
  const [f, setF] = useState({ email: '', name: '', role: 'viewer', password: '' });
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  useLayoutEffect(() => { if (open) { setF({ email: '', name: '', role: 'viewer', password: '' }); setTried(false); setErr(null); } }, [open]);
  const errs = { email: EMAIL_RE.test(f.email) ? '' : 'Enter a valid email address', password: passwordStrength(f.password).ok ? '' : 'Use 8 to 128 characters' };
  const save = async (e) => {
    e && e.preventDefault();
    setTried(true);
    if (errs.email || errs.password) return;
    setBusy(true); setErr(null);
    try { const u = await api.createUser({ ...f, email: f.email.trim(), name: f.name.trim() }); toast({ title: 'User added', message: u.email }); onDone(); onClose(); }
    catch (er) { setErr(er); }
    setBusy(false);
  };
  return html`<${Modal} open=${open} onClose=${onClose} title="Add user" size="sm"
    footer=${html`<${Button} onClick=${onClose}>Cancel</${Button}><${Button} kind="primary" icon="plus" loading=${busy} onClick=${save} data-testid="add-user-save">Add user</${Button}>`}>
    <form class="stack" onSubmit=${save} novalidate>
      <${ErrorBox} error=${err} />
      <${Field} label="Email" htmlFor="au-email" error=${tried && errs.email}><input id="au-email" type="email" inputmode="email" autocomplete="off" class="input" value=${f.email} onInput=${(e) => setF({ ...f, email: e.currentTarget.value })} /></${Field}>
      <${Field} label="Name" htmlFor="au-name"><input id="au-name" class="input" autocomplete="off" value=${f.name} onInput=${(e) => setF({ ...f, name: e.currentTarget.value })} /></${Field}>
      <${Field} label="Role" htmlFor="au-role" hint=${ROLE_HINT[f.role]}><${RoleSelect} id="au-role" value=${f.role} onChange=${(v) => setF({ ...f, role: v })} /></${Field}>
      <${PasswordField} id="au-pw" label="Initial password" value=${f.password} onInput=${(v) => setF({ ...f, password: v })} error=${tried && errs.password} />
      <button type="submit" hidden></button>
    </form>
  </${Modal}>`;
}

function InviteModal({ open, onClose, onDone }) {
  const [email, setEmail] = useState('');
  const [role, setRole] = useState('viewer');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const [result, setResult] = useState(null);
  useLayoutEffect(() => { if (open) { setEmail(''); setRole('viewer'); setErr(null); setResult(null); } }, [open]);
  const send = async (e) => {
    e && e.preventDefault();
    if (!EMAIL_RE.test(email)) { setErr({ message: 'Enter a valid email address' }); return; }
    setBusy(true); setErr(null);
    try { const r = await api.invite(email.trim(), role); setResult(r); onDone(); }
    catch (er) { setErr(er); }
    setBusy(false);
  };
  return html`<${Modal} open=${open} onClose=${onClose} title=${result ? 'Invitation created' : 'Invite user'} size="md"
    footer=${result ? html`<${Button} kind="primary" onClick=${onClose}>Done</${Button}>`
      : html`<${Button} onClick=${onClose}>Cancel</${Button}><${Button} kind="primary" icon="send" loading=${busy} onClick=${send} data-testid="invite-send">Create invitation</${Button}>`}>
    ${result ? html`<div class="stack" data-testid="invite-result">
        ${result.emailed ? html`<${Alert} tone="ok" title="Email sent">We emailed the invitation to <b>${result.invitation.email}</b>.</${Alert}>`
          : result.emailError ? html`<${Alert} tone="warn" title="The email could not be sent">${result.emailError}. Share the link below yourself.</${Alert}>`
          : html`<${Alert} tone="info" title="Share this link">Email is not configured, so send the link to <b>${result.invitation.email}</b> yourself.</${Alert}>`}
        <${Field} label="Invitation link" hint="Shown only once. Valid for 7 days, single use."><${CopyField} value=${result.link} label="invitation link" testid="invite-link" /></${Field}>
        <div class="row subtle" style="gap:6px">Joins as <${RoleBadge} role=${result.invitation.role} /> · expires ${fmtTime(result.invitation.expiresAt, true)}</div>
      </div>`
      : html`<form class="stack" onSubmit=${send} novalidate>
          <${ErrorBox} error=${err} />
          <p class="muted">The invitee picks their own name and password from a single-use link.</p>
          <${Field} label="Email" htmlFor="iv-email"><input id="iv-email" type="email" inputmode="email" autocomplete="off" class="input" value=${email} onInput=${(e) => setEmail(e.currentTarget.value)} /></${Field}>
          <${Field} label="Role" htmlFor="iv-role" hint=${ROLE_HINT[role]}><${RoleSelect} id="iv-role" value=${role} onChange=${setRole} /></${Field}>
          <button type="submit" hidden></button>
        </form>`}
  </${Modal}>`;
}

function SetPasswordModal({ user, onClose }) {
  const [pw, setPw] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  useLayoutEffect(() => { setPw(''); setErr(''); }, [user && user.id]);
  const save = async (e) => {
    e && e.preventDefault();
    if (!passwordStrength(pw).ok) { setErr('Use 8 to 128 characters'); return; }
    setBusy(true);
    try { await api.setUserPassword(user.id, pw); toast({ title: 'Password set', message: `${user.email} was signed out everywhere.` }); onClose(); }
    catch (er) { setErr(er.message); }
    setBusy(false);
  };
  return html`<${Modal} open=${!!user} onClose=${onClose} title=${user ? `Set password for ${user.name || user.email}` : ''} size="sm"
    footer=${html`<${Button} onClick=${onClose}>Cancel</${Button}><${Button} kind="primary" icon="key" loading=${busy} onClick=${save} data-testid="set-password-save">Set password</${Button}>`}>
    <form class="stack" onSubmit=${save}>
      <p class="muted">Their existing sessions end immediately.</p>
      <${PasswordField} id="sp-pw" label="New password" value=${pw} onInput=${setPw} error=${err} />
      <button type="submit" hidden></button>
    </form>
  </${Modal}>`;
}

export function UsersTab() {
  const users = useFetch(() => api.users(), []);
  const invites = useFetch(() => api.invitations(), []);
  const [modal, setModal] = useState('');
  const [pwUser, setPwUser] = useState(null);
  const me = auth.user || {};
  const setRole = async (u, role) => {
    try { const r = await api.updateUser(u.id, { role }); users.setData((l) => l.map((x) => (x.id === u.id ? r : x))); toast({ title: 'Role updated', message: `${u.email} is now ${role}` }); }
    catch (e) { toastError(e, 'Could not change role'); users.reload(true); }
  };
  const del = async (u) => {
    const ok = await confirmDialog({ title: `Delete ${u.name || u.email}?`, message: 'The account and its sessions are removed. Audit entries stay.', typed: u.email, danger: true, confirmLabel: 'Delete user' });
    if (!ok) return;
    try { await api.deleteUser(u.id); toast({ title: 'User deleted', message: u.email }); users.reload(true); } catch (e) { toastError(e, 'Could not delete user'); }
  };
  const revoke = async (i) => {
    const ok = await confirmDialog({ title: `Revoke the invitation for ${i.email}?`, message: 'The link stops working.', confirmLabel: 'Revoke', danger: true });
    if (!ok) return;
    try { await api.revokeInvitation(i.id); toast({ title: 'Invitation revoked', message: i.email }); invites.reload(true); } catch (e) { toastError(e); }
  };
  const list = users.data || [];
  return html`<div class="stack tab-panel">
    <section class="card">
      <div class="card-head"><h2><${Icon} name="users" />Users <span class="count-pill">${list.length}</span></h2>
        <div class="btn-group"><${Button} icon="send" onClick=${() => setModal('invite')} data-testid="invite-user">Invite user</${Button}>
          <${Button} kind="primary" icon="plus" onClick=${() => setModal('add')} data-testid="add-user">Add user</${Button}></div></div>
      <${ErrorBox} error=${users.error && !users.data && users.error} onRetry=${() => users.reload()} />
      ${!users.data ? html`<${SkeletonRows} rows=${3} cols=${4} />`
        : html`<div class="table-wrap"><table class="table rtable" data-testid="users-table">
          <thead><tr><th>User</th><th>Role</th><th>Last sign-in</th><th class="hide-sm">Created</th><th class="col-actions"><span class="sr-only">Actions</span></th></tr></thead>
          <tbody>${list.map((u, i) => {
            const self = u.id === me.id;
            const roleLock = lockReason.role(u, self), delLock = lockReason.del(u, self), pwLock = lockReason.password(u, self);
            return html`<tr key=${u.id} style=${`--i:${i}`} data-user=${u.email}>
              <td data-label="User"><div class="cell-title"><span class="avatar" aria-hidden="true">${initials(u)}</span>
                <div style="min-width:0"><b class="user-name">${u.name || u.email}${u.owner && html` <${Tag} tone="primary" title="Primary administrator: created the installation. Only an operator on the server can change this account's role or delete it.">Owner</${Tag}>`}${self && html` <${Tag} tone="violet">you</${Tag}>`}</b><small>${u.email}</small></div></div></td>
              <td data-label="Role"><${RoleSelect} id=${`role-${u.id}`} value=${u.role} label=${`Role of ${u.email}`} disabled=${!!roleLock} title=${roleLock} onChange=${(r) => setRole(u, r)} /></td>
              <td data-label="Last sign-in" class="nowrap" title=${isZeroTime(u.lastLoginAt) ? '' : fmtTime(u.lastLoginAt, true)}>${isZeroTime(u.lastLoginAt) ? html`<span class="subtle">never</span>` : timeAgo(u.lastLoginAt)}</td>
              <td data-label="Created" class="hide-sm subtle nowrap">${timeAgo(u.createdAt)}</td>
              <td class="col-actions"><div class="btn-group">
                <${Button} size="sm" icon="key" disabled=${!!pwLock} title=${pwLock} onClick=${() => setPwUser(u)} aria-label=${`Set password for ${u.email}`}>Set password</${Button}>
                <${Button} size="sm" kind="danger-ghost" icon="trash" disabled=${!!delLock} title=${delLock}
                  onClick=${() => del(u)} aria-label=${`Delete ${u.email}`} />
              </div></td>
            </tr>`;
          })}</tbody></table></div>`}
    </section>

    <section class="card">
      <div class="card-head"><h2><${Icon} name="mail" />Pending invitations</h2></div>
      ${!invites.data ? html`<${SkeletonRows} rows=${2} cols=${3} />`
        : !invites.data.length ? html`<${Empty} icon="send" title="No pending invitations">Invite teammates by email; they choose their own password.</${Empty}>`
        : html`<div class="table-wrap"><table class="table rtable" data-testid="invitations-table">
          <thead><tr><th>Email</th><th>Role</th><th>Invited by</th><th>Expires</th><th class="col-actions"><span class="sr-only">Actions</span></th></tr></thead>
          <tbody>${invites.data.map((x, i) => html`<tr key=${x.id} style=${`--i:${i}`}>
            <td data-label="Email"><b>${x.email}</b> ${x.emailed ? html`<${Tag} tone="ok">emailed</${Tag}>` : html`<${Tag}>link only</${Tag}>`}</td>
            <td data-label="Role"><${RoleBadge} role=${x.role} /></td>
            <td data-label="Invited by" class="subtle">${x.invitedBy}</td>
            <td data-label="Expires" class="nowrap" title=${fmtTime(x.expiresAt, true)}>${fmtTime(x.expiresAt, true)}</td>
            <td class="col-actions"><${Button} size="sm" kind="danger-ghost" icon="x" onClick=${() => revoke(x)} aria-label=${`Revoke invitation for ${x.email}`}>Revoke</${Button}></td>
          </tr>`)}</tbody></table></div>`}
    </section>

    <${AddUserModal} open=${modal === 'add'} onClose=${() => setModal('')} onDone=${() => users.reload(true)} />
    <${InviteModal} open=${modal === 'invite'} onClose=${() => setModal('')} onDone=${() => invites.reload(true)} />
    <${SetPasswordModal} user=${pwUser} onClose=${() => setPwUser(null)} />
  </div>`;
}
