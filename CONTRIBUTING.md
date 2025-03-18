# Contributing to meilisearch-cdc

Thanks for helping. This file covers the rules a change has to follow to be
merged. The development guide (https://meilicdc.javad.dev/guide/development.html)
covers the codebase and the full end-to-end environment in more depth.

## Before you start

- **Bugs:** open an issue with the version (`meilisearch-cdc version`), the
  source type, what you did, what you expected and what happened. Include logs
  with secrets removed.
- **Features and larger changes:** open an issue first and agree on the design
  before writing code. Small fixes, docs and tests can go straight to a pull
  request.
- **Security issues:** do not open a public issue. Use a private
  [GitHub security advisory](https://github.com/ja7ad/meilisearch-cdc/security/advisories/new).

## Setup

- Go 1.27.3 (see `go.mod`).
- Docker, for integration, e2e and load tests.
- Node.js 22, for the Playwright dashboard tests only.
- [golangci-lint](https://golangci-lint.run) v2 and
  [gosec](https://github.com/securego/gosec), for `make check`. Build them with
  the same Go version as the project, or they cannot read its packages.

```bash
make build && ./bin/meilisearch-cdc
```

The dashboard (`web/`) is plain ES modules with Preact and htm, embedded in the
binary. There is no build step and no bundler: do not add one, and do not add
npm dependencies to the dashboard.

## Rules for every change

1. **`make check` passes.** It runs gofmt, `go vet` (with every build tag),
   golangci-lint, gosec and the unit tests with the race detector. CI runs the
   same checks (`.github/workflows/quality.yml`) and will not merge without them.
2. **Tests come with the change.**
   - Bug fix: a test that fails without the fix.
   - Engine, API, auth or store logic: unit tests and, where a real service is
     involved, an integration test (`//go:build integration`).
   - Dashboard behaviour: a Playwright test in `test/ui/tests` (mock API), or in
     `test/ui/real` when it depends on the real service.
   - Run the suites your change touches: `make integration`, `make e2e`,
     `make ui-test`, `make ui-real`.
3. **Docs come with the change.** Update `docs/api.md` for any API change,
   `docs/cli.md` for CLI changes, the matching page in `docs/guide/` for user-visible
   behaviour, and the README feature list when a feature is added. After editing
   the guide, rebuild its search index:
   `node docs/guide/build-search-index.mjs`.
4. **No silent data loss.** The sync engine's guarantee is that every change
   reaches Meilisearch at least once, in order per document, and that
   checkpoints only advance after Meilisearch confirmed the write. Any change to
   the engine, sinks or sources must keep that, and say how it was tested
   (for example with a crash, restart or outage).
5. **Keep it backwards compatible.** Stored tasks, checkpoints and settings in
   an existing data directory must keep working after an upgrade. Changing a
   stored format needs a migration and a test for it. Breaking API changes need
   an issue and a note in the changelog.

## Code style

- Match the code around you: naming, comment density, error wording and package
  layout.
- Errors are lower case, wrap their cause with `%w`, and tell the user what to
  do when that is possible (`check the database name in the dsn`).
- Handle every error. Ignoring one is acceptable only for best-effort cleanup
  (`Close`, `Rollback`, `RemoveAll`), as configured in `.golangci.yml`.
- `//nolint` and `#nosec` need a reason on the same line:
  `// #nosec G304 -- operator-supplied key file path`.
- No new dependency without a reason in the pull request. Prefer the standard
  library.
- Goroutines must stop when their context ends. Inside `store.Bucket.ForEach`,
  never write to the store: collect the keys and write afterwards.
- Keep `internal/` packages internal. The public surface is the HTTP API, the
  CLI, the MCP server and the configuration (environment variables and
  `tasks.yaml`).

### Security

- Secrets (passwords, API keys, tokens, webhook secrets) are encrypted at rest
  through `secret.Cipher`, masked in every response, log, export and MCP result,
  and restored when a client sends the mask back.
- Every new route gets an entry in the role table in `internal/api/security.go`.
  Choose the lowest role that is safe, and add a test for the roles that must be
  refused.
- Never send a stored credential to an address the request supplies (see the
  destination test endpoint for the pattern).

### Dashboard

- Use the existing components in `web/assets/js/ui.js` and `components.js`, and
  the CSS tokens in `styles.css`. Do not hard-code colours.
- Every effect cleans up its listeners, timers and observers.
- Interactive elements need an accessible name. Add a `data-testid` for anything
  a test drives.
- Check light and dark themes, and phone width.

## Adding a source connector

1. Add a package in `internal/source/<name>` that implements `source.Source`
   and calls `source.Register` in `init`.
2. Import it in `internal/source/all/all.go`.
3. Run the conformance suite in `internal/source/sourcetest` from an
   integration test against a real container (testcontainers).
4. Add an e2e test in `test/e2e`. Add the source to the GUI environment in
   `deploy/e2e` and to the full GUI suite when it can run there.
5. Add the dashboard wizard fields (`web/assets/js/connection.js`), a guide page
   `docs/guide/source-<name>.html`, and a row in the README sources table.

## Commits and pull requests

- Branch from `main`. Keep each pull request to one topic.
- Commit messages are short and in the imperative mood ("add webhook signing",
  "fix lag while Meilisearch hangs"). An optional scope prefix is fine
  (`fix(engine): ...`). Reference the issue (`#123`) when there is one.
- The pull request description says what changed, why, and how it was tested,
  including manual checks. Add screenshots for dashboard changes.
- Keep the branch up to date with `main`.

## Releases

Maintainers release by pushing a `v*` tag. `.github/workflows/release.yml`
tests, builds the binaries and the multi-arch image, and publishes the GitHub
release. A tag with a `-` suffix (`v1.2.0-rc.1`) is a pre-release.
