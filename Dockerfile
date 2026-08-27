FROM golang:1.23-alpine AS builder

RUN apk add --no-cache ca-certificates git

# Force HTTP/1.1 for all Go toolchain fetches. OCB's internal `go mod tidy`
# downloads ~hundreds of modules and verifies them against sum.golang.org over
# HTTP/2; under load the proxy/sumdb resets streams with
# "stream error: ... INTERNAL_ERROR; received from peer", which fails the whole
# build. HTTP/1.1 sidesteps the stream-reset class entirely. Retry (below) covers
# any residual transient blips; the module cache makes retries cheap.
ENV GODEBUG=http2client=0

# Install OCB (OpenTelemetry Collector Builder)
RUN --mount=type=cache,target=/root/.cache/go-build \
    go install go.opentelemetry.io/collector/cmd/builder@v0.118.0

WORKDIR /build
COPY builder-config.yaml .
COPY compositionresolver/ compositionresolver/
COPY k8sobjectsreceiver/ k8sobjectsreceiver/
COPY clickhouseexporter/ clickhouseexporter/

# Retry OCB up to 3× — the module cache mount means a retry re-uses everything
# already downloaded, so a transient proxy.golang.org/sum.golang.org blip on one
# attempt is recovered on the next in seconds. Fails the layer only if all 3 fail.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    for i in 1 2 3; do \
      builder --config builder-config.yaml && exit 0; \
      echo "OCB build attempt $i failed; retrying in 10s..."; sleep 10; \
    done; \
    echo "OCB build failed after 3 attempts"; exit 1

# ---
FROM gcr.io/distroless/base-debian12:latest

ARG USER_UID=10001
USER ${USER_UID}

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 --from=builder /build/otelcol-krateo/otelcol-krateo /otelcol-krateo

ENTRYPOINT ["/otelcol-krateo"]
