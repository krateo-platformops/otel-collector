---
type: Example
title: otel-collector — in-cluster events example
description: Standalone in-cluster deploy of the krateo otel-collector image — K8s events → compositionresolver → debug exporter, no ClickHouse needed.
resource: ghcr.io/krateo-platformops/otel-collector
tags: [observability, clickstack, otel, example]
timestamp: 2026-08-07T00:00:00Z
---

# In-cluster events example

The smallest setup that exercises the custom `compositionresolver` processor end-to-end:
this image runs as a one-replica Deployment, watches Kubernetes `Event` objects cluster-wide
(`k8sobjects` receiver, watch mode), stamps `krateo.io/composition-id` on records whose
involvedObject carries that label, and prints every record via the `debug` exporter — **no
ClickHouse required**.

## Preconditions

- Any Kubernetes cluster (kind is fine) and permissions to create a ClusterRole/Binding —
  the manifest grants cluster-wide `get` broadly because the resolver GETs whatever kind an
  event's involvedObject names (narrow it in production; the real RBAC is owned by the
  `otel-collector-deployment` chart in
  [clickstack-chart](https://github.com/krateo-platformops/clickstack-chart)).
- Pull access to `ghcr.io/krateo-platformops/otel-collector:1.0.2`.
- The processor **requires** running in-cluster (`rest.InClusterConfig()`, no kubeconfig
  fallback) — this cannot be run as a local process.

## Run

```sh
kubectl apply -f ./manifest.yaml
```

Then watch the enrichment happen:

```sh
kubectl logs -n otel-collector-example deploy/otel-collector-example -f
```

Any event whose involvedObject is labeled `krateo.io/composition-id` (e.g. resources of a
Krateo Composition) shows the attribute on the exported record; unlabeled objects pass
through unenriched — that is the contract, not an error.

## Cleanup

```sh
kubectl delete -f ./manifest.yaml
```
