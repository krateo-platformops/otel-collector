// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package clickhouseexporter // import "github.com/open-telemetry/opentelemetry-collector-contrib/exporter/clickhouseexporter"

import (
	"errors"

	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
)

// unknownTableCode is the ClickHouse server error code for UNKNOWN_TABLE. It is
// returned by INSERT/SELECT when the target table does not exist.
const unknownTableCode int32 = 60

// isTableMissing reports whether err was caused by a missing ClickHouse table.
//
// The exporter creates its schema once, in start(). If ClickHouse is later
// re-provisioned with a fresh, empty database while the exporter keeps running,
// the connection re-establishes transparently through the database/sql pool but
// the target tables are gone, so every insert fails with UNKNOWN_TABLE and data
// is dropped until the collector is manually restarted. Detecting this lets the
// exporter recreate the schema and retry (when create_schema is enabled) instead.
func isTableMissing(err error) bool {
	var ex *proto.Exception
	if errors.As(err, &ex) {
		return ex.Code == unknownTableCode
	}
	return false
}
