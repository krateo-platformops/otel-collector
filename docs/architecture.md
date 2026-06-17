# Architecture

krateo-otel-collector is **not** a hand-written service: it is a custom OpenTelemetry Collector
binary assembled at build time by the **OTel Collector Builder (OCB)** from a declarative
manifest, plus one in-tree custom processor. Two artifacts define it:

1. `builder-config.yaml` — the OCB manifest listing every component compiled into the binary.
2. `compositionresolver/` — the one custom component (a logs processor) that is not from upstream.

---

## How the collector is built (OCB)

`builder-config.yaml` is consumed by `go.opentelemetry.io/collector/cmd/builder` to generate and
compile the `otelcol-krateo` binary. `Dockerfile` runs it in a two-stage build:

- Stage 1 (`golang:1.23-alpine`, `Dockerfile:1`): installs OCB pinned at `v0.118.0`
  (`Dockerfile:7`), copies `builder-config.yaml` and `compositionresolver/`, then runs
  `builder --config builder-config.yaml` (`Dockerfile:15`) which emits `./otelcol-krateo/otelcol-krateo`.
- Stage 2 (`gcr.io/distroless/base-debian12`, `Dockerfile:18`): copies the static binary and CA
  certs, runs as non-root UID `10001` (`Dockerfile:20-21`), entrypoint `/otelcol-krateo`
  (`Dockerfile:26`).

The dist is named `otelcol-krateo` with `output_path: ./otelcol-krateo` (`builder-config.yaml:2,4`).

### Components compiled in

Every component below is pinned to OTel `v0.118.0` (the custom processor to its local path). This
is the complete component set the binary can be configured with — anything not here cannot appear
in the runtime collector config the chart supplies.

| Class | Component | gomod | Purpose |
|---|---|---|---|
| receiver | `k8sobjectsreceiver` | `builder-config.yaml:8` | watches K8s objects (events in watch mode) |
| receiver | `k8sclusterreceiver` | `builder-config.yaml:9` | cluster-level metrics |
| processor | `batchprocessor` | `builder-config.yaml:13` | batching |
| processor | `memorylimiterprocessor` | `builder-config.yaml:14` | memory back-pressure |
| processor | `k8sattributesprocessor` | `builder-config.yaml:16` | enrich with K8s metadata |
| processor | `resourceprocessor` | `builder-config.yaml:17` | resource attribute edits |
| **processor** | **`compositionresolver`** | **`builder-config.yaml:19-20` (local `./compositionresolver`)** | **custom: stamps `krateo.io/composition-id`** |
| exporter | `clickhouseexporter` | `builder-config.yaml:23` | the ingestion sink → ClickHouse |
| exporter | `debugexporter` | `builder-config.yaml:24` | debug/stdout |
| extension | `healthcheckextension` | `builder-config.yaml:27` | health endpoint |

The custom processor is declared with a `path:` override so OCB builds it from the in-repo source
rather than fetching the (placeholder `v0.0.1`) module path (`builder-config.yaml:19-20`).

---

## The `compositionresolver` processor

A custom **logs** processor (the only logs-pipeline custom code in the repo). Its job: for each
log record carrying a raw Kubernetes watch event, look up the event's `involvedObject` in the
cluster, read that object's `krateo.io/composition-id` label, and stamp it onto the log record as
an attribute — so telemetry landing in ClickHouse is keyed by composition.

It is a standard OTel processor split across three files:

### `factory.go` — registration & defaults

- Component type is `compositionresolver` (`factory.go:13`).
- `NewFactory` registers it as a **logs** processor at `StabilityLevelAlpha` (`factory.go:15-21`).
- `createDefaultConfig` (`factory.go:23-29`) sets the defaults:
  - `CacheTTL = 5m`
  - `NegativeCacheTTL = 30s`
  - `LabelKey = "krateo.io/composition-id"`
- `createLogsProcessor` (`factory.go:31-45`) wraps the processor with `processorhelper.NewLogs`,
  wiring `processLogs` as the consume hook and `start`/`shutdown` as lifecycle hooks.

### `config.go` — typed config + validation

`Config` (`config.go:8-22`) exposes three mapstructure keys — `cache_ttl`, `negative_cache_ttl`,
`label_key`. `Validate` (`config.go:24-35`) rejects non-positive TTLs and an empty `label_key`.

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

The enrichment path and cache lifecycle are documented in [behavior.md](behavior.md).

### Pipeline position

Because it resolves and stamps the composition-id, `compositionresolver` belongs in the **logs
pipeline** (it implements only `WithLogs`, `factory.go:19`) — between the K8s-events source
(`k8sobjectsreceiver`) and the `clickhouseexporter`. Placing it after `k8sattributesprocessor`
means resource metadata is already present; the actual ordering is set by the **runtime collector
config** shipped from `krateo-clickstack-chart` (this repo only compiles the component in — it does
not pin the config). See the chart repo `docs/` for the deployed pipeline wiring.

---

## What this repo does NOT contain

- No runtime collector YAML (receivers/exporters endpoints, the actual pipeline ordering): that is
  the chart's `otel-collector-deployment` config in `braghettos/krateo-clickstack-chart`.
- No ClickHouse schema/DDL: owned by the ClickHouse side of ClickStack.
- No CRDs: this component owns none (`release-tag.yaml`'s crd job no-ops here — no `make generate`).
