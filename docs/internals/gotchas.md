---
type: Architecture
title: otel-collector — gotchas
description: Runtime and build pitfalls grounded in this repo's code/config — in-cluster-only client, silent RBAC failures, logs-only enrichment, cache staleness, version pinning, the OpAMP trap.
resource: ghcr.io/krateo-platformops/otel-collector
tags: [observability, clickstack, otel, internals]
timestamp: 2026-08-07T00:00:00Z
---

# Gotchas

Real pitfalls, each grounded in this repo's code/config. Ordered roughly by how often they bite.

## 1. The processor only runs in-cluster (no out-of-cluster fallback)

`start` builds its client with `rest.InClusterConfig()` and returns the error if it fails
(`compositionresolver/processor.go:54-57`) — there is **no** kubeconfig fallback. The collector
will fail to start the `compositionresolver` processor outside a pod (no ServiceAccount token /
API host env). This is by design (it's a cluster-internal ingestion component) but means you
cannot exercise the resolve path with a local `otelcol-krateo` against a remote cluster.

## 2. The pod's RBAC must allow GET on every involvedObject kind

`resolveFromK8s` does a dynamic GET on the involvedObject's GVK (`processor.go:245`). The
ServiceAccount therefore needs `get` on **every** kind that can appear as an event's
involvedObject across **all** namespaces it watches — not just the Krateo Composition kinds. A
missing permission is **silent**: the GET error is only `Debug`-logged and the function returns `""`
(`processor.go:246-253`), so the record passes through unenriched with no WARN/ERROR. If
composition-ids are mysteriously absent, suspect RBAC first and turn on debug logging. (The
ClusterRole is owned by the chart, not this repo.)

## 3. Enrichment is logs-only

The factory registers **only** `WithLogs` (`factory.go:19`) and `processLogs` only walks log
records (`processor.go:117-130`). Metrics from `k8sclusterreceiver` are **not** stamped with a
composition-id — there is no metrics or traces consumer here. Don't expect the attribute on metric
streams in ClickHouse.

## 4. Negative-cache staleness window

A resolution that finds no label is cached as a negative entry for `NegativeCacheTTL` (default 30s,
`factory.go:26`; written in `cacheResult`, `processor.go:209-221`). If the controller adds the
`krateo.io/composition-id` label to an object **after** an event for it was already processed, the
attribute stays absent for up to 30s. Likewise a positive entry is held for `CacheTTL` (default 5m,
`factory.go:25`) keyed by UID, so a *label change* on the same object isn't re-read until the entry
expires. Both TTLs are tunable via `cache_ttl` / `negative_cache_ttl` (`config.go:12,17`) — raising
them cuts API load but widens the staleness window.

## 5. Cache keying is by involvedObject UID, deliberately

The cache key is the involvedObject `uid` (`processor.go:38`, written/read by UID throughout). A
record with an empty `uid` is skipped entirely (`processor.go:149-150`) — it is never resolved or
cached. So events whose body lacks `involvedObject.uid` always pass through unenriched, regardless
of whether the kind/name would resolve.

## 6. Body must be a K8s watch-event shape (string or map)

`extractInvolvedObject` accepts only string or map bodies (`processor.go:169-181`) and requires the
`{object:{involvedObject:{…}}}` JSON shape (`processor.go:141-145`). If an upstream processor
rewrites the log body into another structure before `compositionresolver` runs, extraction returns
false and the record is silently not enriched. Pipeline **ordering matters** — keep this processor
on the raw-event body. The runtime ordering lives in the chart config, not here.

## 7. Alpha stability — config surface is small and may shift

The processor is registered at `component.StabilityLevelAlpha` (`factory.go:20`). Only three config
keys exist — `cache_ttl`, `negative_cache_ttl`, `label_key` (`config.go:8-22`); anything else in
the collector config for this processor is rejected by strict-unmarshal. Don't assume a richer knob
set than these three.

## 8. Hard version pinning across OCB / contrib / client-go — and the two forks

`builder-config.yaml` pins every component to OTel `v0.118.0`, the `Dockerfile` pins OCB to
`v0.118.0` (`Dockerfile:7`), and `compositionresolver/go.mod` pins collector `v0.118.0`/`v1.24.0`
and `k8s.io/client-go v0.32.0` (`go.mod:6-12`). The vendored forks are part of the same set:
`k8sobjectsreceiver/go.mod` and `clickhouseexporter/go.mod` are the upstream v0.118.0 module files.
These move together: bumping contrib without bumping OCB and the in-tree components' `go.mod`s will
break the OCB build (collector API drift between minor versions) — and any bump requires
**re-diffing both forks** against the new upstream and re-applying the patches
([api.md](../api.md) records the exact deltas). Treat the version set as one unit.

## 9. The `gomod` versions for the local-path components are placeholders

The custom processor's module in `builder-config.yaml:23` is
`github.com/krateo-platformops/otel-collector/compositionresolver v0.0.1`. The module path is the
real repo path (`compositionresolver/go.mod:1`), but `v0.0.1` is never published as a tag and never
fetched — the build works **only** because of the `path: ./compositionresolver` override
(`builder-config.yaml:24`). The same applies to the two forks: their `gomod` entries name upstream
contrib `v0.118.0` (`builder-config.yaml:11,32`) but the local `path:` overrides
(`builder-config.yaml:12,33`) make OCB use the patched in-repo source. Don't try to `go get` the
compositionresolver path, and don't remove a `path:` line without deleting its vendored dir.

## 10. OpAMP / supervisor-mode collectors ignore the shipped relay config

This binary is built **without** the OpAMP supervisor/extension (it is not in
`builder-config.yaml`'s `extensions:` — only `healthcheckextension`, `builder-config.yaml:36-37`).
If a deployment ever wraps `otelcol-krateo` under an OpAMP supervisor, the supervisor manages the
*effective* collector config remotely and the relay/static config you ship (e.g. the chart's
collector YAML with the `compositionresolver` pipeline) can be **overridden or ignored** — the
processor may silently never run. Run this collector with its static chart-supplied config (the
intended mode), not under a supervisor, unless the supervisor's remote config is updated to include
the `compositionresolver` pipeline.
