// Per source type connection settings shared by the task wizard and the connector editor.
import { html, useState } from '../vendor/htm-preact.js';
import { Field } from './ui.js';
import { cls } from './util.js';

/** Connection level fields (what a connector stores) per source type. */
export const CONN_FIELDS = {
  postgres: ['dsn'], mysql: ['dsn', 'database'], mariadb: ['dsn', 'database'], mongodb: ['dsn', 'database'],
  dynamodb: ['region', 'endpoint', 'accessKeyId', 'secretAccessKey'], kafka: ['brokers'], sqlpoll: ['driver', 'dsn'],
  couchdb: ['dsn'], sqlserver: ['dsn'], cockroachdb: ['dsn'], redis: ['dsn'], webhook: [],
};
/** Table level fields a task adds on top of a connector (docs/api.md, Connectors). */
export const TABLE_FIELDS = {
  postgres: ['schema', 'table', 'slot', 'publication'], mysql: ['database', 'table', 'serverId'], mariadb: ['database', 'table', 'serverId'],
  mongodb: ['database', 'table'], dynamodb: ['table'], kafka: ['groupId', 'table'], sqlpoll: ['table', 'cursorColumn', 'pollInterval'],
  couchdb: ['table'], sqlserver: ['schema', 'table'], cockroachdb: ['table'], redis: ['table'], webhook: ['table', 'webhookSecret'],
};

export function dsnHint(type, driver) {
  return {
    postgres: 'postgres://user:password@host:5432/db?sslmode=disable — needs wal_level=logical',
    mysql: 'user:password@tcp(host:3306)/db — needs binlog_format=ROW',
    mariadb: 'user:password@tcp(host:3306)/db — needs binlog_format=ROW',
    mongodb: 'mongodb://user:password@host:27017/?replicaSet=rs0 — change streams need a replica set',
    couchdb: 'http://user:password@host:5984 — the database name goes in the task',
    sqlserver: 'sqlserver://user:password@host:1433?database=shop — needs CDC enabled on the table',
    cockroachdb: 'postgres://root@host:26257/defaultdb?sslmode=disable — needs kv.rangefeed.enabled',
    redis: 'redis://:password@host:6379/0 — entries {op, id, data}',
    sqlpoll: { pgx: 'postgres://user:password@host/db', mysql: 'user:password@tcp(host:3306)/db', sqlite: 'file:/path/to/db.sqlite' }[driver] || '',
  }[type];
}

const DSN_TYPES = ['postgres', 'mysql', 'mariadb', 'mongodb', 'sqlpoll', 'couchdb', 'sqlserver', 'cockroachdb', 'redis'];

/** Validation of connection level fields: returns {field: message}. */
export function validateConnection(type, c) {
  const e = {};
  if (DSN_TYPES.includes(type) && !String(c.dsn || '').trim()) e.dsn = 'Connection string is required';
  if (type === 'dynamodb' && !String(c.region || '').trim()) e.region = 'Region is required for DynamoDB';
  if (type === 'kafka' && !(c.brokers || []).length) e.brokers = 'At least one broker is required';
  if (type === 'sqlpoll' && !c.driver) e.driver = 'Driver is required';
  return e;
}

/** Pick the connection fields of a type out of a source/config object, omitting empty values. */
export function pickConnection(type, src) {
  const out = {};
  for (const f of CONN_FIELDS[type] || []) {
    const v = src[f];
    if (Array.isArray(v) ? v.length : v !== '' && v != null) out[f] = typeof v === 'string' ? v.trim() : v;
  }
  return out;
}

/** "db.internal:5432" from a DSN (password never shown), region or brokers. */
export function connectionHost(type, c = {}) {
  if (type === 'dynamodb') return c.endpoint || (c.region ? `dynamodb.${c.region}` : '');
  if (type === 'kafka') return (c.brokers || []).join(', ');
  const dsn = c.dsn || '';
  const tcp = /@(?:tcp|unix)?\(([^)]+)\)/.exec(dsn);
  if (tcp) return tcp[1];
  const url = /^[a-z][a-z0-9+.-]*:\/\/(?:[^@/]*@)?([^/?#]+)/i.exec(dsn);
  if (url) return url[1];
  const kv = /host=(\S+)/.exec(dsn);
  if (kv) return kv[1];
  return dsn.startsWith('file:') ? dsn.slice(5) : '';
}

/**
 * Connection inputs for a type. `value` holds dsn/database/region/endpoint/accessKeyId/secretAccessKey/brokers/driver.
 * idPrefix keeps ids unique when two editors could be mounted.
 */
export function ConnectionFields({ type, value, onChange, errors = {}, masked, idPrefix = 'f' }) {
  const [brokerText, setBrokerText] = useState((value.brokers || []).join(', '));
  const has = (f) => (CONN_FIELDS[type] || []).includes(f);
  const up = (k) => (e) => onChange({ ...value, [k]: e.currentTarget.value });
  const hint = dsnHint(type, value.driver);
  const id = (k) => `${idPrefix}-${k}`;
  if (!(CONN_FIELDS[type] || []).length) return html`<p class="muted">This source has no connection settings: it receives changes over HTTP.</p>`;
  return html`<div class="form-grid">
    ${has('driver') && html`<${Field} label="Driver" required htmlFor=${id('driver')} error=${errors.driver}>
      <select id=${id('driver')} class="select" value=${value.driver || 'pgx'} onChange=${up('driver')}>
        <option value="pgx">pgx (PostgreSQL)</option><option value="mysql">mysql (MySQL / MariaDB)</option><option value="sqlite">sqlite</option></select></${Field}>`}
    ${has('dsn') && html`<${Field} label="Connection string (DSN)" required class="span-2" htmlFor=${id('dsn')} error=${errors.dsn}
        hint=${masked && String(value.dsn || '').includes('••••••') ? 'The password is stored. Keep the mask to reuse it, or type a new connection string.' : hint}>
      <input id=${id('dsn')} class=${cls('input mono', errors.dsn && 'is-invalid')} value=${value.dsn || ''} autocomplete="off" autocapitalize="off" spellcheck="false" inputmode="url"
        placeholder=${hint && hint.split(' — ')[0]} onInput=${up('dsn')} aria-invalid=${errors.dsn ? 'true' : undefined} /></${Field}>`}
    ${has('region') && html`<${Field} label="AWS region" required htmlFor=${id('region')} error=${errors.region}><input id=${id('region')} class="input" value=${value.region || ''} placeholder="eu-west-1" autocapitalize="off" onInput=${up('region')} /></${Field}>`}
    ${has('endpoint') && html`<${Field} label="Endpoint" htmlFor=${id('endpoint')} hint="Optional, e.g. http://localhost:8000 for DynamoDB Local."><input id=${id('endpoint')} class="input mono" inputmode="url" autocapitalize="off" value=${value.endpoint || ''} onInput=${up('endpoint')} /></${Field}>`}
    ${has('accessKeyId') && html`<${Field} label="Access key id" htmlFor=${id('akid')} hint="Empty uses the default AWS credential chain."><input id=${id('akid')} class="input mono" autocomplete="off" autocapitalize="off" value=${value.accessKeyId || ''} onInput=${up('accessKeyId')} /></${Field}>`}
    ${has('secretAccessKey') && html`<${Field} label="Secret access key" htmlFor=${id('secret')} hint=${masked ? 'Leave the masked value to keep the stored secret.' : ''}><input id=${id('secret')} type="password" class="input mono" autocomplete="new-password" value=${value.secretAccessKey || ''} onInput=${up('secretAccessKey')} /></${Field}>`}
    ${has('brokers') && html`<${Field} label="Brokers" required class="span-2" htmlFor=${id('brokers')} hint="Comma separated host:port list." error=${errors.brokers}>
      <input id=${id('brokers')} class="input mono" autocapitalize="off" value=${brokerText} placeholder="kafka-1:9092, kafka-2:9092"
        onInput=${(e) => { const v = e.currentTarget.value; setBrokerText(v); onChange({ ...value, brokers: v.split(',').map((x) => x.trim()).filter(Boolean) }); }} /></${Field}>`}
    ${has('database') && html`<${Field} label="Database" required=${type === 'mongodb'} htmlFor=${id('db')} error=${errors.database} hint=${type === 'mongodb' ? 'Default database; tasks may override it.' : 'Optional, taken from the DSN when empty.'}>
      <input id=${id('db')} class="input mono" autocapitalize="off" value=${value.database || ''} onInput=${up('database')} /></${Field}>`}
  </div>`;
}
