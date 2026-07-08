/*
 * Navigation of the Meili CDC user guide: the single source of truth for the
 * page order, sidebar sections, titles and descriptions.
 *
 * After editing this file (or any page), run from the repository root:
 *   node docs/guide/build-search-index.mjs
 * It refreshes every page's <head> meta, sidebar, breadcrumbs, pager and
 * JSON-LD, and rewrites assets/search-index.json, assets/search-index.js and
 * sitemap-pages.json.
 */
window.GUIDE_NAV = {
  updated: "2026-10-26",
  sections: [
    {
      title: "Getting started",
      pages: [
        { file: "index.html", title: "Introduction", description: "What meilisearch-cdc is, how snapshot-then-stream change data capture into Meilisearch works, the core concepts and the feature overview." },
        { file: "quick-start.html", title: "Quick start", description: "Download meilisearch-cdc, verify the checksum, start it, create the admin account and sync your first table into Meilisearch in five minutes." }
      ]
    },
    {
      title: "Deploy",
      pages: [
        { file: "installation.html", title: "Install the binary", description: "Install the meilisearch-cdc binary on Linux (systemd), macOS (launchd) or Windows (service or terminal) with a dedicated user and data directory." },
        { file: "docker.html", title: "Docker and Compose", description: "Run meilisearch-cdc in Docker: image tags, the /data volume, the non-root user, environment variables, secrets files and the Compose demo stack." },
        { file: "kubernetes.html", title: "Kubernetes", description: "Deploy meilisearch-cdc on Kubernetes with a single-replica Deployment, a PersistentVolumeClaim and Secrets mounted through the *_FILE variables." },
        { file: "reverse-proxy.html", title: "Reverse proxy and TLS", description: "Put meilisearch-cdc behind nginx or Caddy with TLS: Server-Sent Events settings, CDC_TRUST_PROXY, CDC_COOKIE_SECURE and CDC_PUBLIC_URL." },
        { file: "backup-upgrade.html", title: "Backups and upgrades", description: "Back up the meilisearch-cdc data directory and secret key, restore it, and upgrade the binary or the Docker image safely." }
      ]
    },
    {
      title: "Configure",
      pages: [
        { file: "configuration.html", title: "Configuration reference", description: "Every meilisearch-cdc flag and environment variable with its default, the *_FILE secret variants, single-instance mode, logging, pprof and metrics." },
        { file: "declarative-tasks.html", title: "Declarative tasks file", description: "Define meilisearch-cdc sync tasks in a YAML or JSON file applied at startup (CDC_TASKS_FILE), with ${VAR} expansion for credentials." }
      ]
    },
    {
      title: "Sources",
      pages: [
        { file: "sources.html", title: "Sources and connectors", description: "The eleven change data capture sources supported by meilisearch-cdc, how connectors store credentials, and what every source has in common." },
        { file: "source-postgres.html", title: "PostgreSQL", description: "Capture PostgreSQL changes with logical replication (pgoutput): wal_level, REPLICA IDENTITY, replication slots, publications and grants." },
        { file: "source-mysql.html", title: "MySQL and MariaDB", description: "Capture MySQL 8 and MariaDB changes from the binary log: binlog_format=ROW, binlog_row_image=FULL, server ids, grants and positions." },
        { file: "source-mongodb.html", title: "MongoDB", description: "Capture MongoDB changes with change streams on a replica set or sharded cluster: connection string, _id handling, resume tokens and drops." },
        { file: "source-dynamodb.html", title: "DynamoDB", description: "Capture Amazon DynamoDB changes through DynamoDB Streams with NEW_AND_OLD_IMAGES: credentials, IAM permissions, shards and 24h retention." },
        { file: "source-couchdb.html", title: "CouchDB", description: "Follow the CouchDB or Cloudant _changes feed into Meilisearch: connection URL, database permissions, deletes, design documents and sequences." },
        { file: "source-sqlserver.html", title: "SQL Server", description: "Capture SQL Server changes from native CDC change tables: sp_cdc_enable_db, sp_cdc_enable_table, SQL Server Agent, grants and retention." },
        { file: "source-cockroachdb.html", title: "CockroachDB", description: "Capture CockroachDB changes with a core changefeed: kv.rangefeed.enabled, CHANGEFEED privilege, HLC positions and gc.ttlseconds." },
        { file: "source-kafka.html", title: "Kafka (Debezium)", description: "Consume Debezium change events from Kafka with the JSON converter to sync Oracle, Db2, Cassandra and other databases into Meilisearch." },
        { file: "source-redis.html", title: "Redis Streams", description: "Push changes into a Redis stream with XADD and sync them into Meilisearch: entry format, op field, positions and trimming." },
        { file: "source-sqlpoll.html", title: "SQL polling", description: "Poll any PostgreSQL, MySQL or SQLite table with an incrementing cursor column when log-based capture is not available; soft deletes and limits." },
        { file: "source-webhook.html", title: "Webhook (HTTP push)", description: "Push changes to meilisearch-cdc over HTTP: the /api/v1/ingest endpoint, JSON, array and NDJSON payloads, responses and retries." }
      ]
    },
    {
      title: "Sync",
      pages: [
        { file: "destinations.html", title: "Meilisearch destinations", description: "Add Meilisearch instances as destinations: URL, API key permissions, gzip, timeout, health checks, the default destination and per-instance pages." },
        { file: "tasks.html", title: "Create a task", description: "Every field of a meilisearch-cdc task, step by step through the wizard: source scope, mapping, primary keys, index, write mode, sync and safety." },
        { file: "task-operations.html", title: "Operate tasks", description: "Task states and lifecycle actions (start, pause, resync, reset checkpoint), editing, batch actions, import and export, and the task detail tabs." },
        { file: "indexes.html", title: "Index management", description: "Manage Meilisearch index settings from meilisearch-cdc: the settings editor, drift detection, primary keys, swap, compaction, search and documents." },
        { file: "data-quality.html", title: "Data quality", description: "Check that Meilisearch matches the source: schema check, document diff and repair, and the dead letter queue with replay and discard." }
      ]
    },
    {
      title: "Operate",
      pages: [
        { file: "monitoring.html", title: "Monitoring and metrics", description: "Monitor meilisearch-cdc: the live dashboard, Server-Sent Events, Prometheus metrics with names and labels, health endpoints and logs." },
        { file: "alerts.html", title: "Alerts and notifications", description: "Alert rules (task state, lag, dead letters, errors, stalled, Meilisearch down) delivered by email (SMTP), Telegram or webhook." },
        { file: "security.html", title: "Team and security", description: "Roles and the permission matrix, invitations, password reset, the primary administrator, sessions, CSRF, rate limiting, audit log and secret encryption." },
        { file: "mcp.html", title: "AI agents (MCP)", description: "Let AI assistants manage meilisearch-cdc over the Model Context Protocol: enable /mcp, create tokens, configure clients, tools, resources and policy." },
        { file: "cli.html", title: "Admin CLI", description: "The meilisearch-cdc admin operator CLI: users, sessions, MCP and purge commands, socket and direct modes, and recovery recipes." },
        { file: "pwa.html", title: "Mobile and app install", description: "Install the meilisearch-cdc dashboard as an app on desktop or phone, how it behaves offline, and how updates are applied." }
      ]
    },
    {
      title: "Reference",
      pages: [
        { file: "api.html", title: "REST API", description: "The meilisearch-cdc REST API v1: authentication, errors, tasks, data quality, indexes, destinations, connectors, accounts, alerts and live events." },
        { file: "performance.html", title: "Performance and tuning", description: "Measured meilisearch-cdc throughput, memory sizing, batch size, in-flight batches, gzip, GOMEMLIMIT and GOGC tuning, and profiling with pprof." },
        { file: "troubleshooting.html", title: "Troubleshooting and FAQ", description: "Fix common meilisearch-cdc errors: replication slots, binlog settings, replica sets, SQL Server Agent, safety holds, SMTP TLS and 409 conflicts." },
        { file: "changelog.html", title: "Changelog and upgrade notes", description: "Where to find meilisearch-cdc release notes, Docker image tags and the upgrade checklist and notes to read before moving between versions." },
        { file: "development.html", title: "Development", description: "Build meilisearch-cdc from source, run unit, integration, end-to-end and dashboard tests, the full GUI e2e environment and load tests." }
      ]
    }
  ]
};
