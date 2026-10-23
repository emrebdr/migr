// Command app stands in for the service whose schema is changing: one version of it per deploy.
//
// It inserts a user and reads it back, in a loop, until it is stopped, with the SQL the version it
// was started as uses. Every failed query is counted, and the process exits 1 if there was one, so
// a walkthrough that changed the schema under a version that could not cope with it fails.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// queries is one app version's SQL: how it writes a user and how it reads one back.
type queries struct {
	insert string
	read   string
}

// versions is the SQL each deploy's app runs, following the expand and contract deploy table.
var versions = map[int]queries{
	0: {
		insert: `INSERT INTO users (id, name, email) VALUES ($1, $2, $3)`,
		read:   `SELECT email FROM users WHERE id = $1`,
	},
	1: {
		insert: `INSERT INTO users (id, name, email, email_address) VALUES ($1, $2, $3, $3)`,
		read:   `SELECT email FROM users WHERE id = $1`,
	},
	2: {
		insert: `INSERT INTO users (id, name, email, email_address) VALUES ($1, $2, $3, $3)`,
		read:   `SELECT email FROM users WHERE id = $1`,
	},
	3: {
		insert: `INSERT INTO users (id, name, email, email_address) VALUES ($1, $2, $3, $3)`,
		read:   `SELECT email_address FROM users WHERE id = $1`,
	},
	4: {
		insert: `INSERT INTO users (id, name, email_address) VALUES ($1, $2, $3)`,
		read:   `SELECT email_address FROM users WHERE id = $1`,
	},
}

// tally is what one run of the app did, which it prints when it is stopped.
type tally struct {
	queries atomic.Int64
	failed  atomic.Int64
}

func main() {
	version := flag.Int("version", 0, "the app version to run, 0 to 4")
	pause := flag.Duration("pause", 5*time.Millisecond, "the wait between two users")
	flag.Parse()

	if err := run(*version, *pause); err != nil {
		fmt.Fprintf(os.Stderr, "app: %v\n", err)
		os.Exit(1)
	}
}

// run serves as one version until SIGINT or SIGTERM, reporting whether any query failed.
func run(version int, pause time.Duration) error {
	sql, ok := versions[version]
	if !ok {
		return fmt.Errorf("there is no version %d", version)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	var t tally

	serve(ctx, conn, sql, int64(version+1)*1_000_000_000, pause, &t)

	fmt.Printf("app v%d: %d queries, %d failed\n", version, t.queries.Load(), t.failed.Load())

	if t.failed.Load() > 0 {
		return errors.New("queries failed while this version was serving")
	}

	return nil
}

// serve writes and reads one user after another, ids counting up from base, until ctx ends.
func serve(ctx context.Context, conn *pgx.Conn, sql queries, base int64, pause time.Duration, t *tally) {
	for id := base; ctx.Err() == nil; id++ {
		once(ctx, conn, sql, id, t)

		select {
		case <-ctx.Done():
		case <-time.After(pause):
		}
	}
}

// once writes one user and reads its address back, counting each query and each failure.
func once(ctx context.Context, conn *pgx.Conn, sql queries, id int64, t *tally) {
	email := fmt.Sprintf("user%d@example.com", id)

	if !count(ctx, t, exec(ctx, conn, sql.insert, id, fmt.Sprintf("user %d", id), email)) {
		return
	}

	var got string

	if count(ctx, t, conn.QueryRow(ctx, sql.read, id).Scan(&got)) && got != email {
		t.failed.Add(1)
		fmt.Fprintf(os.Stderr, "app: user %d reads back %q, want %q\n", id, got, email)
	}
}

// exec runs one statement, dropping the command tag nobody here reads.
func exec(ctx context.Context, conn *pgx.Conn, statement string, args ...any) error {
	_, err := conn.Exec(ctx, statement, args...)

	return err
}

// count records one query and reports whether it succeeded, a stop in its middle not being a failure.
func count(ctx context.Context, t *tally, err error) bool {
	t.queries.Add(1)

	if err == nil {
		return true
	}

	if ctx.Err() == nil {
		t.failed.Add(1)
		fmt.Fprintf(os.Stderr, "app: %v\n", err)
	}

	return false
}
