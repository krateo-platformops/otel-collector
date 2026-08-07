---
type: API
title: otel-collector — api
description: The contract this repo exposes — the composition-id attribute contract, the compositionresolver config keys, and the precise deltas of the two vendored forks vs upstream contrib v0.118.0.
resource: ghcr.io/krateo-platformops/otel-collector
tags: [observability, clickstack, otel, fork]
timestamp: 2026-08-07T00:00:00Z
---

# API

This component owns **no CRDs and no HTTP API** (the only HTTP surface is the standard
`healthcheckextension` endpoint, configured chart-side). Its contract is threefold: the
**telemetry attribute** it stamps, the **collector component set** it compiles
([overview.md](./overview.md)), and — since two of those components are patched upstream
forks — the **fork deltas vs upstream contrib v0.118.0**, documented here so the forks can
be dropped the moment upstream ships equivalents.

## The composition-id attribute contract

Enriched **log records** (K8s events) carry the attribute
`krateo.io/composition-id = <the involvedObject's label value>` when resolvable; the value is
the Composition resource's UID (`compositionresolver/config.go:19-21`). Absence of the
attribute means "not attributable to a composition" (no label, unmapped kind, GET failure,
non-event record) — never an error, and never an empty string
(`processor.go:159-161` stamps only non-empty results). Downstream consumers — ClickHouse
queries and **sse-proxy `/events`** — key on this attribute. Metrics and traces are **not**
enriched (logs-only processor, `factory.go:19`). Full lifecycle:
[internals/behavior.md](./internals/behavior.md).

Config keys (`cache_ttl`, `negative_cache_ttl`, `label_key`):
[configuration.md](./configuration.md).

---

## Fork deltas vs upstream contrib v0.118.0

Both forks keep the **upstream module path** in `go.mod` (so the `gomod:` entry in
`builder-config.yaml` is satisfied) and are wired in via a `path:` override
(`builder-config.yaml:11-12`, `32-33`). Both are strict subsets of the upstream dir plus the
patch: upstream test files/mocks/testdata not needed for the build were dropped from the
receiver copy; the exporter copy keeps upstream tests and adds `recreate_schema_test.go`.
Neither fork adds or changes any configuration key.

### <a name="fork-k8sobjectsreceiver"></a>`k8sobjectsreceiver/` — watch-stall recovery

**Problem (upstream v0.118.0):** a watch connection that goes silently dead (no error, no
channel close — e.g. black-holed behind an LB/NAT) hangs the receiver forever; and a closed
result channel **stopped** the watch permanently (`return true` from the watch loop).

**Delta (all in `receiver.go`; every hunk is marked `// krateo patch:`):**

1. New constant `watchRecycleInterval = 10 * time.Minute` (`receiver.go:30-33`) — not
   configurable.
2. The watch loop arms a recycle timer (`receiver.go:212-213`); when it fires, the watch is
   proactively re-established (`case <-recycle.C:` → `return false`, `receiver.go:218-220`).
3. On the result channel closing, the receiver now **re-establishes** the watch instead of
   stopping it: upstream's `Warn("Watch channel closed unexpectedly") + return true` becomes
   `Warn("watch channel closed, re-establishing watch") + return false`
   (`receiver.go:233-236`).

Everything else (config surface, `unstructured_to_logdata.go`, factory) is byte-identical to
upstream v0.118.0.

### <a name="fork-clickhouseexporter"></a>`clickhouseexporter/` — schema recreation on `UNKNOWN_TABLE`

**Problem (upstream v0.118.0):** the exporter creates its `otel_*` schema only in `start()`.
If ClickHouse is re-provisioned with a fresh, empty database while the collector keeps
running, the connection pool reconnects transparently but every insert fails with
`UNKNOWN_TABLE` (ClickHouse error code 60) and telemetry is dropped until the collector is
manually restarted.

**Delta:**

1. New file `recreate_schema.go`: `unknownTableCode int32 = 60` (`recreate_schema.go:14`)
   and `isTableMissing(err)` matching a `clickhouse-go` `proto.Exception` with that code
   (`recreate_schema.go:24-30`). Paired `recreate_schema_test.go`.
2. In each of `exporter_logs.go` / `exporter_metrics.go` / `exporter_traces.go`, the
   `start()` DDL body is extracted into an idempotent `createSchema(ctx)`
   (`exporter_logs.go:56`, `exporter_metrics.go:57`, `exporter_traces.go:58` —
   `CREATE ... IF NOT EXISTS`, safe to re-run), and the push function is wrapped: on an
   insert error where `shouldCreateSchema() && isTableMissing(err)`, it warns, recreates the
   schema and retries the push **once** (`exporter_logs.go:72-87`,
   `exporter_metrics.go:84-99`, `exporter_traces.go:74-89`); the original push body becomes
   `doPush*Data` (`exporter_logs.go:89`, `exporter_metrics.go:101`,
   `exporter_traces.go:91`).
3. The retry path is gated by the **existing** `create_schema` config option
   (`config.go:182-183`) — no new keys; with `create_schema: false` behavior is upstream's.

Everything else (SQL, models, config, factory) is identical to upstream v0.118.0.

### Exit criteria for the forks

- `clickhouseexporter`: an upstream PR is pending (the fix needs re-implementation on
  upstream's refactored `driver.Conn` main). When it merges and releases: delete the
  vendored dir, drop the `path:` override, bump the `gomod` version
  (`builder-config.yaml:27-31` records this contract).
- `k8sobjectsreceiver`: same shape — the recycle/restart patch is upstream-able; until then
  the vendored copy must be re-diffed against upstream on every version bump (the whole
  v0.118.0 set moves as one unit — see gotcha 8 in
  [internals/gotchas.md](./internals/gotchas.md)).

### `compositionresolver/` — not a fork

Wholly Krateo-owned code (no upstream counterpart). Its module path
`github.com/krateo-platformops/otel-collector/compositionresolver` is real, but the
`v0.0.1` in `builder-config.yaml:23` is a never-fetched placeholder — the build uses the
local `path:` override (`builder-config.yaml:24`).
