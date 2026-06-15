# krateo-otel-collector

Custom **OpenTelemetry Collector** distribution for the Krateo ClickStack observability path,
built with the OTel Collector Builder (`builder-config.yaml`) and a Krateo `compositionresolver`
component. Deployed by the `otel-collector-deployment` chart in
[`krateo-clickstack-chart`](https://github.com/braghettos/krateo-clickstack-chart).

> Split out of the former multi-image `krateo-clickstack` code repo so each component is a
> single-image repo on the canonical Krateo CI. Image renamed `otelcol-krateo` →
> `krateo-otel-collector` to match the repo name (krateo-* naming standard).

## Build & release
Image: `ghcr.io/braghettos/krateo-otel-collector`. Pushing a semver tag (`X.Y.Z`) builds and
pushes a multi-platform (`linux/amd64,linux/arm64`) image via the canonical `release-tag.yaml`.
