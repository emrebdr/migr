package mysql

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// killWait bounds the kill itself, which a reachable server answers in milliseconds.
const killWait = 5 * time.Second

// ErrKill marks a statement whose context ended and that this driver could not tell the server to stop.
var ErrKill = errors.New("the server was not told to stop the statement")

// killer is the second pool a kill is sent from, since the session's own holds its one connection.
type killer struct {
	// db opens a connection only when a kill is sent, through the session's own connector.
	db *sql.DB
}

// newKiller prepares the pool a kill goes through, which connects nothing until it is used.
func newKiller(connector sqldriver.Connector) killer {
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)

	return killer{db: db}
}

// close gives the kill pool back, which a session built without one does not have.
func (k killer) close() error {
	if k.db == nil {
		return nil
	}

	return closeDB(k.db)
}

// interruptible runs one round trip the library cannot cancel, and kills its statement when ctx ends.
func (c *mysqlConn) interruptible(ctx context.Context, work func(context.Context) error) error {
	if ctx.Done() == nil {
		return work(ctx)
	}

	finished := make(chan struct{})
	killed := make(chan error, 1)

	go func() {
		select {
		case <-ctx.Done():
			killed <- c.kill(ctx)
		case <-finished:
			killed <- nil
		}
	}()

	err := work(context.WithoutCancel(ctx))
	close(finished)

	return c.stopped(ctx, err, <-killed)
}

// stopped reports a round trip whose context ended as that ending, and any kill that failed beside it.
func (c *mysqlConn) stopped(ctx context.Context, err, killErr error) error {
	if killErr != nil {
		return errors.Join(err, killErr)
	}

	if err == nil || ctx.Err() == nil {
		return err
	}

	return fmt.Errorf("%w: %w", ctx.Err(), err)
}

// kill tells the server to stop whatever this session's connection is running, from another connection.
func (c *mysqlConn) kill(ctx context.Context) error {
	if c.killer.db == nil {
		return nil
	}

	bound, cancel := context.WithTimeout(context.WithoutCancel(ctx), killWait)
	defer cancel()

	if _, err := c.killer.db.ExecContext(bound, "KILL QUERY "+strconv.FormatInt(c.id, 10)); err != nil {
		return fmt.Errorf("%w: KILL QUERY %d: %w", ErrKill, c.id, err)
	}

	return nil
}

// readConnectionID asks the pinned connection which thread it is, which is what a kill names.
func readConnectionID(ctx context.Context, c *mysqlConn) (int64, error) {
	var id int64
	if err := c.conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		return 0, wrapConnect("reading the connection id", err)
	}

	return id, nil
}
