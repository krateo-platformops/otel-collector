# Behavior (runtime)

This document covers the runtime: the collector pipeline at a glance, then the
`compositionresolver` enrichment contract in detail — what it consumes, what it stamps, and the
cache lifecycle — all traced to `compositionresolver/`.

---

## The pipeline at a glance

The binary is a standard OTel Collector. Its component set (`builder-config.yaml`, see
[architecture.md](architecture.md)) supports, for the ClickStack ingestion path:

```
receivers            processors                                         exporters
─────────            ──────────                                         ─────────
k8sobjectsreceiver → memorylimiter → batch → k8sattributes →           clickhouseexporter
(K8s events,         resource → compositionresolver                    (→ ClickHouse)
 watch mode)                                                            debugexporter
k8sclusterreceiver                                                      (debug)
(cluster metrics)
```

The exact pipeline ordering and endpoints come from the **runtime collector config** in
`krateo-platformops/clickstack-chart` (`otel-collector-deployment`); this repo only fixes *which*
components exist. The load-bearing custom behavior — the part downstream consumers depend on — is
the `compositionresolver` stage, below.

What flows to ClickHouse: K8s **event log records** (enriched with `krateo.io/composition-id`
where resolvable) via `clickhouseexporter`, and **cluster metrics** from `k8sclusterreceiver`.

---

## The composition-id enrichment contract

**Goal:** every Kubernetes-event record that ultimately came from an object belonging to a Krateo
Composition should carry a `krateo.io/composition-id` attribute, so the telemetry in ClickHouse
can be filtered/joined by composition. This is the contract that downstream **sse-proxy `/events`**
relies on to attribute events to a composition.

### Input it consumes

`extractInvolvedObject` (`processor.go:166-193`) reads the log record **body** — accepting either a
string body (`pcommon.ValueTypeStr`, `processor.go:170`) or a map body (`ValueTypeMap`,
JSON-marshalled, `processor.go:172-178`); any other body type is skipped. It JSON-unmarshals the
body into `k8sEventBody` (`processor.go:141-145`) and pulls `object.involvedObject`
(`involvedObjectRef`, `processor.go:133-139`): `apiVersion`, `kind`, `name`, `namespace`, `uid`.
A record with no `kind`/`name` is skipped (`processor.go:189-191`).

This is the shape emitted by `k8sobjectsreceiver` watching K8s `Event` objects — each event names
the `involvedObject` it concerns.

### Resolution (per record)

`enrichLogRecord` (`processor.go:147-162`):

1. Extract the `involvedObject`; skip if absent or `uid == ""` (`processor.go:148-151`).
2. Look up the UID in the TTL cache (`lookupCached`, `processor.go:153`).
3. On a miss, resolve from the live cluster (`resolveFromK8s`) and cache the result
   (`processor.go:154-157`).
4. If a non-empty composition-id was resolved, stamp it as an attribute under
   `Config.LabelKey` (default `krateo.io/composition-id`) via
   `lr.Attributes().PutStr(...)` (`processor.go:159-161`). An empty result stamps **nothing** —
   the attribute is simply absent, never set to `""`.

`resolveFromK8s` (`processor.go:226-260`):

- Parse `apiVersion`+`kind` into a GVK (`parseGVK`, `processor.go:263-266`).
- Map GVK → resource via the `RESTMapper` (`processor.go:229`); on failure, debug-log and return
  `""` (`processor.go:230-236`).
- GET the object with the dynamic client, namespaced or cluster-scoped per the mapping's scope
  (`processor.go:238-253`).
- Read the object's labels and return `labels[LabelKey]` (`processor.go:255-259`).

> The code comment at `processor.go:223-225` notes this mirrors what the Krateo EventRouter does in
> `labels.go → findCompositionID()` — same label, resolved from the involvedObject.

### Cache lifecycle

The cache (`map[string]cacheEntry` keyed by involvedObject UID, `processor.go:38`, entry shape
`processor.go:24-29`) is the API-pressure guard — it avoids a cluster GET per event.

- **Positive entries** live `CacheTTL` (default 5m, `factory.go:25`).
- **Negative entries** (resolved but no label, *or* unresolvable) live `NegativeCacheTTL`
  (default 30s, `factory.go:26`) — written by `cacheResult` choosing the shorter TTL when the id
  is empty (`processor.go:209-221`). Shorter so a label added shortly after object creation is
  picked up sooner.
- **Reads** check expiry inline: an expired entry returns a miss (`lookupCached`,
  `processor.go:195-207`).
- **Eviction**: a background goroutine (`evictExpiredEntries`, `processor.go:88-113`) runs every
  60s, deleting expired entries to bound memory; it stops on `shutdown` closing `stopEvict`
  (`processor.go:81-84`, `109-110`).

### Output contract (what downstream gets)

Enriched **log records** carry an attribute `krateo.io/composition-id = <the object's label value>`
when resolvable. The label value is the Composition resource's UID (per `config.go:19-21`).
Consumers — ClickHouse queries and **sse-proxy `/events`** — key on this attribute to attribute an
event to its composition. Absence of the attribute means "not resolvable to a composition" (no
label, unmapped kind, GET failure, or a non-K8s-event record) — it is never an error in the stream.
