// Hash router: #/overview, #/tasks, #/tasks/new, #/tasks/:id[/:tab], #/tasks/:id/edit,
// #/indexes, #/indexes/:uid[/:tab], #/meili-tasks, #/alerts, #/settings[/:tab],
// and the public auth pages #/setup, #/login, #/forgot, #/reset/:token, #/invite/:token.
import { useEffect, useState } from '../vendor/htm-preact.js';

function parseHash() {
  const h = (location.hash || '#/overview').replace(/^#/, '');
  const [path, query = ''] = h.split('?');
  const parts = path.split('/').filter(Boolean).map(decodeURIComponent);
  return { path: '/' + parts.join('/'), parts, query: new URLSearchParams(query), hash: `#${h}` };
}

export const PUBLIC_PAGES = new Set(['setup', 'login', 'forgot', 'reset', 'invite']);

export function matchRoute(r) {
  const [a, b, c] = r.parts;
  switch (a) {
    case undefined: case 'overview': return { page: 'overview' };
    case 'tasks':
      if (!b) return { page: 'tasks' };
      if (b === 'new') return { page: 'wizard', connector: r.query.get('connector') || '', table: r.query.get('table') || '', schema: r.query.get('schema') || '', presetDestination: r.query.get('destination') || '' };
      if (c === 'edit') return { page: 'wizard', id: b };
      return { page: 'task', id: b, tab: c || 'overview' };
    case 'destinations': return b ? { page: 'destination', id: b, tab: c || 'overview', query: r.query } : { page: 'destinations' };
    case 'welcome': return { page: 'welcome' };
    case 'connectors': return b ? { page: 'connector', id: b } : { page: 'connectors' };
    case 'indexes': return b ? { page: 'index', uid: b, tab: c || 'search', destination: r.query.get('destination') || '' } : { page: 'indexes', instance: r.query.get('instance') || '' };
    case 'meili-tasks': return { page: 'meili-tasks' };
    case 'alerts': return { page: 'alerts' };
    case 'settings': return { page: 'settings', tab: b || 'general' };
    case 'setup': return { page: 'setup' };
    case 'login': return { page: 'login', next: r.query.get('next') || '' };
    case 'forgot': return { page: 'forgot' };
    case 'reset': return { page: 'reset', token: b || '' };
    case 'invite': return { page: 'invite', token: b || '' };
    default: return { page: 'notfound' };
  }
}

export function useRoute() {
  const [r, setR] = useState(parseHash);
  useEffect(() => {
    const on = () => setR(parseHash());
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  return r;
}

export const navigate = (hash) => { location.hash = hash; };
/** Replace the current history entry (redirects should not add a back step). */
export const redirect = (hash) => { location.replace(`${location.pathname}${location.search}${hash}`); };
export const currentHash = () => location.hash || '#/overview';
