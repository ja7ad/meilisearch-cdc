# Operator CLI: `meilisearch-cdc admin`

The binary that runs the service also has commands for operators on the server: recover an account, transfer the primary administrator, sign everyone out, turn MCP off, or delete stored data.

```bash
meilisearch-cdc admin [config flags] <command> [flags]
meilisearch-cdc admin --help                 # every command
meilisearch-cdc admin users create --help    # one command's flags
```

`meilisearch-cdc` without `admin` runs the service as before.

## How it reaches the data

- **The service is running.** The service listens on a unix socket in its data directory, `<data-dir>/admin.sock` (mode `0600`, removed on shutdown). When that path is too long for a unix socket (over 103 bytes), the socket goes to a private `0700` directory in the system temp directory instead, `$TMPDIR/meilisearch-cdc-<uid>/<hash of the data directory>.sock`; the CLI finds it the same way and refuses a directory another user could write to. The CLI sends the command there, so the running service applies it: a purge stops the affected tasks cleanly and the dashboard sees changes at once. The file permission is the authorization: whoever can open the socket can read the data directory anyway.
- **The service is stopped.** The CLI opens the database itself and runs the same code.
- **The database is locked, but nothing answers on the socket** (a version without the socket, or the socket could not be created): the CLI says so and exits. Stop the service and retry.

The CLI reads the service's configuration (the same flags and environment variables), so it finds the same data directory and key. When the service gets its key from `CDC_SECRET_KEY` rather than the generated `secret.key` file, give the CLI the same key; without one it refuses to open the data directory. Put config flags before the command:

```bash
CDC_DATA_DIR=/var/lib/meilisearch-cdc meilisearch-cdc admin users list
CDC_SECRET_KEY_FILE=/run/secrets/cdc-key meilisearch-cdc admin -data-dir /var/lib/meilisearch-cdc users list
docker exec -it cdc meilisearch-cdc admin users list
```

Unix sockets have a path limit of about 100 characters: with a longer data directory path the service logs a warning, and the CLI works only while the service is stopped.

Every command, refused ones included, is recorded in the audit log with the user `cli` and the action `cli.<command>` (for example `cli.users.reset-password`). Passwords never are.

Output is human readable; `-json` (before or after the command) prints JSON. The exit code is `0` on success, `1` when the command failed and `2` for usage errors.

## Commands

| Command | |
|---|---|
| `users list` | accounts, oldest first, with the primary administrator marked |
| `users create --email E [--name N] [--role R]` | add an account (`viewer` by default). The first admin of an empty installation becomes the primary administrator |
| `users reset-password --email E` | set a new password and sign the user out everywhere. Works for the primary administrator too |
| `users set-role --email E --role R` | change a role. The primary administrator's role cannot change, and the last admin cannot be demoted |
| `users set-owner --email E` | make an admin the primary administrator |
| `users delete --email E` | delete an account and its sessions. Not the primary administrator, not the last admin |
| `sessions revoke [--email E]` | sign one user out, or everyone without `--email` |
| `mcp disable` | turn the MCP server off; tokens and the access policy stay |
| `mcp revoke-tokens` | revoke every MCP token |
| `purge <scope>... [--yes] [--indexes]` | delete stored data, see below |

Passwords for `users create` and `users reset-password`:
- `--generate` creates a strong random password and prints it once;
- `--password-file F` reads it from a file (`-` for stdin);
- otherwise, on a terminal, a prompt asks twice without echo; with stdin redirected, its first line is used.

## The primary administrator

The first admin, created by the setup page or by `CDC_ADMIN_EMAIL`, is the **primary administrator** (owner). With data from an older version, the oldest admin becomes the owner on start. In the dashboard and the API:

- nobody can change their own role;
- nobody can change the owner's role or delete the owner (`403`);
- only the owner can set the owner's password.

Only the CLI transfers ownership (`users set-owner`, to an admin) or resets the owner's password. To demote or delete the owner, transfer ownership first.

## Purge

`admin purge` permanently deletes exactly the data of the scopes you name. It asks you to type `purge` unless `--yes` is given. Scopes run in this order:

| Scope | Deletes |
|---|---|
| `tasks` | every task with its checkpoint, status, dead letters and diff report; running tasks stop first. Meilisearch indexes stay unless `--indexes` is given, then each task's index is deleted on its destination (a task whose index cannot be deleted is kept and reported). Source resources such as replication slots are not dropped |
| `checkpoints` | sync progress: every task runs a full snapshot on its next start. Running tasks are restarted now |
| `dlq` | dead letters of every task |
| `diffs` | diff reports; running diffs are cancelled |
| `logs` | the task activity log. It is kept in memory, so only a running service has one |
| `connectors` | saved source connections. Refused while tasks exist, unless `tasks` is purged in the same command |
| `destinations` | Meilisearch destinations, with the same rule. With `MEILI_URL` set, its destination is recreated |
| `alert-rules` | alert rules and their alert state; the history stays |
| `alert-events` | alert history and alert state; rules stay |
| `mcp-tokens` | MCP bearer tokens |
| `settings` | SMTP, Telegram and the MCP settings (MCP is disabled and the default policy applies) |
| `sessions` | every sign-in session: everyone is signed out |
| `invitations` | pending invitations |
| `users` | every account with its sessions, invitations and password resets; the next visit shows the setup page (or `CDC_ADMIN_EMAIL` creates the admin on the next start) |
| `audit` | the audit log. The purge itself is recorded afterwards |
| `all` | everything: the data directory is as good as new, except that the secret key file stays |

Examples:

```bash
meilisearch-cdc admin purge sessions                       # sign everyone out
meilisearch-cdc admin purge dlq diffs alert-events --yes
meilisearch-cdc admin purge tasks connectors destinations --indexes
meilisearch-cdc admin purge all
```

Purge is not available in the REST API: it is a destructive, server-side operator action.

## Example session

```text
$ meilisearch-cdc admin users list
EMAIL              NAME           ROLE   OWNER  LAST SIGN-IN      CREATED
owner@example.com  Administrator  admin  yes    2026-10-26 22:47  2026-10-26 22:47

$ meilisearch-cdc admin users create --email viewer@example.com --name Vera --role viewer --generate
created viewer@example.com (viewer)
Generated password (shown only once): zRkR-iKwOwCM4Cr1B30g2125

$ meilisearch-cdc admin users set-role --email owner@example.com --role viewer
error: forbidden: the primary administrator's role cannot be changed; transfer ownership first with `meilisearch-cdc admin users set-owner`

$ meilisearch-cdc admin users set-owner --email ops@example.com
ops@example.com is now the primary administrator

$ meilisearch-cdc admin purge sessions audit --yes
SCOPE     REMOVED
sessions  0
audit     17
```
