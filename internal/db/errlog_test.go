package db_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sheshakanthra/sluice/internal/db"
)

func TestErrorAttrs(t *testing.T) {
	pgErr := &pgconn.PgError{Severity: "ERROR", Code: "23514", Message: "new row violates check constraint",
		TableName: "seats", ConstraintName: "seat_state_shape"}
	tests := []struct {
		name string
		err  error
		want map[string]string
	}{
		{"plain error", errors.New("boom"), map[string]string{"error": "boom"}},
		{"wrapped pg error", fmt.Errorf("extend seats: %w", fmt.Errorf("tx: %w", pgErr)), map[string]string{
			"error":         "extend seats: tx: ERROR: new row violates check constraint (SQLSTATE 23514)",
			"sqlstate":      "23514",
			"pg_severity":   "ERROR",
			"pg_message":    "new row violates check constraint",
			"pg_table":      "seats",
			"pg_constraint": "seat_state_shape",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.New(slog.NewJSONHandler(&buf, nil)).Error("x", db.ErrorAttrs(tc.err)...)
			var got map[string]any
			if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %v, want %q", k, got[k], v)
				}
			}
			if _, ok := got["pg_detail"]; ok {
				t.Error("empty fields must be omitted")
			}
		})
	}
}
