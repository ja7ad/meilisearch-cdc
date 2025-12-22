// Destinations store: the list of Meilisearch instances, refreshed every 15s and after changes.
import { useEffect, useState } from '../vendor/htm-preact.js';
import { api, getDestination } from './api.js';
import { auth } from './auth.js';

const state = { list: null, error: null };
const subs = new Set();
let timer = null;
let inflight = null;

export function refreshDestinations() {
  if (!auth.user) return Promise.resolve(null);
  if (!inflight) {
    inflight = api.destinations().then((l) => { state.list = l; state.error = null; }).catch((e) => { state.error = e; })
      .finally(() => { inflight = null; subs.forEach((f) => f({ ...state })); });
  }
  return inflight;
}
// live health from SSE patches the cached list (no polling needed)
window.addEventListener('cdc:dest-health', (e) => {
  const { id, health } = e.detail || {};
  if (!state.list) return;
  state.list = state.list.map((d) => (d.id === id ? { ...d, health } : d));
  subs.forEach((f) => f({ ...state }));
});

export const destinationsChanged = () => { refreshDestinations(); window.dispatchEvent(new CustomEvent('cdc:destinations')); };
export const destinationsSnapshot = () => state.list;

/** Live list of DestinationView (null while loading). */
export function useDestinations() {
  const [s, set] = useState({ ...state });
  useEffect(() => {
    subs.add(set);
    if (!state.list) refreshDestinations();
    // usedBy/sync change slowly; health arrives over SSE
    if (!timer) timer = setInterval(refreshDestinations, 60000);
    return () => { subs.delete(set); if (!subs.size) { clearInterval(timer); timer = null; } };
  }, []);
  return s.list;
}

export const singleDestination = () => !!(auth.config && auth.config.singleDestination);
export const defaultDestination = (list) => (list || []).find((d) => d.isDefault) || null;
/** The destination the scoped pages use: the switcher choice, else the default. */
export function selectedDestination(list) {
  const id = getDestination();
  return (list || []).find((d) => d.id === id) || defaultDestination(list);
}
export const destinationName = (list, id) => { const d = (list || []).find((x) => x.id === id); return d ? d.name : id || 'default'; };
export const hostOf = (url) => { try { return new URL(url).host; } catch { return url || ''; } };
