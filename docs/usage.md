---
type: Usage
title: otel-collector — usage
description: How the image is consumed — the installer → clickstack-chart deployment path, direct image pull, and the local OCB build.
resource: ghcr.io/krateo-platformops/otel-collector
tags: [observability, clickstack, otel]
timestamp: 2026-08-07T00:00:00Z
---

# Usage

This is a **source repo**: it publishes one artifact, the container image
`ghcr.io/krateo-platformops/otel-collector`. **The charts live in
[`clickstack-chart`](https://github.com/krateo-platformops/clickstack-chart)** — there is no
chart here and nothing to `helm install` from this repo.

## The normal path (installer → ClickStack)

The Krateo installer deploys ClickStack as a composition; inside it, the
`otel-collector-deployment` chart (`clickstack-chart/charts/otel-collector-deployment`) runs
this image (its `values.yaml` pins `repository: ghcr.io/krateo-platformops/otel-collector`
with the release tag) and supplies everything runtime:

- the collector config (receivers/processors/exporters wiring, ClickHouse endpoint and
  credentials, the `compositionresolver` pipeline position),
- the ServiceAccount + RBAC the in-cluster components need,
- resources, probes (via `healthcheckextension`) and scheduling.

The sibling `otel-collector-daemonset` chart in the same repo runs the **upstream**
`otel/opentelemetry-collector-contrib` image, not this one — this image is deliberately
minimal (see the compiled-in component table in [overview.md](./overview.md)); e.g. it has
no `otlp` or `prometheus` receiver, and declaring an uncompiled receiver type in the runtime
config crashes the collector at startup.

## Direct image pull

```sh
docker pull ghcr.io/krateo-platformops/otel-collector:1.0.2
```

The tag equals this repo's git tag and the chart's `appVersion`. The binary is the image
entrypoint (`/otelcol-krateo`); run it with a collector config:

```sh
docker run --rm ghcr.io/krateo-platformops/otel-collector:1.0.2 --config /etc/otelcol/config.yaml
```

Note: any pipeline containing `compositionresolver` or `k8sobjects` requires an in-cluster
environment (both build their Kubernetes clients from the pod's ServiceAccount;
`compositionresolver` has **no kubeconfig fallback** — see
[internals/gotchas.md](./internals/gotchas.md)). For a runnable in-cluster manifest see
[examples/in-cluster-events](../examples/in-cluster-events/README.md).

## Local build

```sh
docker build -t otel-collector:dev .
```

The two-stage `Dockerfile` installs OCB `v0.118.0` and runs
`builder --config builder-config.yaml`; no local Go toolchain is needed. To build the bare
binary instead:

```sh
go install go.opentelemetry.io/collector/cmd/builder@v0.118.0
builder --config builder-config.yaml   # emits ./otelcol-krateo/otelcol-krateo
```

Keep the version set coherent when bumping anything — OCB, every `gomod` pin in
`builder-config.yaml`, and the three in-tree components' `go.mod`s move together
(gotcha 8 in [internals/gotchas.md](./internals/gotchas.md)).
