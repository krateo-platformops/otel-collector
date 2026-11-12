// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package config // import "go.opentelemetry.io/contrib/config"

import (
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

func keyVal(k string, v any) attribute.KeyValue {
	switch val := v.(type) {
	case bool:
		return attribute.Bool(k, val)
	case int64:
		return attribute.Int64(k, val)
	case uint64:
		return attribute.String(k, fmt.Sprintf("%d", val))
	case float64:
		return attribute.Float64(k, val)
	case int8:
		return attribute.Int64(k, int64(val))
	case uint8:
		return attribute.Int64(k, int64(val))
	case int16:
		return attribute.Int64(k, int64(val))
	case uint16:
		return attribute.Int64(k, int64(val))
	case int32:
		return attribute.Int64(k, int64(val))
	case uint32:
		return attribute.Int64(k, int64(val))
	case float32:
		return attribute.Float64(k, float64(val))
	case int:
		return attribute.Int(k, val)
	case uint:
		return attribute.String(k, fmt.Sprintf("%d", val))
	case string:
		return attribute.String(k, val)
	default:
		return attribute.String(k, fmt.Sprint(v))
	}
}

func newResource(res *Resource) (*resource.Resource, error) {
	if res == nil || res.Attributes == nil {
		return resource.Default(), nil
	}
	var attrs []attribute.KeyValue

	for k, v := range res.Attributes {
		attrs = append(attrs, keyVal(k, v))
	}

	// Krateo patch: upstream v0.10.0 builds the second resource with
	// NewWithAttributes(*res.SchemaUrl, ...). Collector core v0.118.0 passes the semconv 1.26.0
	// schema URL there, and resource.Default() carries a newer one from otel/sdk v1.37.0 on
	// (semconv 1.43.0 at v1.47.0), so resource.Merge fails with "conflicting Schema URL" and the
	// collector exits at startup.
	// Schemaless takes the schema URL from Default() and keeps the merged attributes unchanged.
	return resource.Merge(resource.Default(), resource.NewSchemaless(attrs...))
}
