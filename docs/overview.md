---
type: Architecture
title: otel-collector — overview
description: How the otelcol-krateo binary is assembled by OCB from builder-config.yaml — the component table, the two-stage Docker build, and the compositionresolver processor.
resource: ghcr.io/krateo-platformops/otel-collector
tags: [observability, clickstack, otel, ocb]
timestamp: 2026-08-07T00:00:00Z
---

# Overview

otel-collector is **not** a hand-written service: it is a custom OpenTelemetry Collector
binary assembled at build time by the **OTel Collector Builder (OCB)** from a declarative
manifest, plus three in-tree components. Four artifacts define it:

1. `builder-config.yaml` — the OCB manifest listing every component compiled into the binary.
2. `compositionresolver/` — the wholly custom logs processor (not from upstream).
3. `k8sobjectsreceiver/` — a vendored fork of the upstream contrib receiver at v0.118.0,
   patched for silent watch stalls (see [api.md](./api.md#fork-k8sobjectsreceiver)).
4. `clickhouseexporter/` — a vendored fork of the upstream contrib exporter at v0.118.0,
   patched to recreate the `otel_*` schema on `UNKNOWN_TABLE`
   (see [api.md](./api.md#fork-clickhouseexporter)).

---

## How the collector is built (OCB)

`builder-config.yaml` is consumed by `go.opentelemetry.io/collector/cmd/builder` to generate
and compile the `otelcol-krateo` binary. `Dockerfile` runs it in a two-stage build:

- Stage 1 (`golang:1.23-alpine`, `Dockerfile:1`): installs OCB pinned at `v0.118.0`
  (`Dockerfile:7`), copies `builder-config.yaml` and the three in-tree component dirs
  (`Dockerfile:10-13`), then runs `builder --config builder-config.yaml` (`Dockerfile:15-17`)
  which emits `./otelcol-krateo/otelcol-krateo`.
- Stage 2 (`gcr.io/distroless/base-debian12`, `Dockerfile:20`): copies the static binary and
  CA certs, runs as non-root UID `10001` (`Dockerfile:22-23`), entrypoint `/otelcol-krateo`
  (`Dockerfile:28`).

The dist is named `otelcol-krateo` with `output_path: ./otelcol-krateo`
(`builder-config.yaml:2,4`).

### Components compiled in

Every component below is pinned to OTel `v0.118.0`; the three in-tree components carry a
`path:` override so OCB builds them from the local source. This is the complete component set
the binary can be configured with — anything not here cannot appear in the runtime collector
config the chart supplies (declaring an uncompiled type crashes the collector at config load).

| Class | Component | gomod | Purpose |
|---|---|---|---|
| receiver | `k8sobjectsreceiver` | `builder-config.yaml:11-12` (local `./k8sobjectsreceiver`) | watches K8s objects (events in watch mode) — **krateo-patched fork** |
| receiver | `k8sclusterreceiver` | `builder-config.yaml:13` | cluster-level metrics |
| processor | `batchprocessor` | `builder-config.yaml:17` | batching |
| processor | `memorylimiterprocessor` | `builder-config.yaml:18` | memory back-pressure |
| processor | `k8sattributesprocessor` | `builder-config.yaml:20` | enrich with K8s metadata |
| processor | `resourceprocessor` | `builder-config.yaml:21` | resource attribute edits |
| **processor** | **`compositionresolver`** | **`builder-config.yaml:23-24` (local `./compositionresolver`)** | **custom: stamps `krateo.io/composition-id`** |
| exporter | `clickhouseexporter` | `builder-config.yaml:32-33` (local `./clickhouseexporter`) | the ingestion sink → ClickHouse — **krateo-patched fork** |
| exporter | `debugexporter` | `builder-config.yaml:34` | debug/stdout |
| extension | `healthcheckextension` | `builder-config.yaml:37` | health endpoint |

---

## The pipeline at a glance

```
receivers            processors                                         exporters
─────────            ──────────                                         ─────────
k8sobjectsreceiver → memorylimiter → batch → k8sattributes →           clickhouseexporter
(K8s events,         resource → compositionresolver                    (→ ClickHouse)
 watch mode)                                                            debugexporter
k8sclusterreceiver                                                      (debug)
(cluster metrics)
```

The exact pipeline ordering and endpoints come from the **runtime collector config** shipped
by `clickstack-chart` (`charts/otel-collector-deployment`); this repo only fixes *which*
components exist. What flows to ClickHouse: K8s **event log records** (enriched with
`krateo.io/composition-id` where resolvable) and **cluster metrics** from
`k8sclusterreceiver`.

---

## The `compositionresolver` processor

The load-bearing custom code: a **logs** processor that, for each log record carrying a raw
Kubernetes watch event, looks up the event's `involvedObject` in the cluster, reads that
object's `krateo.io/composition-id` label, and stamps it onto the record as an attribute — so
telemetry landing in ClickHouse is keyed by composition. This is the contract downstream
**sse-proxy `/events`** relies on.

It is a standard OTel processor split across three files:

### `factory.go` — registration & defaults

- Component type is `compositionresolver` (`factory.go:13`).
- `NewFactory` registers it as a **logs** processor at `StabilityLevelAlpha`
  (`factory.go:15-21`).
- `createDefaultConfig` (`factory.go:23-29`) sets the defaults: `CacheTTL = 5m`,
  `NegativeCacheTTL = 30s`, `LabelKey = "krateo.io/composition-id"`.
- `createLogsProcessor` (`factory.go:31-45`) wraps the processor with
  `processorhelper.NewLogs`, wiring `processLogs` as the consume hook and `start`/`shutdown`
  as lifecycle hooks.

### `config.go` — typed config + validation

`Config` (`config.go:8-22`) exposes three mapstructure keys — `cache_ttl`,
`negative_cache_ttl`, `label_key`. `Validate` (`config.go:24-35`) rejects non-positive TTLs
and an empty `label_key`.

### `processor.go` — resolve + cache

`compositionResolverProcessor` (`processor.go:31-42`) holds a dynamic K8s client, a
`RESTMapper`, and a TTL cache keyed by the involvedObject UID.

- **`start`** (`processor.go:53-79`): builds an **in-cluster** client config
  (`rest.InClusterConfig()`, `processor.go:54`) — so this only runs inside a pod with a
  ServiceAccount. Creates the `dynamic.Interface` and a `DeferredDiscoveryRESTMapper` over a
  memory-cached discovery client (`processor.go:59-69`), then launches the eviction goroutine.
- **`shutdown`** (`processor.go:81-84`): closes the eviction stop channel.
- **`processLogs`** (`processor.go:117-130`): the consume hook — iterates every
  ResourceLogs → ScopeLogs → LogRecord and calls `enrichLogRecord` on each.

The enrichment path and cache lifecycle are traced in
[internals/behavior.md](./internals/behavior.md); the pitfalls in
[internals/gotchas.md](./internals/gotchas.md).

### Pipeline position

Because it resolves and stamps the composition-id, `compositionresolver` belongs in the
**logs pipeline** (it implements only `WithLogs`, `factory.go:19`) — between the K8s-events
source (`k8sobjectsreceiver`) and the `clickhouseexporter`. Placing it after
`k8sattributesprocessor` means resource metadata is already present; the actual ordering is
set by the runtime collector config in the chart.

---

## What this repo does NOT contain

- No charts and no runtime collector YAML (receivers/exporter endpoints, the actual pipeline
  ordering): that is `charts/otel-collector-deployment` in
  [`clickstack-chart`](https://github.com/krateo-platformops/clickstack-chart).
- No ClickHouse schema/DDL ownership: the forked exporter *creates* the `otel_*` tables when
  `create_schema` is enabled, but retention/tuning policy is set chart-side.
- No CRDs: this component owns none (the release workflow's `crds` job no-ops here — there
  is no `make generate`).
