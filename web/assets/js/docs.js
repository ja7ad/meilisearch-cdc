// Links from the dashboard into the documentation site.
import { html } from '../vendor/htm-preact.js';
import { Icon } from './icons.js';

export const DOCS_BASE = 'https://meilicdc.javad.dev/guide/';

// Dashboard areas (and source types) to guide pages; `#anchor` suffixes are allowed.
const PAGES = {
  overview: 'monitoring', tasks: 'tasks', wizard: 'tasks', connectors: 'sources', indexes: 'indexes', 'meili-tasks': 'task-operations',
  destinations: 'destinations', alerts: 'alerts', settings: 'configuration', users: 'security', notify: 'alerts', mcp: 'mcp',
  audit: 'security', updates: 'installation', sources: 'sources',
};
const SOURCE_PAGE = { mariadb: 'source-mysql' };

/** Documentation URL for a dashboard area or a source type (`postgres`, `kafka`, …); falls back to the guide index. */
export function docUrl(key, source) {
  const page = (source && (SOURCE_PAGE[source] || `source-${source}`)) || PAGES[key] || '';
  return `${DOCS_BASE}${page ? `${page}.html` : ''}`;
}

/** Small “?” icon linking to the matching documentation page. */
export function HelpLink({ doc, source, label = 'Documentation' }) {
  const href = docUrl(doc, source);
  return html`<a class="help-link" href=${href} target="_blank" rel="noopener" aria-label=${label} title=${label} data-testid="help-link" data-doc=${source || doc}><${Icon} name="help" size=${16} /></a>`;
}
