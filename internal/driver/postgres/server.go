package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/capability"
)

// uuidFunction is the SQL this driver emits for the neutral uuid default function.
const uuidFunction = "gen_random_uuid()"

// uuidVolatilityQuery asks the server whether it has that function, and how it behaves.
//
// to_regprocedure returns null rather than erroring for a name the server does not have, so one
// round trip answers both questions. Asking beats testing the version: gen_random_uuid moved into
// core in PostgreSQL 13, but pgcrypto provides it on older servers, and a version test would
// refuse a server that has it (CLAUDE.md invariant #13).
const uuidVolatilityQuery = `SELECT provolatile FROM pg_proc WHERE oid = to_regprocedure($1)`

// volatile is PostgreSQL's provolatile code for a function returning a different value per call.
const volatile = "v"

// uuidInCoreRelease is where gen_random_uuid stopped needing pgcrypto, named only in a message.
const uuidInCoreRelease = 13

// schemaExistsQuery reports whether the configured schema is one the server actually has.
const schemaExistsQuery = `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`

// transactionalDDL is true for every PostgreSQL release, and proven by an integration test.
const transactionalDDL = true

// serverFacts is everything about a connected server that a capability rule closes over.
type serverFacts struct {
	version     serverVersion
	uuidDefault defaultFunction
	capture     capability.Capture
}

// defaultFunction is what a server reports about a function this driver emits as a column default.
type defaultFunction struct {
	exists   bool
	volatile bool
}

// gatherFacts asks the server everything the declaration depends on, once, inside Open.
func gatherFacts(ctx context.Context, conn *pgx.Conn) (serverFacts, error) {
	version, err := detectVersion(ctx, conn)
	if err != nil {
		return serverFacts{}, err
	}

	uuidDefault, err := describeFunction(ctx, conn, uuidFunction)
	if err != nil {
		return serverFacts{}, err
	}

	return serverFacts{version: version, uuidDefault: uuidDefault, capture: readCapture(ctx, conn)}, nil
}

// describeFunction reports whether the server has a function and whether it is volatile.
func describeFunction(ctx context.Context, conn *pgx.Conn, signature string) (defaultFunction, error) {
	var reported string

	err := conn.QueryRow(ctx, uuidVolatilityQuery, signature).Scan(&reported)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultFunction{}, nil
	}

	if err != nil {
		return defaultFunction{}, fmt.Errorf("%w: asking about %s: %w", ErrConnect, signature, err)
	}

	return defaultFunction{exists: true, volatile: reported == volatile}, nil
}

// checkSchemaExists reports a configured schema the server does not have, which it would ignore.
func checkSchemaExists(ctx context.Context, conn *pgx.Conn, schema string) error {
	var exists bool

	if err := conn.QueryRow(ctx, schemaExistsQuery, schema).Scan(&exists); err != nil {
		return fmt.Errorf("%w: looking for schema %q: %w", ErrConnect, schema, err)
	}

	if !exists {
		return &SchemaError{Schema: schema}
	}

	return nil
}
