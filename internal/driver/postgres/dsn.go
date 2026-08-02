package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/version"
)

// searchPathQuery sets the session's schema as one identifier, through a bound parameter.
//
// quote_ident is not decoration. A bound parameter stops SQL injection, but search_path is a
// list-valued setting, so PostgreSQL parses the value it is given as comma-separated entries --
// a schema of "public, pg_catalog" would silently become two of them. quote_ident makes it one
// identifier, so an injected value names a schema nobody has rather than changing what the
// session resolves.
const searchPathQuery = `SELECT set_config('search_path', quote_ident($1), false)`

// stringGrammarQuery makes the session's string literal grammar known rather than assumed.
//
// DDL takes no parameters, so a column default's literal value is written into the statement
// rather than bound, and doubling ' is the whole defense. That defense is complete only while
// standard_conforming_strings is on: with it off, a backslash starts an escape sequence and the
// doubling can be escaped past. It has been the default since PostgreSQL 9.1 and is deprecated,
// but Compile is pure by contract and cannot read a session setting -- so the session is set to
// the grammar the compiler was written against instead of trusting that it already is.
const stringGrammarQuery = `SELECT set_config('standard_conforming_strings', 'on', false)`

// lockTimeoutParam is the setting invariant #12 requires every DDL statement to run under.
//
// It is pinned in the startup packet rather than set with set_config, and the difference is not
// cosmetic. A startup-packet value reaches PostgreSQL as source "client", which sets the setting's
// reset_val; an interactive SET does not, because its source outranks the one RESET restores. So a
// session default established the way search_path and standard_conforming_strings are established
// would be wiped by the first RESET -- and acquire ends with one. lock_timeout would be 0 from the
// first migration lock onward, and every DDL statement after it would wait for a lock forever:
// invariant #12 broken on the happy path, silently. Verified on PostgreSQL 16 and 17, where
// pg_settings then reports source "client" and RESET restores the pinned value rather than 0.
const lockTimeoutParam = "lock_timeout"

// applicationNameParam is how a migr session identifies itself to anyone reading pg_stat_activity.
//
// It is what a holder report names beside the pid, so it is pinned after parsing and overrides one
// the DSN carried -- the same precedence pinLockTimeout takes, for a related reason. A DSN that
// renamed the session would make every "who holds the lock" answer anonymous, and the environments
// most likely to set it for their own monitoring are the ones most likely to need the answer.
const applicationNameParam = "application_name"

// applicationNameLimit is how many bytes PostgreSQL keeps, being NAMEDATALEN-1.
//
// It truncates silently past this, and escapes a non-printable byte to a four-character \xNN
// first -- so sanitizing has to happen before clamping or the escape expansion pushes a name that
// fits over the limit. Both measured on PostgreSQL 16 and 17.
const applicationNameLimit = 63

// keepaliveParams ask the server to notice a migr client that vanished without closing its socket.
//
// This is the half of invariant #11 that PostgreSQL does not answer for free. A backend whose
// client was killed with SIGKILL, OOM-killed, or partitioned away never sees a FIN: it stays
// blocked in ReadCommand holding the advisory lock until TCP keepalives expire, which is two hours
// by default on Linux. A crashed migr up would wedge every later run for that long. These three
// bring it to about ninety seconds. They are USERSET, they report 0 and are ignored without error
// on a Unix socket or an unsupported platform, and they read back as set over TCP -- measured on
// both servers, both transports.
var keepaliveParams = map[string]string{
	"tcp_keepalives_idle":     "60",
	"tcp_keepalives_interval": "10",
	"tcp_keepalives_count":    "3",
}

// parseDSN turns a connection string into a pgx config, withholding the string when it will not.
func parseDSN(dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, &DSNError{Err: err}
	}

	return cfg, nil
}

// pinLockTimeout bounds every statement this session runs, after parsing so a DSN cannot lower it.
func pinLockTimeout(cfg *pgx.ConnConfig, wait time.Duration) {
	cfg.RuntimeParams[lockTimeoutParam] = lockTimeoutSetting(wait)
}

// pinApplicationName makes this session recognizable in pg_stat_activity, overriding the DSN's.
func pinApplicationName(cfg *pgx.ConnConfig) {
	cfg.RuntimeParams[applicationNameParam] = applicationName()
}

// applicationName is what a migr session calls itself, cut to what PostgreSQL will keep.
func applicationName() string {
	return clamp(printableOnly(version.Program+" "+version.Version), applicationNameLimit)
}

// printableOnly replaces the bytes PostgreSQL would escape, since escaping one costs four characters.
func printableOnly(name string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r > '~' {
			return '?'
		}

		return r
	}, name)
}

// clamp cuts a name to the bytes an engine keeps, so truncation is this driver's rather than silent.
func clamp(name string, limit int) string {
	if len(name) <= limit {
		return name
	}

	return name[:limit]
}

// pinKeepalives asks the server to reap a backend whose client vanished, per keepaliveParams.
func pinKeepalives(cfg *pgx.ConnConfig) {
	for param, value := range keepaliveParams {
		cfg.RuntimeParams[param] = value
	}
}

// applyStringGrammar pins the session to the string literal grammar this driver quotes for.
func applyStringGrammar(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, stringGrammarQuery); err != nil {
		return fmt.Errorf("%w: pinning the string literal grammar: %w", ErrConnect, err)
	}

	return nil
}

// applySearchPath points the session at the configured schema, if migr.yaml named one.
func applySearchPath(ctx context.Context, conn *pgx.Conn, schema string) error {
	if strings.TrimSpace(schema) == "" {
		return nil
	}

	if err := checkSchemaExists(ctx, conn, schema); err != nil {
		return err
	}

	if _, err := conn.Exec(ctx, searchPathQuery, schema); err != nil {
		return fmt.Errorf("%w: setting the search path to %q: %w", ErrConnect, schema, err)
	}

	return nil
}
