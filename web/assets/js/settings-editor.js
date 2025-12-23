// Full Meilisearch index settings editor: form sections plus a JSON tab kept in sync both ways.
//
// `value` holds only the keys the user set (unset = Meilisearch default). Used by the task
// wizard, the task "Index settings" tab and the index detail Settings tab.
import { html, useEffect, useRef, useState } from '../vendor/htm-preact.js';
import { Icon } from './icons.js';
import { Alert, Button, ChipsInput, Field, Toggle } from './ui.js';
import { cls, pretty } from './util.js';

export const DEFAULT_RANKING = ['words', 'typo', 'proximity', 'attributeRank', 'sort', 'wordPosition', 'exactness'];
const BUILTIN_RULES = new Set([...DEFAULT_RANKING, 'attribute']);
const LOCALES = ['eng', 'fra', 'deu', 'spa', 'ita', 'por', 'nld', 'swe', 'dan', 'fin', 'pol', 'ces', 'ron', 'hun', 'tur', 'ell', 'rus', 'ukr', 'ara', 'heb', 'pes', 'hin', 'tha', 'vie', 'ind', 'jpn', 'kor', 'cmn'];

const SECTIONS = [
  { id: 'attributes', title: 'Attributes', icon: 'layers', keys: ['searchableAttributes', 'displayedAttributes', 'filterableAttributes', 'sortableAttributes', 'distinctAttribute'], help: 'Which fields are searched, returned, filtered, sorted and deduplicated.' },
  { id: 'ranking', title: 'Ranking rules', icon: 'sparkle', keys: ['rankingRules'], help: 'The order in which relevancy criteria break ties.' },
  { id: 'typo', title: 'Typo tolerance', icon: 'edit', keys: ['typoTolerance'], help: 'How many typos a query word may contain and where typos are not allowed.' },
  { id: 'synonyms', title: 'Synonyms', icon: 'swap', keys: ['synonyms'], help: 'Words that should match each other.' },
  { id: 'words', title: 'Words & tokens', icon: 'tag', keys: ['stopWords', 'dictionary', 'separatorTokens', 'nonSeparatorTokens'], help: 'Ignored words, custom dictionary entries and word separators.' },
  { id: 'search', title: 'Facets, pagination & search', icon: 'gauge', keys: ['faceting', 'pagination', 'proximityPrecision', 'searchCutoffMs', 'prefixSearch', 'facetSearch'], help: 'Facet limits, result window and query-time behaviour.' },
  { id: 'localized', title: 'Localized attributes', icon: 'info', keys: ['localizedAttributes'], help: 'Force the language used to tokenize some fields.' },
  { id: 'embedders', title: 'Embedders', icon: 'robot', keys: ['embedders'], help: 'Vector embeddings for semantic and hybrid search.' },
];

const without = (o, k) => { const c = { ...o }; delete c[k]; return c; };
const uniq = (a) => [...new Set(a)];
const filterName = (f) => (typeof f === 'string' ? f : (f && f.attributePatterns && f.attributePatterns.join(', ')) || '');

// ---------------------------------------------------------------- building blocks

/** Ordered list with drag & drop and keyboard friendly move buttons. */
export function OrderedList({ items, onChange, label, render = (x) => x, testid }) {
  const [drag, setDrag] = useState(null);
  const [over, setOver] = useState(null);
  const move = (from, to) => {
    if (to < 0 || to >= items.length || from === to) return;
    const a = items.slice();
    const [x] = a.splice(from, 1);
    a.splice(to, 0, x);
    onChange(a);
  };
  return html`<ol class="olist" aria-label=${label} data-testid=${testid}>
    ${items.map((it, i) => html`<li key=${typeof it === 'string' ? it : i} class=${cls('olist-item', drag === i && 'is-dragging', over === i && drag !== i && 'is-over')} draggable="true"
        onDragStart=${(e) => { setDrag(i); e.dataTransfer.effectAllowed = 'move'; try { e.dataTransfer.setData('text/plain', String(i)); } catch { /* ignore */ } }}
        onDragOver=${(e) => { e.preventDefault(); setOver(i); }} onDragLeave=${() => setOver(null)}
        onDrop=${(e) => { e.preventDefault(); if (drag != null) move(drag, i); setDrag(null); setOver(null); }} onDragEnd=${() => { setDrag(null); setOver(null); }}>
      <span class="olist-grip" aria-hidden="true"><${Icon} name="more" size=${16} /></span>
      <span class="olist-num">${i + 1}</span>
      <span class="olist-label">${render(it)}</span>
      <span class="olist-btns">
        <button type="button" class="btn btn-ghost btn-sm btn-icon" disabled=${i === 0} aria-label=${`Move ${typeof it === 'string' ? it : i + 1} up`} onClick=${() => move(i, i - 1)}><${Icon} name="chevron-left" size=${15} class="rot90" /></button>
        <button type="button" class="btn btn-ghost btn-sm btn-icon" disabled=${i === items.length - 1} aria-label=${`Move ${typeof it === 'string' ? it : i + 1} down`} onClick=${() => move(i, i + 1)}><${Icon} name="chevron-down" size=${15} /></button>
        <button type="button" class="btn btn-ghost btn-sm btn-icon" aria-label=${`Remove ${typeof it === 'string' ? it : i + 1}`} onClick=${() => onChange(items.filter((_, j) => j !== i))}><${Icon} name="x" size=${14} /></button>
      </span>
    </li>`)}
  </ol>`;
}

/** Text input with suggestions (datalist) and an Add button. */
function Adder({ id, placeholder, suggestions = [], onAdd, label, exclude = [] }) {
  const [v, setV] = useState('');
  const opts = suggestions.filter((s) => !exclude.includes(s));
  const add = () => { const x = v.trim(); if (x) { onAdd(x); setV(''); } };
  return html`<div class="adder">
    <input id=${id} class="input input-sm mono" list=${`${id}-list`} value=${v} placeholder=${placeholder} aria-label=${label} autocapitalize="off" spellcheck="false"
      onInput=${(e) => setV(e.currentTarget.value)} onKeyDown=${(e) => { if (e.key === 'Enter') { e.preventDefault(); add(); } }} />
    <datalist id=${`${id}-list`}>${opts.map((s) => html`<option value=${s} />`)}</datalist>
    <${Button} size="sm" icon="plus" onClick=${add} disabled=${!v.trim()} aria-label=${`Add to ${label}`}>Add</${Button}>
  </div>`;
}

function NumberInput({ id, value, onChange, placeholder, min = 0, label }) {
  return html`<input id=${id} class="input" type="number" inputmode="numeric" min=${min} value=${value ?? ''} placeholder=${placeholder} aria-label=${label}
    onInput=${(e) => { const t = e.currentTarget.value; onChange(t === '' ? undefined : Number(t)); }} />`;
}

// ---------------------------------------------------------------- sections

function QuickFields({ value, set, fields }) {
  const has = (k, f) => {
    const v = value[k];
    if (!Array.isArray(v)) return false;
    return k === 'filterableAttributes' ? v.some((x) => (typeof x === 'string' ? x === f : x && (x.attributePatterns || []).includes(f))) : v.includes(f);
  };
  const toggle = (k, f) => {
    let arr = Array.isArray(value[k]) ? value[k].filter((x) => !(k === 'searchableAttributes' && x === '*')) : [];
    if (has(k, f)) arr = arr.filter((x) => (typeof x === 'string' ? x !== f : !(x.attributePatterns || []).includes(f)));
    else arr = [...arr, f];
    set(k, arr.length ? arr : undefined);
  };
  if (!fields.length) return html`<p class="subtle" style="font-size:12.5px">Test the source connection to list fields, or add them below.</p>`;
  const KEYS = [['filterableAttributes', 'Filter'], ['sortableAttributes', 'Sort'], ['searchableAttributes', 'Search']];
  return html`<div class="card" style="box-shadow:none;overflow:hidden">
    <div class="settings-helper" role="table" aria-label="Field settings helper">
      <div class="hd" role="columnheader">Field</div>${KEYS.map(([, l]) => html`<div class="hd c" role="columnheader">${l}</div>`)}
      ${fields.map((f) => html`<div class="mono" role="cell" style="overflow:hidden;text-overflow:ellipsis">${f}</div>
        ${KEYS.map(([k, l]) => html`<div class="c" role="cell"><input type="checkbox" class="checkbox" aria-label=${`${l}: ${f}`} checked=${has(k, f)} onChange=${() => toggle(k, f)} /></div>`)}`)}
    </div></div>`;
}

function OrderedAttrs({ k, title, help, value, set, fields, p }) {
  const v = value[k];
  const all = v === undefined || (Array.isArray(v) && v.length === 1 && v[0] === '*');
  const list = Array.isArray(v) ? v.filter((x) => x !== '*') : [];
  return html`<div class="se-field">
    <div class="row-between"><div><b>${title}</b><p class="se-help">${help}</p></div>
      <${Toggle} id=${`${p}-${k}-all`} label="All attributes" checked=${all} onChange=${(on) => set(k, on ? (v === undefined ? undefined : ['*']) : (fields.length ? fields.slice(0, 3) : []))} /></div>
    ${!all && html`<${OrderedList} items=${list} label=${title} onChange=${(a) => set(k, a)} testid=${`${p}-${k}`} />
      <${Adder} id=${`${p}-${k}-add`} label=${title} placeholder="field" suggestions=${fields} exclude=${list} onAdd=${(x) => set(k, uniq([...list, x]))} />`}
  </div>`;
}

function FilterableEditor({ value, set, fields, p }) {
  const list = Array.isArray(value.filterableAttributes) ? value.filterableAttributes : [];
  const upd = (i, x) => set('filterableAttributes', list.map((y, j) => (j === i ? x : y)));
  const toObj = (f) => ({ attributePatterns: [filterName(f)], features: { facetSearch: false, filter: { equality: true, comparison: false } } });
  return html`<div class="se-field">
    <b>Filterable</b><p class="se-help">Fields usable in filters and facets. “Advanced” enables per field features (granular form).</p>
    <ul class="filter-list" data-testid=${`${p}-filterable`}>${list.map((f, i) => {
      const adv = typeof f === 'object';
      const feat = adv ? f.features || {} : null;
      const name = filterName(f);
      return html`<li key=${name + i} class=${cls('filter-row', adv && 'is-adv')}>
        <span class="mono filter-name">${name}</span>
        <label class="check-label small"><input type="checkbox" class="checkbox" checked=${adv} aria-label=${`Advanced filter settings for ${name}`} onChange=${() => upd(i, adv ? name : toObj(f))} />Advanced</label>
        <button type="button" class="btn btn-ghost btn-sm btn-icon" aria-label=${`Remove filterable ${name}`} onClick=${() => set('filterableAttributes', list.filter((_, j) => j !== i).length ? list.filter((_, j) => j !== i) : undefined)}><${Icon} name="x" size=${14} /></button>
        ${adv && html`<div class="filter-features">
          <label class="check-label small"><input type="checkbox" class="checkbox" checked=${!!feat.facetSearch} aria-label=${`${name}: facet search`} onChange=${(e) => upd(i, { ...f, features: { ...feat, facetSearch: e.currentTarget.checked } })} />Facet search</label>
          <label class="check-label small"><input type="checkbox" class="checkbox" checked=${!!(feat.filter && feat.filter.equality)} aria-label=${`${name}: equality filters`} onChange=${(e) => upd(i, { ...f, features: { ...feat, filter: { ...(feat.filter || {}), equality: e.currentTarget.checked } } })} />Equality (=, !=, IN)</label>
          <label class="check-label small"><input type="checkbox" class="checkbox" checked=${!!(feat.filter && feat.filter.comparison)} aria-label=${`${name}: comparison filters`} onChange=${(e) => upd(i, { ...f, features: { ...feat, filter: { ...(feat.filter || {}), comparison: e.currentTarget.checked } } })} />Comparison (&lt;, &gt;, TO)</label>
        </div>`}
      </li>`;
    })}</ul>
    <${Adder} id=${`${p}-filterable-add`} label="Filterable" placeholder="field or pattern (e.g. attrs.*)" suggestions=${fields} exclude=${list.map(filterName)} onAdd=${(x) => set('filterableAttributes', [...list, x])} />
  </div>`;
}

function AttributesSection({ value, set, fields, p }) {
  return html`<div class="stack">
    <${Field} label="Quick add"><${QuickFields} value=${value} set=${set} fields=${fields} /></${Field}>
    <${OrderedAttrs} k="searchableAttributes" title="Searchable" help="Searched fields, in order of importance." value=${value} set=${set} fields=${fields} p=${p} />
    <${FilterableEditor} value=${value} set=${set} fields=${fields} p=${p} />
    <div class="se-field"><b>Sortable</b><p class="se-help">Fields usable in the sort parameter.</p>
      <${ChipsInput} id=${`${p}-sortable`} label="Sortable attributes" values=${value.sortableAttributes || []} placeholder="price, rating…" onChange=${(v) => set('sortableAttributes', v.length ? v : undefined)} /></div>
    <${OrderedAttrs} k="displayedAttributes" title="Displayed" help="Fields returned in search results." value=${value} set=${set} fields=${fields} p=${p} />
    <${Field} label="Distinct attribute" htmlFor=${`${p}-distinct`} hint="Return at most one document per value of this field.">
      <select id=${`${p}-distinct`} class="select" value=${value.distinctAttribute || ''} onChange=${(e) => set('distinctAttribute', e.currentTarget.value || undefined)}>
        <option value="">None</option>${uniq([...fields, ...(value.distinctAttribute ? [value.distinctAttribute] : [])]).map((f) => html`<option value=${f}>${f}</option>`)}</select></${Field}>
  </div>`;
}

function RankingSection({ value, set, p }) {
  const rules = value.rankingRules || DEFAULT_RANKING;
  const [field, setField] = useState('');
  const [dir, setDir] = useState('desc');
  const missing = DEFAULT_RANKING.filter((r) => !rules.includes(r) && !(r === 'attributeRank' && rules.includes('attribute')));
  return html`<div class="stack-sm">
    <p class="se-help">Drag or use the arrows to reorder. Custom rules sort by a numeric or date field.</p>
    <${OrderedList} items=${rules} label="Ranking rules" onChange=${(a) => set('rankingRules', a)} testid=${`${p}-ranking`}
      render=${(r) => (BUILTIN_RULES.has(r) ? html`<span class="mono">${r}</span>` : html`<span class="mono">${r}</span> <span class="tag tag-violet">custom</span>`)} />
    <div class="row">
      <input class="input input-sm mono" style="max-width:200px" value=${field} placeholder="release_date" aria-label="Custom rule field" onInput=${(e) => setField(e.currentTarget.value)} />
      <select class="select select-sm" style="width:auto" value=${dir} aria-label="Custom rule direction" onChange=${(e) => setDir(e.currentTarget.value)}><option value="desc">desc</option><option value="asc">asc</option></select>
      <${Button} size="sm" icon="plus" disabled=${!field.trim()} onClick=${() => { set('rankingRules', [...rules, `${field.trim()}:${dir}`]); setField(''); }}>Add custom rule</${Button}>
      ${missing.map((r) => html`<${Button} size="sm" kind="ghost" icon="plus" onClick=${() => set('rankingRules', [...rules, r])}>${r}</${Button}>`)}
      ${value.rankingRules && html`<${Button} size="sm" kind="ghost" icon="restart" onClick=${() => set('rankingRules', undefined)}>Reset to default</${Button}>`}
    </div>
  </div>`;
}

function TypoSection({ value, set, p }) {
  const t = value.typoTolerance || {};
  const up = (patch) => set('typoTolerance', { ...t, ...patch });
  const mw = t.minWordSizeForTypos || {};
  return html`<div class="stack">
    <${Toggle} id=${`${p}-typo-on`} label="Typo tolerance" hint="Match words with small spelling mistakes." checked=${t.enabled !== false} onChange=${(v) => up({ enabled: v })} />
    <div class="form-grid">
      <${Field} label="Min word size for 1 typo" htmlFor=${`${p}-typo1`}><${NumberInput} id=${`${p}-typo1`} value=${mw.oneTypo} placeholder="5" onChange=${(n) => up({ minWordSizeForTypos: { ...mw, oneTypo: n } })} /></${Field}>
      <${Field} label="Min word size for 2 typos" htmlFor=${`${p}-typo2`}><${NumberInput} id=${`${p}-typo2`} value=${mw.twoTypos} placeholder="9" onChange=${(n) => up({ minWordSizeForTypos: { ...mw, twoTypos: n } })} /></${Field}>
    </div>
    <${Field} label="Disable on words" hint="Exact match required (brand names, SKUs)."><${ChipsInput} id=${`${p}-typo-words`} label="Disable typos on words" values=${t.disableOnWords || []} onChange=${(v) => up({ disableOnWords: v })} /></${Field}>
    <${Field} label="Disable on attributes"><${ChipsInput} id=${`${p}-typo-attrs`} label="Disable typos on attributes" values=${t.disableOnAttributes || []} onChange=${(v) => up({ disableOnAttributes: v })} /></${Field}>
    <${Toggle} id=${`${p}-typo-num`} label="Disable on numbers" hint="Numbers must match exactly." checked=${!!t.disableOnNumbers} onChange=${(v) => up({ disableOnNumbers: v })} />
  </div>`;
}

function SynonymsSection({ value, set, p }) {
  const syn = value.synonyms || {};
  const [word, setWord] = useState('');
  const [list, setList] = useState([]);
  const [mutual, setMutual] = useState(true);
  const add = () => {
    const w = word.trim();
    if (!w || !list.length) return;
    const next = { ...syn, [w]: uniq([...(syn[w] || []), ...list]) };
    if (mutual) for (const s of list) next[s] = uniq([...(next[s] || []), w, ...list.filter((x) => x !== s)]);
    set('synonyms', next); setWord(''); setList([]);
  };
  const rows = Object.entries(syn);
  return html`<div class="stack-sm">
    ${rows.length > 0 && html`<ul class="syn-list" data-testid=${`${p}-synonyms`}>${rows.map(([w, l]) => html`<li key=${w} class="syn-row" data-word=${w}>
      <span class="mono syn-word">${w}</span><${Icon} name="arrow-right" size=${14} />
      <${ChipsInput} id=${`${p}-syn-${w}`} label=${`Synonyms of ${w}`} values=${l} onChange=${(v) => set('synonyms', v.length ? { ...syn, [w]: v } : without(syn, w))} />
      <button type="button" class="btn btn-ghost btn-sm btn-icon" aria-label=${`Remove synonyms of ${w}`} onClick=${() => { const n = without(syn, w); set('synonyms', Object.keys(n).length ? n : undefined); }}><${Icon} name="x" size=${14} /></button>
    </li>`)}</ul>`}
    <div class="syn-add">
      <input class="input input-sm mono" value=${word} placeholder="word" aria-label="New synonym word" onInput=${(e) => setWord(e.currentTarget.value)} />
      <${ChipsInput} id=${`${p}-syn-new`} label="New synonyms" values=${list} placeholder="synonyms, then Enter" onChange=${setList} />
      <label class="check-label small"><input type="checkbox" class="checkbox" checked=${mutual} onChange=${(e) => setMutual(e.currentTarget.checked)} />Mutual</label>
      <${Button} size="sm" icon="plus" onClick=${add} disabled=${!word.trim() || !list.length} data-testid=${`${p}-syn-addbtn`}>Add</${Button}>
    </div>
    <p class="se-help">Mutual writes every direction: “phone ↔ mobile” also maps mobile to phone.</p>
  </div>`;
}

function WordsSection({ value, set, p }) {
  const chips = (k, label, hint) => html`<${Field} label=${label} hint=${hint}><${ChipsInput} id=${`${p}-${k}`} label=${label} values=${value[k] || []} onChange=${(v) => set(k, v.length ? v : undefined)} /></${Field}>`;
  return html`<div class="stack">
    ${chips('stopWords', 'Stop words', 'Ignored in queries and documents (the, a, of…).')}
    ${chips('dictionary', 'Dictionary', 'Strings kept as a single word (J.R.R., C++).')}
    ${chips('separatorTokens', 'Separator tokens', 'Extra characters that split words.')}
    ${chips('nonSeparatorTokens', 'Non-separator tokens', 'Characters that should not split words (@, #).')}
  </div>`;
}

function SearchSection({ value, set, p }) {
  const fac = value.faceting || {};
  const sortBy = Object.entries(fac.sortFacetValuesBy || {});
  const setFac = (patch) => set('faceting', { ...fac, ...patch });
  return html`<div class="stack">
    <div class="form-grid">
      <${Field} label="Max values per facet" htmlFor=${`${p}-mvpf`}><${NumberInput} id=${`${p}-mvpf`} value=${fac.maxValuesPerFacet} placeholder="100" onChange=${(n) => setFac({ maxValuesPerFacet: n })} /></${Field}>
      <${Field} label="Max total hits" htmlFor=${`${p}-mth`} hint="Largest result window for pagination."><${NumberInput} id=${`${p}-mth`} value=${value.pagination && value.pagination.maxTotalHits} placeholder="1000"
        onChange=${(n) => set('pagination', n === undefined ? undefined : { maxTotalHits: n })} /></${Field}>
      <${Field} label="Proximity precision" htmlFor=${`${p}-prox`}><select id=${`${p}-prox`} class="select" value=${value.proximityPrecision || ''} onChange=${(e) => set('proximityPrecision', e.currentTarget.value || undefined)}>
        <option value="">Default (byWord)</option><option value="byWord">byWord — precise</option><option value="byAttribute">byAttribute — faster indexing</option></select></${Field}>
      <${Field} label="Search cutoff (ms)" htmlFor=${`${p}-cutoff`} hint="Stop ranking after this time."><${NumberInput} id=${`${p}-cutoff`} value=${value.searchCutoffMs ?? undefined} placeholder="1500" onChange=${(n) => set('searchCutoffMs', n)} /></${Field}>
      <${Field} label="Prefix search" htmlFor=${`${p}-prefix`}><select id=${`${p}-prefix`} class="select" value=${value.prefixSearch || ''} onChange=${(e) => set('prefixSearch', e.currentTarget.value || undefined)}>
        <option value="">Default (indexingTime)</option><option value="indexingTime">indexingTime</option><option value="disabled">disabled</option></select></${Field}>
      <div style="padding-top:24px"><${Toggle} id=${`${p}-facetsearch`} label="Facet search" hint="Allow searching inside facet values." checked=${value.facetSearch !== false} onChange=${(v) => set('facetSearch', v)} /></div>
    </div>
    <${Field} label="Sort facet values by" hint="Per facet, or * for all facets.">
      <div class="stack-sm">${sortBy.map(([k, v]) => html`<div class="row" key=${k}><span class="mono" style="min-width:120px">${k}</span>
        <select class="select select-sm" style="width:auto" value=${v} aria-label=${`Sort ${k} facet values by`} onChange=${(e) => setFac({ sortFacetValuesBy: { ...fac.sortFacetValuesBy, [k]: e.currentTarget.value } })}><option value="alpha">alpha</option><option value="count">count</option></select>
        <button type="button" class="btn btn-ghost btn-sm btn-icon" aria-label=${`Remove facet sort for ${k}`} onClick=${() => setFac({ sortFacetValuesBy: without(fac.sortFacetValuesBy, k) })}><${Icon} name="x" size=${14} /></button></div>`)}
        <${Adder} id=${`${p}-sortfacet`} label="Facet sort" placeholder="facet or *" onAdd=${(x) => setFac({ sortFacetValuesBy: { ...(fac.sortFacetValuesBy || {}), [x]: 'count' } })} />
      </div></${Field}>
  </div>`;
}

function LocalizedSection({ value, set, p }) {
  const rows = Array.isArray(value.localizedAttributes) ? value.localizedAttributes : [];
  const upd = (i, patch) => set('localizedAttributes', rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return html`<div class="stack-sm">
    ${rows.map((r, i) => html`<div class="loc-row" key=${i}>
      <${Field} label="Attribute patterns"><${ChipsInput} id=${`${p}-loc-a-${i}`} label=${`Localized attribute patterns ${i + 1}`} values=${r.attributePatterns || []} placeholder="title_ja, *_fr" onChange=${(v) => upd(i, { attributePatterns: v })} /></${Field}>
      <${Field} label="Locales" hint=${`ISO 639-3, e.g. ${LOCALES.slice(0, 6).join(', ')}`}><${ChipsInput} id=${`${p}-loc-l-${i}`} label=${`Locales ${i + 1}`} values=${r.locales || []} placeholder="jpn"
        validate=${(x) => (LOCALES.includes(x) ? '' : `“${x}” is not a supported locale code`)} onChange=${(v) => upd(i, { locales: v })} /></${Field}>
      <button type="button" class="btn btn-ghost btn-sm btn-icon" aria-label=${`Remove localized rule ${i + 1}`} onClick=${() => set('localizedAttributes', rows.length > 1 ? rows.filter((_, j) => j !== i) : undefined)}><${Icon} name="x" size=${14} /></button>
    </div>`)}
    <div><${Button} size="sm" icon="plus" onClick=${() => set('localizedAttributes', [...rows, { attributePatterns: [], locales: [] }])}>Add rule</${Button}></div>
  </div>`;
}

const EMB_SOURCES = ['openAi', 'huggingFace', 'ollama', 'rest', 'userProvided', 'composite'];
const EMB_FIELDS = {
  openAi: ['model', 'apiKey', 'url', 'dimensions', 'documentTemplate', 'documentTemplateMaxBytes', 'distribution', 'binaryQuantized'],
  huggingFace: ['model', 'revision', 'documentTemplate', 'documentTemplateMaxBytes', 'distribution', 'binaryQuantized'],
  ollama: ['url', 'model', 'apiKey', 'dimensions', 'documentTemplate', 'documentTemplateMaxBytes', 'distribution', 'binaryQuantized'],
  rest: ['url', 'apiKey', 'dimensions', 'documentTemplate', 'documentTemplateMaxBytes', 'request', 'response', 'distribution', 'binaryQuantized'],
  userProvided: ['dimensions', 'distribution', 'binaryQuantized'],
  composite: ['searchEmbedder', 'indexingEmbedder'],
};

function JsonField({ id, label, value, onChange, hint }) {
  const [text, setText] = useState(value === undefined ? '' : pretty(value));
  const [err, setErr] = useState('');
  return html`<${Field} label=${label} htmlFor=${id} hint=${err ? '' : hint} error=${err}>
    <textarea id=${id} class="textarea code" rows="4" spellcheck="false" value=${text} onInput=${(e) => {
      const t = e.currentTarget.value; setText(t);
      if (!t.trim()) { setErr(''); onChange(undefined); return; }
      try { onChange(JSON.parse(t)); setErr(''); } catch (er) { setErr(`Invalid JSON: ${er.message}`); }
    }}></textarea></${Field}>`;
}

function EmbedderCard({ name, e, onChange, onRemove, p }) {
  const fields = EMB_FIELDS[e.source] || [];
  const up = (patch) => onChange(Object.fromEntries(Object.entries({ ...e, ...patch }).filter(([, v]) => v !== undefined && v !== '')));
  const id = (k) => `${p}-emb-${name}-${k}`;
  const txt = (k, label, hint, opts = {}) => html`<${Field} label=${label} htmlFor=${id(k)} hint=${hint}>
    <input id=${id(k)} class=${cls('input', opts.mono !== false && 'mono')} type=${opts.type || 'text'} autocomplete="off" autocapitalize="off" value=${e[k] ?? ''} placeholder=${opts.placeholder}
      onInput=${(ev) => up({ [k]: ev.currentTarget.value })} /></${Field}>`;
  return html`<div class="emb-card" data-embedder=${name}>
    <div class="row-between"><b class="mono">${name}</b><button type="button" class="btn btn-ghost btn-sm" aria-label=${`Remove embedder ${name}`} onClick=${onRemove}><${Icon} name="trash" size=${14} />Remove</button></div>
    <div class="form-grid">
      <${Field} label="Source" htmlFor=${id('source')}><select id=${id('source')} class="select" value=${e.source} onChange=${(ev) => onChange({ source: ev.currentTarget.value })}>
        ${EMB_SOURCES.map((s) => html`<option value=${s}>${s}</option>`)}</select></${Field}>
      ${fields.includes('model') && txt('model', 'Model', e.source === 'openAi' ? 'e.g. text-embedding-3-small' : e.source === 'ollama' ? 'e.g. nomic-embed-text' : 'e.g. BAAI/bge-base-en-v1.5', { placeholder: '' })}
      ${fields.includes('revision') && txt('revision', 'Revision', 'Optional model revision.')}
      ${fields.includes('url') && txt('url', 'URL', e.source === 'rest' ? 'Endpoint called for each batch.' : 'Optional, for proxies or self-hosted servers.')}
      ${fields.includes('apiKey') && txt('apiKey', 'API key', 'Write only: stored by Meilisearch and never returned.', { type: 'password' })}
      ${fields.includes('dimensions') && html`<${Field} label="Dimensions" htmlFor=${id('dim')}><${NumberInput} id=${id('dim')} value=${e.dimensions} min="1" placeholder=${e.source === 'userProvided' ? 'required' : 'auto'} onChange=${(n) => up({ dimensions: n })} /></${Field}>`}
      ${fields.includes('documentTemplateMaxBytes') && html`<${Field} label="Template max bytes" htmlFor=${id('maxb')}><${NumberInput} id=${id('maxb')} value=${e.documentTemplateMaxBytes} placeholder="400" onChange=${(n) => up({ documentTemplateMaxBytes: n })} /></${Field}>`}
    </div>
    ${fields.includes('documentTemplate') && html`<${Field} label="Document template" htmlFor=${id('tpl')} hint="Liquid template; use {{doc.field}}, e.g. “A product named {{doc.name}}: {{doc.description}}”.">
      <textarea id=${id('tpl')} class="textarea code" rows="3" value=${e.documentTemplate || ''} placeholder="{{doc.name}} {{doc.description}}" onInput=${(ev) => up({ documentTemplate: ev.currentTarget.value })}></textarea></${Field}>`}
    ${fields.includes('request') && html`<div class="form-grid">
      <${JsonField} id=${id('req')} label="Request (JSON)" value=${e.request} hint='Use "{{text}}" where the text goes.' onChange=${(v) => up({ request: v })} />
      <${JsonField} id=${id('res')} label="Response (JSON)" value=${e.response} hint='Use "{{embedding}}" where the vector is.' onChange=${(v) => up({ response: v })} /></div>`}
    ${fields.includes('searchEmbedder') && html`<div class="form-grid">
      <${JsonField} id=${id('se')} label="Search embedder (JSON)" value=${e.searchEmbedder} onChange=${(v) => up({ searchEmbedder: v })} />
      <${JsonField} id=${id('ie')} label="Indexing embedder (JSON)" value=${e.indexingEmbedder} onChange=${(v) => up({ indexingEmbedder: v })} /></div>`}
    ${fields.includes('distribution') && html`<div class="form-grid">
      <${Field} label="Distribution mean" htmlFor=${id('mean')} hint="Optional relevancy correction (0-1)."><input id=${id('mean')} class="input" type="number" step="0.01" min="0" max="1" value=${e.distribution ? e.distribution.mean : ''}
        onInput=${(ev) => { const t = ev.currentTarget.value; up({ distribution: t === '' ? undefined : { ...(e.distribution || { sigma: 0.1 }), mean: Number(t) } }); }} /></${Field}>
      <${Field} label="Distribution sigma" htmlFor=${id('sigma')}><input id=${id('sigma')} class="input" type="number" step="0.01" min="0" max="1" value=${e.distribution ? e.distribution.sigma : ''}
        onInput=${(ev) => { const t = ev.currentTarget.value; up({ distribution: t === '' ? undefined : { ...(e.distribution || { mean: 0.5 }), sigma: Number(t) } }); }} /></${Field}>
    </div>`}
    ${fields.includes('binaryQuantized') && html`<${Toggle} id=${id('bq')} label="Binary quantized" hint="Smaller vectors, lower precision. Cannot be undone." checked=${!!e.binaryQuantized} onChange=${(v) => up({ binaryQuantized: v || undefined })} />`}
  </div>`;
}

function EmbeddersSection({ value, set, p }) {
  const emb = value.embedders || {};
  const [name, setName] = useState('');
  const add = () => { const n = name.trim(); if (!n || emb[n]) return; set('embedders', { ...emb, [n]: { source: 'openAi', model: 'text-embedding-3-small', documentTemplate: '' } }); setName(''); };
  return html`<div class="stack">
    <${Alert} tone="warn">Embedders call external services (OpenAI, Hugging Face, your REST endpoint…) for every indexed document, which costs time and money.</${Alert}>
    ${Object.entries(emb).map(([n, e]) => html`<${EmbedderCard} key=${n} name=${n} e=${e} p=${p} onChange=${(v) => set('embedders', { ...emb, [n]: v })}
      onRemove=${() => { const x = without(emb, n); set('embedders', Object.keys(x).length ? x : undefined); }} />`)}
    <div class="row"><input class="input input-sm mono" style="max-width:220px" value=${name} placeholder="default" aria-label="New embedder name" onInput=${(e) => setName(e.currentTarget.value)}
      onKeyDown=${(e) => { if (e.key === 'Enter') { e.preventDefault(); add(); } }} />
      <${Button} size="sm" icon="plus" onClick=${add} disabled=${!name.trim() || !!emb[name.trim()]} data-testid=${`${p}-emb-add`}>Add embedder</${Button}></div>
  </div>`;
}

// ---------------------------------------------------------------- editor
export function SettingsEditor({ value = {}, onChange, suggestions = [], idPrefix = 'se', jsonTestId, readOnly, defaultTab = 'form', openSections }) {
  const [tab, setTab] = useState(defaultTab);
  const [text, setText] = useState(pretty(value));
  const [jsonErr, setJsonErr] = useState('');
  const fromJson = useRef(false);
  const lastStr = useRef(JSON.stringify(value || {}));
  useEffect(() => {
    // callers often pass a freshly parsed object: only react to real changes
    const str = JSON.stringify(value || {});
    if (str === lastStr.current) return;
    lastStr.current = str;
    if (fromJson.current) { fromJson.current = false; return; }
    setText(Object.keys(value || {}).length ? pretty(value) : '');
    setJsonErr('');
  }, [value]);
  const set = (k, v) => {
    const next = { ...(value || {}) };
    if (v === undefined) delete next[k]; else next[k] = v;
    onChange(next);
  };
  const p = idPrefix;
  const fields = uniq(suggestions.filter(Boolean));
  const body = {
    attributes: () => html`<${AttributesSection} value=${value} set=${set} fields=${fields} p=${p} />`,
    ranking: () => html`<${RankingSection} value=${value} set=${set} p=${p} />`,
    typo: () => html`<${TypoSection} value=${value} set=${set} p=${p} />`,
    synonyms: () => html`<${SynonymsSection} value=${value} set=${set} p=${p} />`,
    words: () => html`<${WordsSection} value=${value} set=${set} p=${p} />`,
    search: () => html`<${SearchSection} value=${value} set=${set} p=${p} />`,
    localized: () => html`<${LocalizedSection} value=${value} set=${set} p=${p} />`,
    embedders: () => html`<${EmbeddersSection} value=${value} set=${set} p=${p} />`,
  };
  const changedCount = SECTIONS.filter((s) => s.keys.some((k) => k in (value || {}))).length;
  return html`<div class=${cls('settings-editor', readOnly && 'is-readonly')} data-testid=${`${p}-editor`}>
    <div class="row-between se-top">
      <div class="seg" role="tablist" aria-label="Settings editor view">
        <button type="button" role="tab" aria-selected=${tab === 'form'} aria-pressed=${tab === 'form'} onClick=${() => setTab('form')}>Form</button>
        <button type="button" role="tab" aria-selected=${tab === 'json'} aria-pressed=${tab === 'json'} onClick=${() => setTab('json')}>JSON</button></div>
      <span class="subtle se-count">${changedCount ? `${changedCount} section${changedCount > 1 ? 's' : ''} changed from default` : 'All Meilisearch defaults'}</span>
    </div>
    <div hidden=${tab !== 'form'} class="se-sections">
      ${SECTIONS.map((s) => {
        const changed = s.keys.some((k) => k in (value || {}));
        return html`<details class=${cls('se-section', changed && 'is-changed')} key=${s.id} open=${openSections ? openSections.includes(s.id) : s.id === 'attributes'} data-section=${s.id}>
          <summary><${Icon} name=${s.icon} size=${16} /><span class="se-title">${s.title}</span>${changed && html`<span class="se-dot" title="Changed from default" aria-label="changed from default"></span>`}
            <span class="se-sum-help">${s.help}</span><${Icon} name="chevron-down" size=${16} class="acc-caret" /></summary>
          <div class="se-body">
            ${body[s.id]()}
            ${changed && !readOnly && html`<div class="se-reset"><button type="button" class="link-btn" onClick=${() => { const n = { ...value }; for (const k of s.keys) delete n[k]; onChange(n); }}>
              <${Icon} name="restart" size=${13} />Reset ${s.title.toLowerCase()} to default</button></div>`}
          </div>
        </details>`;
      })}
    </div>
    <div hidden=${tab !== 'json'} class="stack-sm">
      <textarea class=${cls('textarea code', jsonErr && 'is-invalid')} rows="18" spellcheck="false" aria-label="Settings JSON" value=${text} readonly=${readOnly}
        placeholder=${'{\n  "filterableAttributes": ["category"]\n}'} data-testid=${jsonTestId}
        onInput=${(e) => {
          const t = e.currentTarget.value; setText(t);
          if (!t.trim()) { setJsonErr(''); fromJson.current = true; onChange({}); return; }
          try {
            const v = JSON.parse(t);
            if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error('settings must be a JSON object');
            setJsonErr(''); fromJson.current = true; onChange(v);
          } catch (er) { setJsonErr(er.message); }
        }}></textarea>
      ${jsonErr ? html`<p class="field-error" role="alert">Invalid JSON: ${jsonErr}</p>` : html`<p class="field-hint">Same body as PATCH /indexes/{uid}/settings. Edits here update the form.</p>`}
    </div>
  </div>`;
}

/** "key: current → desired" list used by previews and drift reports. */
export function ChangeList({ changes, testid, currentLabel = 'Current', desiredLabel = 'New' }) {
  return html`<ul class="change-list" data-testid=${testid}>${changes.map((c, i) => html`<li class="change" style=${`--i:${i}`} key=${c.key}>
    <div class="change-key">${c.key}</div>
    <div class="change-vals"><pre class="cur"><small>${currentLabel}</small>${pretty(c.current)}</pre><pre class="des"><small>${desiredLabel}</small>${pretty(c.desired)}</pre></div></li>`)}</ul>`;
}
