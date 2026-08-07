# otel-collector

Custom OpenTelemetry Collector distribution for the Krateo ClickStack ingestion path — OCB-built
from upstream contrib components plus three in-tree Krateo components.

## What is this

A **source repo** (image, no charts): the OTel Collector Builder assembles `otelcol-krateo` from
`builder-config.yaml` — upstream contrib components plus the custom `compositionresolver`
processor (stamps `krateo.io/composition-id` on K8s-event log records) and two patched forks,
`k8sobjectsreceiver` (watch-stall recovery) and `clickhouseexporter` (schema recreation on
`UNKNOWN_TABLE`). Deployed by the `otel-collector-deployment` chart in
[`clickstack-chart`](https://github.com/krateo-platformops/clickstack-chart).
Full picture: [docs/index.md](docs/index.md).

## Install

This repo ships an image, not a chart — it is installed by the Krateo installer through
ClickStack. The published artifact:

```sh
docker pull ghcr.io/krateo-platformops/otel-collector:1.0.2
```

Deployment (chart values, RBAC, the runtime collector config) lives in
`clickstack-chart/charts/otel-collector-deployment` — see [docs/usage.md](docs/usage.md).

## Configure

See [docs/configuration.md](docs/configuration.md). The config this repo owns
(`compositionresolver` processor keys; everything else is chart-side):

| Setting | Default | Effect |
|---|---|---|
| `cache_ttl` | `5m` | TTL for resolved composition-ids (API-pressure vs label-change staleness). |
| `negative_cache_ttl` | `30s` | TTL for "no label / unresolvable" results — re-checked sooner. |
| `label_key` | `krateo.io/composition-id` | The label read from the involvedObject and the attribute stamped. |

## Examples

- [examples/in-cluster-events](examples/in-cluster-events) — standalone in-cluster deploy:
  K8s events → `compositionresolver` → debug exporter (no ClickHouse needed).

## Docs

- [docs/index.md](docs/index.md) — the map
- [docs/overview.md](docs/overview.md) — how the binary is assembled (OCB) + the custom processor
- [docs/usage.md](docs/usage.md) — how the image is consumed (charts live in clickstack-chart)
- [docs/configuration.md](docs/configuration.md) — the whole config surface this repo owns
- [docs/api.md](docs/api.md) — the component contract + the fork deltas vs upstream v0.118.0
- [docs/examples.md](docs/examples.md) — examples index
- [docs/release.md](docs/release.md) — how a release ships
- [docs/log.md](docs/log.md) — curated history

Internals (code-traced): [docs/internals/behavior.md](docs/internals/behavior.md) and
[docs/internals/gotchas.md](docs/internals/gotchas.md).

## Develop & release

`docker build .` runs the full OCB build (Go toolchain optional locally). Tag `X.Y.Z`
(no `v`) ships the multi-platform image — release runbook: [docs/release.md](docs/release.md).
