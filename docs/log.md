---
type: Log
title: otel-collector — log
description: Curated chronological history of the otel-collector distribution — forks, migrations, notable fixes.
resource: ghcr.io/krateo-platformops/otel-collector
tags: [observability, clickstack, otel]
timestamp: 2026-08-07T00:00:00Z
---

# Log

Curated history, newest first. Release notes live in GitHub Releases.

## 2026-08-07
- Adopted the Krateo Documentation Standard (this bundle). The former
  `docs/{architecture,behavior,gotchas}.md` were re-derived against current source:
  `architecture.md` became [overview.md](./overview.md) (its "one custom component" framing
  and its `builder-config.yaml`/`Dockerfile` line references predated the two vendored
  forks and were corrected); `behavior.md`/`gotchas.md` moved to
  [internals/](./internals/behavior.md) with stale references fixed. Fork deltas vs
  upstream v0.118.0 are now documented in [api.md](./api.md).

## 2026-07 (≈ tag 1.0.2)
- **Forked `clickhouseexporter`** (PR #3): recreate the `otel_*` schema and retry once when
  an insert fails with `UNKNOWN_TABLE` (code 60) — i.e. ClickHouse was re-provisioned with a
  fresh DB while the collector kept running — instead of dropping telemetry until a manual
  restart. Gated by the existing `create_schema` option; upstream PR pending
  (needs re-implementation on upstream's refactored `driver.Conn` main).
- CI: `release-tag.yaml` build job moved to the shared reusable multi-platform
  `component-image-build.yaml` (PR #4).

## 2026-06
- **Forked `k8sobjectsreceiver`** (PR #2, KOS-1 #112 bug 2): the upstream v0.118.0 watch
  stalls silently (no error, no channel close) when the connection dies, hanging event
  ingestion forever. The fork recycles the watch every 10 minutes and re-establishes (rather
  than stops) on channel close.
- Org migration: all references re-pointed to `krateo-platformops`; security CI moved to the
  org reusable (git-mode gitleaks).
- Code-repo doc set added (PR #1): architecture/behavior/gotchas + llms.txt (now re-homed,
  see 2026-08-07).
- Split out of the former multi-image clickstack code repo into this single-image repo on
  the canonical Krateo CI; published image renamed `otelcol-krateo` →
  `krateo-otel-collector` repository path `ghcr.io/krateo-platformops/otel-collector`
  (the binary/dist name inside the image remains `otelcol-krateo`).

## Earlier (in-clickstack era)
- Upgraded the whole component set to OTel `v0.118.0` and re-enabled the metrics pipeline;
  dropped the deprecated `otelcol_version` OCB field.
- **`compositionresolver` processor introduced**, replacing the Krateo EventRouter: resolves
  `krateo.io/composition-id` from the event's involvedObject labels and stamps it on log
  records, with a UID-keyed TTL cache.
