// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package clickhouseexporter

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
	"github.com/stretchr/testify/assert"
)

func TestIsTableMissing(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unknown table (code 60)", &proto.Exception{Code: unknownTableCode, Name: "UNKNOWN_TABLE"}, true},
		{"wrapped unknown table", fmt.Errorf("ExecContext:%w", &proto.Exception{Code: unknownTableCode}), true},
		{"other clickhouse error", &proto.Exception{Code: 241, Name: "MEMORY_LIMIT_EXCEEDED"}, false},
		{"non-clickhouse error", errors.New("boom"), false},
		{"sql.ErrNoRows", sql.ErrNoRows, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTableMissing(tt.err))
		})
	}
}
