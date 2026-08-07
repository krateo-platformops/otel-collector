---
type: Component
title: otel-collector — index
description: The map of the otel-collector doc bundle — the custom OTel Collector distribution for Krateo ClickStack (OCB build + compositionresolver + two patched upstream forks).
resource: ghcr.io/krateo-platformops/otel-collector
tags: [observability, clickstack, otel]
timestamp: 2026-08-07T00:00:00Z
---

# otel-collector

otel-collector is the **custom OpenTelemetry Collector distribution** of Krateo ClickStack —
the telemetry → ClickHouse ingestion path. This is a **source repo**: the OTel Collector
Builder (OCB) compiles `otelcol-krateo` from `builder-config.yaml`, combining upstream
contrib components with three in-tree components — the wholly custom `compositionresolver`
logs processor and two minimally patched forks of upstream v0.118.0 components
(`k8sobjectsreceiver`, `clickhouseexporter`). It ships one artifact, the image
`ghcr.io/krateo-platformops/otel-collector`; all charts, RBAC and the runtime collector
config live in [`clickstack-chart`](https://github.com/krateo-platformops/clickstack-chart)
(`charts/otel-collector-deployment`).

## The bundle (start here)

- [overview](./overview.md) — how the binary is assembled: the OCB manifest, the two-stage
  Docker build, the full component table, the `compositionresolver` processor.
- [usage](./usage.md) — how the image is consumed: the installer → clickstack-chart path,
  pulling the image, local build.
- [configuration](./configuration.md) — the config surface this repo owns
  (`compositionresolver` keys, build-time manifest) vs what the chart owns.
- [api](./api.md) — the contract: the composition-id attribute contract + the precise
  fork deltas vs upstream contrib v0.118.0.
- [examples](./examples.md) — the runnable example under `examples/`.
- [release](./release.md) — how a release ships (plain-semver tag → multi-platform image).
- [log](./log.md) — curated history.
- [llms.txt](./llms.txt) — the version-pinned agent index of this bundle.

## Internals (code-adjacent deep dives)

- [internals/behavior.md](./internals/behavior.md) — the runtime pipeline and the
  composition-id enrichment contract, traced at `file:line`.
- [internals/gotchas.md](./internals/gotchas.md) — runtime pitfalls grounded in code/config:
  in-cluster-only client, silent RBAC failures, logs-only enrichment, cache staleness,
  version pinning, the OpAMP trap.

## Version pinning (agents)

You are reading docs for a specific deployed build. The image tag
(`ghcr.io/krateo-platformops/otel-collector:<tag>`) equals this repo's git tag and the
chart's `appVersion` — read this repo's files **at that tag**, not `main`. The chart-side
view (pipeline wiring, endpoints, RBAC) is versioned by
`CompositionDefinition.spec.chart.version` and documented in the `clickstack-chart` repo.
