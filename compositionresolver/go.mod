module github.com/braghettos/observability-stack/otel-collector-custom/compositionresolver

go 1.23.0

require (
	go.opentelemetry.io/collector/component v0.117.0
	go.opentelemetry.io/collector/consumer v1.23.0
	go.opentelemetry.io/collector/pdata v1.23.0
	go.opentelemetry.io/collector/processor v0.117.0
	go.uber.org/zap v1.27.0
	k8s.io/apimachinery v0.32.0
	k8s.io/client-go v0.32.0
)
