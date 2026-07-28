package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// searchPathQuery sets the session's schema as one identifier, through a bound parameter.
//
// quote_ident is not decoration. A bound parameter stops SQL injection, but search_path is a
// list-valued setting, so PostgreSQL parses the value it is given as comma-separated entries --
// a schema of "public, pg_catalog" would silently become two of them. quote_ident makes it one
// identifier, so an injected value names a schema nobody has rather than changing what the
// session resolves.
const searchPathQuery = `SELECT set_config('search_path', quote_ident($1), false)`

// parseDSN turns a connection string into a pgx config, withholding the string when it will not.
func parseDSN(dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, &DSNError{Err: err}
	}

	return cfg, nil
}

// applySearchPath points the session at the configured schema, if migr.yaml named one.
func applySearchPath(ctx context.Context, conn *pgx.Conn, schema string) error {
	if strings.TrimSpace(schema) == "" {
		return nil
	}

	if _, err := conn.Exec(ctx, searchPathQuery, schema); err != nil {
		return fmt.Errorf("%w: setting the search path to %q: %w", ErrConnect, schema, err)
	}

	return nil
}
