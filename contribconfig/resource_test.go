// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

// The schema URL collector core v0.118.0 passes in service.New. This test is the regression guard
// for the Krateo patch in resource.go: without it newResource returns "conflicting Schema URL" and
// the collector exits at startup, which no go test in the other modules can see.
func TestNewResourceToleratesOlderSchemaURL(t *testing.T) {
	schema := "https://opentelemetry.io/schemas/1.26.0"
	res, err := newResource(&Resource{
		SchemaUrl:  &schema,
		Attributes: Attributes{"service.name": "otelcol-krateo"},
	})
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}
	if got, want := res.SchemaURL(), resource.Default().SchemaURL(); got != want {
		t.Errorf("SchemaURL = %q, want %q", got, want)
	}
	v, ok := res.Set().Value(attribute.Key("service.name"))
	if !ok || v.AsString() != "otelcol-krateo" {
		t.Errorf("service.name = %q (present %v), want otelcol-krateo", v.AsString(), ok)
	}
	if _, ok := res.Set().Value(attribute.Key("telemetry.sdk.language")); !ok {
		t.Error("telemetry.sdk.language missing: the Default() attributes were dropped")
	}
}
