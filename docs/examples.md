---
type: ExampleIndex
title: otel-collector — examples
description: Index of the runnable examples under examples/.
resource: ghcr.io/krateo-platformops/otel-collector
tags: [observability, clickstack, otel]
timestamp: 2026-08-07T00:00:00Z
---

# Examples

- [in-cluster-events](../examples/in-cluster-events/README.md) — standalone in-cluster
  deployment of this image: watches K8s events, enriches them with `compositionresolver`,
  prints them via the `debug` exporter. No ClickHouse required — the smallest setup that
  exercises the custom processor end-to-end. Preconditions: any Kubernetes cluster and
  cluster-admin enough to create a ClusterRole; one `kubectl apply -f manifest.yaml`.

The production wiring (ClickHouse exporter, retention, RBAC, pipeline ordering) is not an
example here — it is the `otel-collector-deployment` chart in
[`clickstack-chart`](https://github.com/krateo-platformops/clickstack-chart).
