package db

import (
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrorAttrs returns log attributes for err: the full wrapped error chain
// and, when a Postgres error is in the chain, its SQLSTATE and the fields
// that say where it came from. Handlers log these before answering 500, so
// an unexpected database error is always diagnosable from one log line.
func ErrorAttrs(err error) []any {
	attrs := []any{slog.String("error", err.Error())}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return attrs
	}
	attrs = append(attrs, slog.String("sqlstate", pgErr.Code), slog.String("pg_severity", pgErr.Severity),
		slog.String("pg_message", pgErr.Message))
	for _, kv := range [][2]string{
		{"pg_detail", pgErr.Detail}, {"pg_hint", pgErr.Hint}, {"pg_where", pgErr.Where},
		{"pg_schema", pgErr.SchemaName}, {"pg_table", pgErr.TableName}, {"pg_column", pgErr.ColumnName},
		{"pg_constraint", pgErr.ConstraintName}, {"pg_routine", pgErr.Routine},
	} {
		if kv[1] != "" {
			attrs = append(attrs, slog.String(kv[0], kv[1]))
		}
	}
	return attrs
}
