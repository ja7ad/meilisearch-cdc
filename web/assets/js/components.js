// Domain components shared by several pages (source icons, task actions, metrics seeding).
import { html } from '../vendor/htm-preact.js';
import { api } from './api.js';
import { Icon } from './icons.js';
import { seedHistory } from './sse.js';
import { confirmDialog, toast, toastError } from './ui.js';
import { ACTION_LABELS, sourceInfo } from './util.js';

export function SourceIcon({ type, size = 'md' }) {
  const s = sourceInfo(type);
  return html`<span class=${`src-icon ${size === 'sm' ? 'sm' : ''}`} style=${`--hue:${s.hue}`} title=${s.label}>
    <${Icon} name=${s.icon} size=${size === 'sm' ? 15 : size === 'lg' ? 24 : 19} label=${s.label} /></span>`;
}

const CONFIRM = {
  stop: { message: 'The task stops reading changes. Its checkpoint is kept, so starting it again resumes where it stopped.' },
  restart: { message: 'The pipeline is stopped and started again from its checkpoint.' },
  resync: { message: 'All rows are copied from the source again. With zero-downtime resync enabled the copy goes into a shadow index that is swapped in when done.', danger: true },
  'reset-checkpoint': { message: 'The stored position is cleared. The next start takes a new snapshot (or streams from the current position in stream mode).', danger: true },
};

/** Runs a task action with an optional confirmation; returns the updated TaskView or null. */
export async function runTaskAction(task, action) {
  const c = CONFIRM[action];
  if (c) {
    const ok = await confirmDialog({ title: `${ACTION_LABELS[action]} “${task.name || task.id}”?`, message: c.message, confirmLabel: ACTION_LABELS[action], danger: c.danger });
    if (!ok) return null;
  }
  try {
    const v = await api.taskAction(task.id, action);
    toast({ title: `${ACTION_LABELS[action]}: ${task.name || task.id}`, message: v && v.status ? `State is now ${v.status.state}` : undefined });
    return v;
  } catch (e) { toastError(e, `Could not ${ACTION_LABELS[action].toLowerCase()} ${task.name || task.id}`); return null; }
}

/** Typed confirmation, then DELETE /tasks/{id}. Resolves true when deleted. */
export async function deleteTaskFlow(task) {
  const res = await confirmDialog({
    title: `Delete task “${task.name || task.id}”?`,
    message: 'The task definition and its checkpoint are removed. Documents already in Meilisearch stay unless you also delete the index.',
    typed: task.id, danger: true, confirmLabel: 'Delete task',
    options: [
      // On by default: a forgotten replication slot makes the database keep WAL forever.
      { id: 'dropSource', label: 'Drop source replication objects', checked: true,
        hint: 'PostgreSQL: removes the replication slot and publication created for this task, so the database stops keeping WAL for it. Keep them only if another task will reuse them.' },
      { id: 'deleteIndex', label: `Also delete index “${task.index && task.index.uid}”`, hint: 'Deletes every document in the target index.' },
    ],
  });
  if (!res) return false;
  try {
    await api.deleteTask(task.id, { dropSource: res.options.dropSource, deleteIndex: res.options.deleteIndex ? task.index.uid : undefined });
    toast({ title: 'Task deleted', message: task.name || task.id });
    return true;
  } catch (e) { toastError(e, 'Could not delete task'); return false; }
}

const seeded = new Set();
/** Fetch /tasks/{id}/metrics once per task so sparklines have history right away. */
export function seedMetrics(ids) {
  return Promise.all(ids.filter((id) => !seeded.has(id)).slice(0, 16).map(async (id) => {
    seeded.add(id);
    try { const m = await api.metrics(id); seedHistory(id, (m && m.points) || []); return [id, (m && m.points) || []]; } catch { seeded.delete(id); return [id, []]; }
  }));
}
