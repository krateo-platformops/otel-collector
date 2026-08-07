---
type: Runbook
title: otel-collector — release
description: How a release ships — plain-semver tag → shared multi-platform image build → ghcr.io, then the chart-side appVersion bump in clickstack-chart.
resource: ghcr.io/krateo-platformops/otel-collector
tags: [observability, clickstack, release]
timestamp: 2026-08-07T00:00:00Z
---

# Release

One artifact: the image `ghcr.io/krateo-platformops/otel-collector`. Latest released tag:
`1.0.2`.

## Ship

1. Merge to `main`.
2. Tag **plain semver, no `v` prefix** (the trigger is `tags: ["[0-9]+.[0-9]+.[0-9]+"]`,
   `.github/workflows/release-tag.yaml`):

   ```sh
   git tag X.Y.Z && git push origin X.Y.Z
   ```

3. CI (`release-tag.yaml`) runs two jobs:
   - **build** — calls the shared reusable
     `krateo-platformops/.github/.github/workflows/component-image-build.yaml@main` with
     `modules: '[{"mod":"otel-collector","context":"."}]'`: a multi-platform
     (`linux/amd64,linux/arm64`) Docker build of the repo root (the two-stage OCB
     `Dockerfile`), pushed as `ghcr.io/krateo-platformops/otel-collector:X.Y.Z`.
   - **crds** — the canonical component-repo CRD-publish job. This repo has **no
     `make generate` target**, so the job detects a non-CRD-owner and skips cleanly
     (expected: "No 'make generate' target … skipping CRD publish").

## Verify

- The `build` job is green and the tag is visible on the GHCR package
  (`ghcr.io/krateo-platformops/otel-collector`).
- `docker pull ghcr.io/krateo-platformops/otel-collector:X.Y.Z` works for both platforms.

## Propagate (chart side)

The image is consumed by `clickstack-chart/charts/otel-collector-deployment`
(`values.yaml` → `image.tag`, kept equal to the chart `appVersion`). After releasing here,
open the clickstack-chart PR bumping that pin and release the chart; the installer then picks
up the new chart version. Nothing in this repo self-propagates.

## Version-set rule

An OTel version bump is never image-only: OCB (`Dockerfile:7`), every `gomod` pin in
`builder-config.yaml`, and the three in-tree components' `go.mod`s move together, and both
vendored forks must be re-diffed against the new upstream (deltas in [api.md](./api.md)).
