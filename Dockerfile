FROM golang:1.23-alpine AS builder

RUN apk add --no-cache ca-certificates git

# Install OCB (OpenTelemetry Collector Builder)
RUN --mount=type=cache,target=/root/.cache/go-build \
    go install go.opentelemetry.io/collector/cmd/builder@v0.117.0

WORKDIR /build
COPY builder-config.yaml .
COPY compositionresolver/ compositionresolver/

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    builder --config builder-config.yaml

# ---
FROM gcr.io/distroless/base-debian12:latest

ARG USER_UID=10001
USER ${USER_UID}

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 --from=builder /build/otelcol-krateo/otelcol-krateo /otelcol-krateo

ENTRYPOINT ["/otelcol-krateo"]
