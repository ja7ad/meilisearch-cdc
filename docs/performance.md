# Performance and resource usage

## Measured throughput

These numbers come from the load test in `test/bench`. It runs the real service in-process against real Meilisearch and Postgres containers.

| Setup | |
|---|---|
| Machine | 12-core Apple M-series, containers under OrbStack |
| Source | Postgres 17 (`postgres:17-alpine`): pgoutput replication slot, keyset-paged snapshot copy |
| Destination | Meilisearch (`getmeili/meilisearch`), single node, gzip on |
| Dataset | `products`: 10 columns (bigint id, text name/description/category, numeric price, boolean, `text[]` tags, `jsonb` meta, timestamptz, int version) |
| Task settings | `batchSize` 10,000, `maxInflight` 8, `flushInterval` 200ms |

Ranges are the lowest and highest result across repeated runs.

The figures are end to end: a row changes, and the document is confirmed and searchable in Meilisearch. Meilisearch's own indexing is the bottleneck. The service uses roughly a third of one core at these rates.

| Workload | Rows | Throughput |
|---|---|---|
| Postgres snapshot (pgoutput slot + keyset copy) | 200,000 | 83k–92k docs/s |
| Postgres streaming (500-row transactions) | 50,000 | 39k–46k docs/s |
| In-memory source snapshot | 200,000 | 64k–73k docs/s |
| In-memory source streaming | 50,000 | 26k–29k docs/s |
| Writer pipeline alone (fake Meilisearch) | — | ~2.5M docs/s |

Footprint:

| Metric | Value |
|---|---|
| Idle process RSS, dashboard included | ~47 MiB |
| Binary size | ~55 MiB |

## What profiling changed

Profiles were taken with `go test -tags bench -cpuprofile/-memprofile` during the load test.

| Change | Effect |
|---|---|
| Documents are encoded to NDJSON by a hand-written encoder, then sent with the SDK's `AddDocumentsNdjson`/`UpdateDocumentsNdjson` (`internal/sink/ndjson.go`). It replaces `encoding/json` reflection over `map[string]any`, which also sorted keys. | Encoding is 3.8× faster, with 23× less memory and 13× fewer allocations. The writer's CPU dropped 45% and its allocations 58% in the end-to-end test. |
| Encode buffers are pooled and pre-sized from the previous batch's average document size. Retries resend the same bytes. | No repeated buffer regrowth. |
| `transform` copies nested maps and slices only when something inside them needs converting. | 27% fewer bytes and 3 fewer allocations per row. |
| argon2id key derivation uses OWASP's 19 MiB / 2-pass profile instead of 64 MiB. | 45 MiB less allocated at startup. |
| In total, across the end-to-end load test. | Process CPU dropped 23% (2.26 s to 1.74 s for 250k documents). |

The remaining CPU is mostly garbage collection of short-lived row maps, plus network I/O for the source protocol and the Meilisearch HTTP calls.

### Profile-guided optimisation (PGO)

`cmd/meilisearch-cdc/default.pgo` merges the CPU profiles of both load tests, and `go build` uses it automatically. In A/B runs of the Postgres load test (`-pgo=off` vs the profile), CPU time and throughput stayed **within run-to-run noise**: 1.91–1.95 s vs 1.92–1.99 s CPU.

The service is I/O- and GC-bound, so the inlining that PGO enables has little to work on. The profile is kept because it costs nothing at runtime and becomes useful once refreshed from production traffic:

```bash
CDC_PPROF=true ./meilisearch-cdc            # exposes /debug/pprof/* to admins
curl -H "Authorization: Bearer $TOKEN" -o prod.pprof "http://host:8080/debug/pprof/profile?seconds=60"
make pgo PROFILES="prod.pprof"              # merges into cmd/meilisearch-cdc/default.pgo
```

## Tuning

| Knob | Default | Effect |
|---|---|---|
| `sync.batchSize` | 1000 | Larger batches mean fewer Meilisearch tasks and better indexing throughput, at the cost of more memory per in-flight batch. 5,000–10,000 suits snapshots. |
| `sync.maxInflight` | 4 | Number of batches waiting for Meilisearch confirmation. Meilisearch auto-batches consecutive tasks, so 8 helps bulk loads. |
| `sync.flushInterval` | 1s | Upper bound on how long a change waits in the buffer. Lower it for lower latency on quiet tables. |
| `index.writeMode` | replace | `upsert` (partial update) sends only changed fields from sources that omit unchanged columns. |
| Destination `gzip` | on | Saves bandwidth to remote instances; turn it off on loopback or LAN to save CPU. |
| `GOMEMLIMIT` | unset | Set it to about 80% of the container memory limit, e.g. `GOMEMLIMIT=400MiB`. The GC then works harder near the limit instead of the process being killed by the OOM killer. |
| `GOGC` | 100 | Raising it (e.g. 200) trades memory for less GC CPU on busy instances. |

Memory scales with `batchSize × maxInflight × average document size` per running task, plus about 1 MB of logs and metrics history per task. Snapshot reads are chunked (5,000 rows), and source back pressure stops reading while the writer is saturated.

## Reproduce

```bash
make bench                 # micro benchmarks (transform, batching, encoder, writer)
make load                  # end-to-end load tests against containers, prints docs/s and writes profiles
BENCH_ROWS=1000000 make load
```
