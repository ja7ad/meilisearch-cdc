# meilisearch-cdc REST API (v1)

Base path: `/api/v1`. JSON in and out. When `CDC_ADMIN_TOKEN` is set every
`/api` request needs `Authorization: Bearer <token>` (the SSE endpoint also
accepts `?token=`). Errors: `{"error": "message", "details": [...]}` with a 4xx/5xx code.

Types are defined in `internal/model` (Task, Status, TaskView, DLQEntry,
LogEntry, Schema) and `internal/diff` (Report, SchemaReport).

## Overview
- `GET /health` -> `{"status":"ok","meilisearch":{"status":"available","version":"1.x"}}`
- `GET /overview` -> `{"tasks":{"total":n,"byState":{"streaming":n,...}},"totals":Counters,"rate":float,"meilisearch":{"healthy":bool,"version":"","indexes":n,"documents":n,"databaseSize":n},"sources":["postgres",...]}`

## Tasks
- `GET /tasks?state=&q=&tag=` -> `[TaskView]` (secrets redacted)
- `POST /tasks` body Task (no id -> generated) -> `201 TaskView`. `?start=true` starts it.
- `GET /tasks/{id}` -> TaskView
- `PUT /tasks/{id}` body Task -> TaskView (restarts if running; masked secrets are kept)
- `DELETE /tasks/{id}?dropSource=true&deleteIndex=<uid>` -> 204 (deleteIndex must equal the index uid to delete it)
- `POST /tasks/{id}/{action}` action in `start|stop|pause|resume|restart|resync|reset-checkpoint` -> TaskView
- `POST /tasks/batch` `{"ids":[...],"action":"start|stop|pause|resume|restart|resync|delete"}` -> `{"results":[{"id":"","ok":true,"error":""}]}`
- `POST /tasks/test` body Task -> `{"ok":bool,"error":"","schema":Schema,"sample":[{...doc}],"primaryKey":"id"}` (connects, reads schema and 5 transformed sample docs)
- `GET /tasks/export` -> `[Task]` with secrets redacted; `POST /tasks/import` `[Task]` -> `{"created":n,"errors":[...]}`
- `GET /tasks/{id}/logs?limit=200` -> `[LogEntry]`
- `GET /tasks/{id}/metrics` -> `{"points":[{"t":unixMs,"rate":f,"lagMs":n,"upserted":n,"deleted":n}]}` (last ~10 min, 2s resolution)

### Webhook ingest
`POST /ingest/{taskId}` takes changes for a `webhook` task: one object `{"op":"upsert|delete|truncate","row":{...}}`, an array of them, or NDJSON (`op` defaults to `upsert`). It answers `200 {"accepted":n,"position":"..."}` once Meilisearch confirmed the documents, `503` while the task is not running and `504` when confirmation took over 60s (retry: writes are idempotent).

Authentication depends on the task's `source.webhookSecret`:

- **No secret:** a moderator token or session is required.
- **Secret set:** every request must be signed with it, and no token is needed. A token alone is refused. Send:
  - `X-CDC-Timestamp: <unix seconds>`
  - `X-CDC-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + raw body)>`

  Requests are refused with `401` when unsigned, signed with another secret, carrying a changed body, or timestamped more than 5 minutes away from the server clock. Several comma-separated signatures are accepted, which helps while senders rotate secrets.

The secret is at least 16 characters (the dashboard generates `whsec_…` values). It is encrypted at rest and returned as `••••••`; sending the mask back keeps it, and `""` removes it. Anonymous callers get `401` for unknown tasks and for tasks without a secret, so task ids are not revealed.

```bash
BODY='{"op":"upsert","row":{"id":1,"name":"Desk lamp"}}'
TS=$(date +%s)
SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')
curl -X POST "$CDC/api/v1/ingest/$TASK" -H 'Content-Type: application/json' \
  -H "X-CDC-Timestamp: $TS" -H "X-CDC-Signature: sha256=$SIG" -d "$BODY"
```

### Data quality
- `GET /tasks/{id}/schema-check` -> SchemaReport
  `{"taskId","indexUid","primaryKey":{"source":["id"],"field":"id","index":"id","ok":true},"fields":[{"name","sourceColumn","sourceType","inSource":bool,"inIndex":bool,"coverage":0..1,"settings":["filterable",...],"issues":["..."]}],"issues":[{"level":"error|warning|info","message":""}],"checkedAt"}`
- `POST /tasks/{id}/diff` `{"sampleLimit":50,"compareFields":true}` -> `202 {"status":"running"}`; runs async
- `GET /tasks/{id}/diff` -> Report
  `{"taskId","status":"running|done|failed","error":"","startedAt","finishedAt","sourceCount":n,"indexCount":n,"missing":n,"extra":n,"mismatched":n,"inSync":bool,"samples":{"missing":["id"],"extra":["id"],"mismatched":[{"id":"","fields":[{"field":"","source":any,"index":any}]}]}}`
- `POST /tasks/{id}/diff/repair` `{"missing":true,"mismatched":true,"extra":false}` -> `202 {"status":"running"}`: the repair runs in the background as a diff that fixes what it finds; follow it with `GET /tasks/{id}/diff` (its report counts what was repaired)

### Dead letter queue
- `GET /tasks/{id}/dlq?limit=100` -> `[DLQEntry]`
- `POST /tasks/{id}/dlq/replay` `{"ids":[...]}` (empty = all) -> `{"replayed":n,"failed":n}`
- `DELETE /tasks/{id}/dlq` `{"ids":[...]}` (empty = all) -> `{"deleted":n}`

## Meilisearch indexes
- `GET /indexes` -> `[{"uid","primaryKey","createdAt","updatedAt","numberOfDocuments","isIndexing","managedBy":["taskId"],"managedByTasks":[{"id","name","state"}]}]`
- `POST /indexes` `{"uid","primaryKey"}` -> `{"taskUid":n}`
- `GET /indexes/{uid}` -> `{"uid","primaryKey","createdAt","updatedAt","stats":{numberOfDocuments,isIndexing,fieldDistribution,...},"managedBy":[]}`
- `DELETE /indexes/{uid}?confirm={uid}` -> `{"taskUid":n}` (409 when a running task writes to it)
- `GET /indexes/{uid}/settings` -> Meilisearch settings JSON
- `POST /indexes/{uid}/settings/preview` settings JSON -> `{"changes":[{"key":"filterableAttributes","current":any,"desired":any}]}`
- `PATCH /indexes/{uid}/settings` settings JSON -> `{"taskUid":n,"status":"succeeded|failed","error":""}` (waits up to 30s)
- `POST /indexes/{uid}/settings/reset` -> `{"taskUid":n}`
- `PUT /indexes/{uid}/primary-key` `{"primaryKey":"id"}` -> `{"taskUid":n}` (409 when the index has documents)
- `POST /indexes/{uid}/search` `{"q":"","filter":"","sort":[],"limit":20,"facets":[],"hybrid":{...}}` -> Meilisearch search response
- `GET /indexes/{uid}/documents?limit=20&offset=0` -> `{"results":[...],"total":n}`
- `POST /indexes/swap` `{"a":"uid1","b":"uid2","rename":false}` -> `{"taskUid":n}`
- `POST /indexes/{uid}/compact` -> `{"taskUid":n}`

## Meilisearch instance
- `GET /meili/tasks?indexUid=&status=&type=&limit=50&from=` -> `{"results":[Task],"next":n,"total":n}`
- `POST /meili/tasks/cancel` `{"uids":[n]}` -> `{"taskUid":n}`
- `GET /meili/stats` -> Meilisearch stats
- `POST /meili/dumps` / `POST /meili/snapshots` -> `{"taskUid":n}`
- `GET /meili/experimental-features` / `PATCH ...` `{"metrics":true,...}`

## Live events (Server-Sent Events)
`GET /events` streams:
- `event: status` data `Status` (on every change, throttled to ~2/s per task)
- `event: log` data `LogEntry`
- `event: task` data `{"id","action":"created|updated|deleted"}`
- `event: overview` data overview (every 2s)

## Other
- `GET /metrics` Prometheus metrics (not under /api)
- `GET /` web dashboard (embedded)

---

# Accounts, roles and notifications

## Authentication
Two ways to authenticate:
- **Session cookie** (`cdc_session`, HttpOnly, SameSite=Lax, Secure on HTTPS) created by login, setup or invitation acceptance. Sessions last 7 days, sliding. For cookie-authenticated `POST/PUT/PATCH/DELETE` the `Origin` (or `Referer`) must match the host (CSRF protection); browsers send it automatically.
- **Bearer token** `CDC_ADMIN_TOKEN` (machine access, acts as an admin named "api-token"). SSE also accepts `?token=`.

`401` = not authenticated, `403` = authenticated but the role is too low (`{"error":"requires role moderator"}`).

### Roles
| Role | Can |
|---|---|
| `viewer` | read everything except users, invitations, SMTP/Telegram settings and the audit log |
| `moderator` | viewer + task actions (start/stop/pause/resume/restart/resync/reset-checkpoint, batch of those), run diff/repair, DLQ replay/discard, test connection, webhook ingest, cancel Meilisearch tasks |
| `admin` | everything: create/edit/delete/import tasks, index changes (create/delete/settings/primary key/swap/compact), dumps/snapshots/experimental features, users, invitations, SMTP, Telegram, alert rules, audit log |

### Public endpoints (no auth)
- `GET /auth/config` -> `{"setupRequired":bool,"smtpConfigured":bool,"passwordLogin":true}`; `setupRequired` is true while no user exists.
- `POST /auth/setup` `{"email","name","password"}` -> `200 {"user":User}` + cookie. Creates the first admin; `409` once a user exists.
- `POST /auth/login` `{"email","password"}` -> `200 {"user":User}` + cookie; `401 {"error":"invalid email or password"}`; `429` after too many failures (Retry-After header).
- `POST /auth/logout` -> `204` (clears the cookie, revokes the session)
- `POST /auth/forgot` `{"email"}` -> `202 {}` always (never reveals whether the email exists); `409` when SMTP is not configured.
- `POST /auth/reset` `{"token","password"}` -> `204`; `400` invalid/expired token. Revokes the user's other sessions.
- `GET /auth/invite/{token}` -> `{"email","role","expiresAt"}`; `404` invalid/expired/used.
- `POST /auth/invite/accept` `{"token","name","password"}` -> `200 {"user":User}` + cookie.
Passwords: 8-128 characters.

### Current user (any role)
- `GET /auth/me` -> `{"user":User}` (`401` when not logged in; for the bearer token `{"user":{"id":"api-token","email":"","name":"API token","role":"admin"}}`)
- `PATCH /auth/me` `{"name"}` -> `{"user":User}`
- `POST /auth/me/password` `{"currentPassword","newPassword"}` -> `204` (other sessions are revoked)

`User` = `{"id","email","name","role":"viewer|moderator|admin","createdAt","updatedAt","lastLoginAt","owner"?:true}`

`owner: true` marks the **primary administrator**: the first admin (created by setup or `CDC_ADMIN_EMAIL`). On data from older versions the oldest admin becomes the owner. Only the operator CLI on the server can transfer ownership (`meilisearch-cdc admin users set-owner`) or reset the owner's password; see [cli.md](cli.md).

## Users (admin)
- `GET /users` -> `[User]`
- `POST /users` `{"email","name","role","password"}` -> `201 User` (`409` duplicate email)
- `PATCH /users/{id}` `{"name"?, "role"?}` -> `User`. `403` when changing your own role or the primary administrator's role (any caller, the API token included); `409` when demoting the last admin.
- `POST /users/{id}/password` `{"password"}` -> `204` (revokes that user's sessions). `403` for the primary administrator unless they set their own.
- `DELETE /users/{id}` -> `204` (`409` for yourself or the last admin, `403` for the primary administrator)

These rules live in the account service, so the dashboard and the API token are held to them alike. Purging data and other server-side operator actions are CLI only: see [cli.md](cli.md).

## Invitations (admin)
- `GET /invitations` -> `[{"id","email","role","invitedBy","createdAt","expiresAt","emailed":bool}]` (pending only)
- `POST /invitations` `{"email","role"}` -> `201 {"invitation":Invitation,"link":"https://host/#/invite/<token>","emailed":bool,"emailError":""}`. The link is returned once; it is emailed when SMTP is configured. Valid 7 days, single use.
- `DELETE /invitations/{id}` -> `204`

## SMTP (admin)
- `GET /settings/smtp` -> `{"host","port":587,"username","password":"••••••" or "","from","tls":"auto|starttls|tls|none","publicUrl","configured":bool}`. `tls` defaults to `auto` (TLS from the first byte on port 465, STARTTLS otherwise; plain text only when the server lacks STARTTLS and no password is sent). A mode that cannot work on its port is corrected when sending (STARTTLS on 465 uses TLS; TLS on 25/587 uses STARTTLS).
- `PUT /settings/smtp` same body (send the mask back to keep the stored password; empty host disables email) -> same
- `POST /settings/smtp/test` `{"to"}` -> `{"ok":bool,"error":""}`
`publicUrl` is the dashboard base URL used in emailed links (falls back to `CDC_PUBLIC_URL`, then the request origin).

## Telegram (admin)
- `GET /settings/telegram` -> `{"botToken":"••••••" or "","apiUrl":"https://api.telegram.org","configured":bool,"botName":""}`
- `PUT /settings/telegram` `{"botToken","apiUrl"}` -> same (validates the token with getMe; `422` when invalid)
- `POST /settings/telegram/test` `{"chatId"}` -> `{"ok":bool,"error":""}`

## Alert rules
- `GET /alerts/rules` (viewer) -> `[Rule]`
- `GET /alerts/channels` (viewer) -> `{"email":bool,"telegram":bool,"webhook":true}` (which delivery channels are configured)
- `POST /alerts/rules` (admin) Rule -> `201 Rule`; `PUT /alerts/rules/{id}` -> `Rule`; `DELETE /alerts/rules/{id}` -> `204`
  - Creating a rule (or saving one with `enabled: true`) needs email (SMTP) or a Telegram bot configured: otherwise `409` "alerts need a notification channel…". The rule needs at least one recipient (email, Telegram chat or webhook); emails need SMTP and Telegram chats need the bot (`422` with the reason). A disabled rule can always be saved, so rules stay editable after a channel is removed.
- `POST /alerts/rules/{id}/test` (admin) -> `{"results":[{"channel":"email|telegram|webhook","target":"","ok":bool,"error":""}]}` sends a test notification on every channel.
- `GET /alerts/events?limit=100&ruleId=&state=firing|resolved` (viewer) -> `[AlertEvent]` newest first
- `GET /alerts/active` (viewer) -> `[AlertEvent]` currently firing

```
Rule = {
  "id", "name", "enabled": true,
  "condition": {
    "type": "task_state" | "lag" | "dlq" | "errors" | "meili_down" | "stalled",
    "states": ["failed","held","retrying"],   // task_state
    "thresholdMs": 60000,                     // lag
    "count": 1,                               // dlq: new dead letters within the window; errors: errors within the window
    "window": "5m",                           // dlq, errors
    "for": "1m"                               // condition must hold this long before firing (all types)
  },
  "taskIds": [],                              // empty = every task (not used by meili_down)
  "severity": "info" | "warning" | "critical",
  "channels": { "emails": ["ops@x.com"], "telegramChatIds": ["-1001234"], "webhookUrl": "", "webhookSecret": "" },
  "cooldown": "30m",                          // minimum time between repeated notifications while firing
  "notifyResolved": true,
  "createdAt", "updatedAt"
}
AlertEvent = { "id", "ruleId", "ruleName", "severity", "taskId", "taskName", "state": "firing|resolved",
               "message", "at", "deliveries": [{"channel","target","ok","error","at"}] }
```
With `channels.webhookSecret` (at least 16 characters, needs `webhookUrl`), every webhook delivery is signed the same way as webhook ingest: `X-CDC-Timestamp` and `X-CDC-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + body)>`. Receivers should recompute the HMAC over the raw body, compare it in constant time, and reject timestamps older than 5 minutes. The secret is encrypted at rest and returned as `••••••` (send the mask back to keep it).

`stalled` fires when a streaming task received no event for `for` while its source position is behind (best-effort: no confirmed batch for `for`).

## Audit log (admin)
- `GET /audit?limit=200` -> `[{"at","userId","userEmail","action","target","details","ip"}]` newest first. Records logins (success/failure), user/invitation/settings/rule changes, task and index mutations.

## SSE additions
- `event: alert` data `AlertEvent` when an alert fires or resolves.

## MCP server (AI management)
A Model Context Protocol server (Streamable HTTP) at **`/mcp`**, disabled by default. Clients authenticate with `Authorization: Bearer cdcmcp_...`. Each token has a role; the tools and resources a session sees are the ones the policy grants to that role, and every call is checked again (revoked or expired tokens and policy changes apply immediately) and written to the audit log.

Admin endpoints:
- `GET /settings/mcp` -> `{"enabled":bool,"endpoint":"/mcp","policy":Policy,"tools":[ToolInfo],"resources":[ResourceInfo]}`
  - `Policy` = `{"viewer":{"tools":["list_tasks",...],"resources":["cdc://overview",...]},"moderator":{...},"admin":{...}}`
  - `ToolInfo` = `{"name","title","description","minRole":"viewer|moderator|admin","mutating":bool}` (minRole is the default grant; admins may grant more or less)
  - `ResourceInfo` = `{"uri","name","description"}` (uri may be a template like `cdc://tasks/{id}`)
- `PUT /settings/mcp` `{"enabled":bool,"policy":Policy}` -> same as GET
- `POST /settings/mcp/reset-policy` -> same as GET (restores defaults from minRole)
- `GET /mcp/tokens` -> `[{"id","name","role","prefix":"cdcmcp_ab12","createdAt","createdBy","lastUsedAt","expiresAt"}]`
- `POST /mcp/tokens` `{"name","role","expiresIn":"720h" | ""}` -> `201 {"token":"cdcmcp_<random>","info":Token}` — the raw token is returned only once.
- `POST /mcp/tokens/{id}/rotate` -> `{"token","info"}` (old token stops working immediately)
- `DELETE /mcp/tokens/{id}` -> `204` (revoke)

Tools: `overview`, `list_tasks`, `get_task`, `get_task_logs`, `create_task`, `update_task`, `delete_task`, `task_action` (start/stop/pause/resume/restart/resync/reset-checkpoint), `batch_task_action`, `test_connection`, `schema_check`, `run_diff`, `get_diff`, `list_dlq`, `replay_dlq`, `list_indexes`, `get_index`, `search_index`, `get_index_settings`, `preview_index_settings`, `update_index_settings`, `list_meili_tasks`, `list_alert_rules`, `create_alert_rule`, `delete_alert_rule`, `list_alert_events`.
Resources: `cdc://overview`, `cdc://tasks`, `cdc://tasks/{id}`, `cdc://tasks/{id}/logs`, `cdc://indexes`, `cdc://alerts/active`, `cdc://docs/api`.

## Connectors (reusable connections)
A connector holds the connection settings and credentials of a source once; tasks reference it with `source.connectorId` and only add table level fields (`table`, `schema`, `database` override, `slot`, `publication`, `serverId`, `cursorColumn`, `pollInterval`, `groupId`). Rotating a connector password restarts the running tasks that use it. Secrets are encrypted at rest and never returned.

```
Connector = {
  "id", "name", "description", "type": "<source type>", "tags": [],
  "config": {  // connection fields of model.Source for that type
    "dsn", "database", "region", "endpoint", "accessKeyId", "secretAccessKey", "brokers": [], "driver"
  },
  "createdAt", "updatedAt"
}
ConnectorView = Connector + { "usedBy": [{"id","name","state"}],
  "lastTest": {"ok","error","latencyMs","version","tables":n,"at"} }   // dsn password and secretAccessKey masked; lastTest absent until tested

Table = { "name", "schema", "kind": "table|view|collection|topic|stream|database", "rows": n (-1 unknown) }
```
- `GET /connectors` (viewer) -> `[ConnectorView]`
- `GET /connectors/{id}` (viewer) -> `ConnectorView`
- A connector is tested in the background when it is created or saved, and by `POST /connectors/{id}/test`; the outcome is kept as `lastTest`. Testing unsaved values (`POST /connectors/test`) is not recorded.
- `POST /connectors` (admin) Connector -> `201 ConnectorView`
- `PUT /connectors/{id}` (admin) Connector (masked secrets keep the stored value) -> `ConnectorView`
- `DELETE /connectors/{id}` (admin) -> `204`; `409 {"error","usedBy":[...]}` while tasks use it
- `POST /connectors/test` (admin) Connector (unsaved; `id` set to reuse stored secrets) -> `{"ok":bool,"error":"","latencyMs":n,"version":"server version if known","tables":[Table]}`
- `POST /connectors/{id}/test` (moderator) -> same, with the stored connector
- `GET /connectors/{id}/tables?q=` (viewer) -> `[Table]` (discovery, max 1000)
- Tasks: `POST /tasks` with `"source": {"connectorId": "...", "table": "products"}`; `type` is taken from the connector. `GET /tasks/{id}` returns `source.connectorId` and the merged (redacted) connection fields for display.

## Meilisearch destinations (many sources → many Meilisearch instances)
A destination is a named Meilisearch instance. Every task writes to one destination (`index.destinationId`; empty = the default destination). Fan-out to several instances = several tasks on the same connector/table.

Two modes:
- **Multi-instance (default, `MEILI_URL` unset)**: destinations are created and managed in the dashboard.
- **Single instance (`MEILI_URL` set)**: the service writes only to that instance. It appears as destination `default` (`managedByEnv`, URL/key read-only); creating, deleting or switching destinations returns `409` with an explanation, and other stored destinations are hidden. `GET /auth/config` reports `"singleDestination": true` so the UI hides destination management.

```
Destination = {
  "id", "name", "description", "url": "https://meili.example.com", "apiKey": "••••••" (write only),
  "gzip": true, "timeout": "30s", "healthCheckInterval": "30s" (5s–1h, default 30s), "isDefault": bool, "managedByEnv": bool, "tags": [], "createdAt", "updatedAt"
}
DestinationView = Destination + {
  "usedBy": [{"id","name","state","indexUid","lagMs","rate","lastEventAt","lastSyncAt","error"}],
  "sync": {"tasks":n,"running":n,"failing":n,"lastSyncAt","maxLagMs":n,"rate":f},   // aggregated over usedBy
  "health": Health
}
Health = {"healthy":bool,"version":"","indexes":n,"documents":n,"databaseSize":n,"error":"","latencyMs":n,
          "consecutiveFailures":n,"lastHealthyAt"(absent until healthy once),"checkedAt","nextCheckAt"}
```
Every destination is checked in the background at its `healthCheckInterval`; reads return the last check. Each completed check is pushed on the SSE stream as `event: destination.health` with `{"id","health":Health}`. Changing the interval takes effect at once.
- `GET /destinations` (viewer) -> `[DestinationView]`; `GET /destinations/{id}` -> `DestinationView`
- `POST /destinations` (admin) -> `201 DestinationView`; `PUT /destinations/{id}` (admin; masked apiKey keeps the stored key; changing url/key restarts running tasks using it); `DELETE /destinations/{id}` (admin; `409 {"error","usedBy"}` while used; the default cannot be deleted)
- `POST /destinations/{id}/default` (admin) -> make it the default
- `POST /destinations/test` (admin) Destination (unsaved; `id` reuses the stored key only when the URL is unchanged) -> `{"ok","error","version","latencyMs","indexes":n}`
- `POST /destinations/{id}/test` (moderator) -> same
- `POST /destinations/{id}/health/check` (moderator) -> `Health` (checks now; `404` for an unknown destination)

Every index and Meilisearch endpoint (`/indexes/...`, `/meili/...`) takes `?destination=<id>` (default destination when omitted). `GET /indexes` items gain `"destinationId"`, and `managedBy` only lists tasks of that destination.
`GET /indexes?destination=*` lists every instance at once, grouped (default destination first, instances queried in parallel; an unreachable one has `"healthy": false` and `"error"` with no indexes): `[{"destinationId","destinationName","isDefault","healthy","error","indexes":[...]}]`. In single instance mode it returns the one group.
`GET /overview` keeps `meilisearch` (default destination) and adds `"destinations":[{"id","name","isDefault","healthy","version","indexes","documents","error"}]`.
Tasks: `index.destinationId`; `TaskView.index.destinationId` is always filled in (resolved default).
Alerts: `meili_down` rules evaluate every destination (or `"destinationIds":[...]` on the rule); events carry `"destinationId","destinationName"`.
MCP: index tools accept an optional `"destination"` argument; `list_destinations` tool and `cdc://destinations` resource.

## Zero configuration and onboarding
The service starts with only a data directory: no Meilisearch URL, keys or tokens are required.
- When `CDC_SECRET_KEY` is not set, a random key is created once in `<data-dir>/secret.key` (0600) and used to encrypt credentials.
- `MEILI_URL` is optional; without it there is no default destination until one is added in the UI.
- `GET /auth/config` also reports `"telegramConfigured"` to signed in users, and adds `"onboarding": {"destinations": n, "connectors": n, "tasks": n, "complete": bool}` (counts only; `complete` = at least one destination and one task). The dashboard shows an onboarding flow after the first admin signs in: add a destination → add a connector → create the first task (each step skippable).
