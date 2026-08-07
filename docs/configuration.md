---
type: Configuration
title: otel-collector — configuration
description: The config surface this repo owns — compositionresolver processor keys, the build-time OCB manifest, and the boundary with chart-owned runtime config.
resource: ghcr.io/krateo-platformops/otel-collector
tags: [observability, clickstack, otel]
timestamp: 2026-08-07T00:00:00Z
---

# Configuration

Two layers of configuration exist; this repo owns only the first:

1. **Build-time** — `builder-config.yaml` fixes *which* components are compiled into the
   binary (this repo).
2. **Runtime** — the collector YAML (which components are *used*, in what pipelines, against
   which endpoints) is supplied by `clickstack-chart/charts/otel-collector-deployment`
   (chart repo, not here).

## `compositionresolver` processor keys

The only custom runtime config surface defined in this repo (`compositionresolver/config.go`).
Strict-unmarshal: any key beyond these three is rejected at collector startup.

| Key | Type | Default | Source | Effect |
|---|---|---|---|---|
| `cache_ttl` | duration | `5m` | `config.go:12`, default `factory.go:25` | How long a resolved composition-id is cached per involvedObject UID before re-querying the API. Longer = less API pressure, slower label-change pickup. |
| `negative_cache_ttl` | duration | `30s` | `config.go:17`, default `factory.go:26` | TTL for "resolved but no label / unresolvable" results — shorter so a freshly-labeled object is picked up sooner. |
| `label_key` | string | `krateo.io/composition-id` | `config.go:21`, default `factory.go:27` | The Kubernetes label read from the involvedObject **and** the attribute key stamped on the log record. |

Validation (`config.go:24-35`): both TTLs must be positive; `label_key` must be non-empty.
The processor is registered at **alpha** stability (`factory.go:19`).

The chart's default runtime config sets exactly the defaults above (visible in the
`otel-collector-deployment` values under `config.processors.compositionresolver`).

## Forked components — no new keys

The two vendored forks add **no** configuration keys over upstream v0.118.0:

- `k8sobjectsreceiver`: the watch-recycle interval is a compile-time constant
  (`watchRecycleInterval = 10 * time.Minute`, `k8sobjectsreceiver/receiver.go:33`) — it is
  **not** tunable from the collector config.
- `clickhouseexporter`: the schema-recreate-and-retry path is gated by the **existing**
  upstream `create_schema` option (`shouldCreateSchema()`,
  `clickhouseexporter/config.go:182-183`); with `create_schema: false` the fork behaves like
  upstream (fails inserts until restart).

Full delta details: [api.md](./api.md).

## Build-time surface (`builder-config.yaml`)

The OCB manifest pins every component to OTel `v0.118.0` and points the three in-tree
components at their local paths (`./k8sobjectsreceiver`, `./compositionresolver`,
`./clickhouseexporter`). Adding a receiver/processor/exporter to the runtime config requires
adding it here first and cutting a new image release — see the component table in
[overview.md](./overview.md) and the version-set warning in
[internals/gotchas.md](./internals/gotchas.md).

## Everything else is chart-owned

ClickHouse endpoint/credentials/table names/TTL retention, pipeline ordering, RBAC,
resources, probes: `clickstack-chart/charts/otel-collector-deployment` values. This repo has
no environment variables of its own; env interpolation in the runtime config (e.g.
`${env:CH_USERNAME}`) is wired by the chart.
