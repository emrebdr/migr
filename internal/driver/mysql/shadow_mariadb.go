package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// MariaDB refuses RENAME TABLE under LOCK TABLES (error 1192), and an ALTER TABLE ... RENAME under the
// lock fails every writer queued behind it with 1146, so its cutover is three connections beside the
// session, each measured on 11.4.13 (M13-T17, ADR 0095):
//
//   - a holder takes LOCK TABLES on the table alone, so writers queue behind it;
//   - the session catches the shadow up, carries AUTO_INCREMENT across and commits, which leaves it
//     holding no metadata lock on the shadow;
//   - a renamer sends RENAME TABLE of both, which waits on the holder's lock;
//   - a prober reads, with lock_wait_timeout = 0, that the renamer holds every name it must take
//     before the table's, so its exclusive request on the table is what is pending;
//   - the holder unlocks, and the pending exclusive request is granted before the queued writers,
//     which then land in the new table.
//
// The prober is what makes the order safe. A RENAME takes its names' metadata locks in sorted order,
// and the shadow's and the retired name sort before most tables', so a renamer still waiting behind
// some other session's lock on the shadow has nothing pending on the table -- measured, writers then
// went to the original before the rename moved it aside. SHOW CREATE TABLE takes the high-priority
// shared lock a pending exclusive request does not block and a granted one does, so it tells a name
// the renamer holds from one it is waiting for. A renamer not seen queued within safety.lock_timeout
// is killed and the cutover retried as a lock wait, with nothing swapped.

// processStateWaiting is the state PROCESSLIST shows for a statement waiting on a metadata lock.
const processStateWaiting = "Waiting for table metadata lock"

// queuePoll is how often the cutover looks for the rename waiting on the holder's lock.
const queuePoll = 5 * time.Millisecond

// cutoverConnections are the holder, the renamer and the prober.
const cutoverConnections = 3

// cutoverMariaDB swaps the shadow in through a holder, a renamer and a prober beside the session.
func (c *mysqlConn) cutoverMariaDB(ctx context.Context, s driver.Shadow, catchUp driver.CatchUp) (err error) {
	if c.connector == nil {
		return errors.New("this session was opened without a connector, so no MariaDB cutover can lock beside it")
	}

	side := sql.OpenDB(c.connector)
	side.SetMaxOpenConns(cutoverConnections)

	defer func() { err = errors.Join(err, closeDB(side)) }()

	holder, err := c.sideConn(ctx, side, c.cutoverWait())
	if err != nil {
		return err
	}

	defer func() { err = errors.Join(err, closeConn(holder)) }()

	if _, err := holder.ExecContext(ctx, "LOCK TABLES "+quote(s.Table)+" WRITE"); err != nil {
		return fmt.Errorf("locking %s for the cutover: %w", s.Table, explainExec(err))
	}

	defer func() { err = errors.Join(err, unlockHolder(ctx, holder)) }()

	if err := c.catchUpBounded(ctx, catchUp); err != nil {
		return err
	}

	if err := c.carryAutoIncrement(ctx, s); err != nil {
		return err
	}

	if _, err := c.runner().ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("committing the catch-up before %s is renamed: %w", s.Table, err)
	}

	return c.renameBehind(ctx, side, holder, s)
}

// sideConn pins one connection of the cutover's own pool, its metadata-lock wait bounded in whole seconds.
func (c *mysqlConn) sideConn(ctx context.Context, side *sql.DB, wait time.Duration) (*sql.Conn, error) {
	conn, err := side.Conn(ctx)
	if err != nil {
		return nil, wrapConnect("pinning a connection for the cutover", err)
	}

	if _, err := conn.ExecContext(ctx, "SET SESSION lock_wait_timeout = "+waitSeconds(wait)); err != nil {
		return nil, errors.Join(fmt.Errorf("bounding the cutover's lock waits: %w", err), closeConn(conn))
	}

	return conn, nil
}

// unlockHolder releases the holder's lock, on a context that has not been canceled.
func unlockHolder(ctx context.Context, holder *sql.Conn) error {
	if _, err := holder.ExecContext(context.WithoutCancel(ctx), "UNLOCK TABLES"); err != nil {
		return fmt.Errorf("releasing the cutover's lock: %w", err)
	}

	return nil
}

// renameBehind queues the rename behind the holder's lock, waits until it is next for the table, and lets it through.
func (c *mysqlConn) renameBehind(ctx context.Context, side *sql.DB, holder *sql.Conn, s driver.Shadow) error {
	renamer, err := c.sideConn(ctx, side, c.cutoverWait()+time.Second)
	if err != nil {
		return err
	}

	defer func() { _ = closeConn(renamer) }()

	var id int64
	if err := renamer.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		return fmt.Errorf("reading the cutover's renaming connection: %w", err)
	}

	done := make(chan error, 1)

	go func() {
		_, err := renamer.ExecContext(context.WithoutCancel(ctx), "RENAME TABLE "+quote(s.Table)+" TO "+
			quote(s.Retired)+", "+quote(s.Shadow)+" TO "+quote(s.Table))
		done <- err
	}()

	if err := c.awaitQueued(ctx, side, id, s, done); err != nil {
		return errors.Join(err, c.stopRename(ctx, id, done))
	}

	if err := unlockHolder(ctx, holder); err != nil {
		return errors.Join(err, c.stopRename(ctx, id, done))
	}

	if err := <-done; err != nil {
		return fmt.Errorf("swapping %s in for %s: %w", s.Shadow, s.Table, explainExec(err))
	}

	return nil
}

// stopRename kills a rename that was not let through, and waits for it to come back.
func (c *mysqlConn) stopRename(ctx context.Context, id int64, done chan error) error {
	bound, cancel := context.WithTimeout(context.WithoutCancel(ctx), killWait)
	defer cancel()

	_, err := c.killer.db.ExecContext(bound, "KILL QUERY "+strconv.FormatInt(id, 10))

	renamed := <-done
	if renamed == nil {
		return errors.Join(err, errors.New("the rename completed although it was stopped"))
	}

	if err != nil {
		return fmt.Errorf("stopping the cutover's rename: %w", err)
	}

	return nil
}

// awaitQueued waits, within safety.lock_timeout, until the rename holds every name before the table's and waits on it.
func (c *mysqlConn) awaitQueued(ctx context.Context, side *sql.DB, id int64, s driver.Shadow, done chan error) error {
	bounded, cancel := context.WithTimeout(ctx, c.cutoverWait())
	defer cancel()

	prober, err := c.sideConn(bounded, side, 0)
	if err != nil {
		return err
	}

	defer func() { _ = closeConn(prober) }()

	before := c.namesBefore(s)

	for {
		queued, err := renameQueued(bounded, prober, id, before)
		if err != nil {
			return c.queueFailure(ctx, bounded, s, err)
		}

		if queued {
			return nil
		}

		select {
		case renamed := <-done:
			done <- renamed

			return fmt.Errorf("the cutover's rename of %s returned before it could wait on the lock: %w",
				s.Table, explainExec(renamed))
		case <-bounded.Done():
			return c.queueFailure(ctx, bounded, s, bounded.Err())
		case <-time.After(queuePoll):
		}
	}
}

// queueFailure explains a rename not seen queued, as a lock wait where the bound ran out first.
func (c *mysqlConn) queueFailure(ctx, bounded context.Context, s driver.Shadow, err error) error {
	if ctx.Err() == nil && errors.Is(bounded.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: the cutover's rename of %s was not seen waiting next for the table within "+
			"safety.lock_timeout (%s) -- another session holds a lock on %s or %s -- so the lock was "+
			"released and nothing swapped: %w", driver.ErrStatementLockTimeout, s.Table, c.cutoverWait(),
			s.Shadow, s.Retired, err)
	}

	return fmt.Errorf("reading whether the cutover's rename of %s is waiting: %w", s.Table, err)
}

// namesBefore lists the rename's other names whose metadata locks it takes before the table's, in the server's order.
func (c *mysqlConn) namesBefore(s driver.Shadow) []string {
	table := storedName(s.Table, c.folding)

	var before []string

	for _, name := range []string{s.Retired, s.Shadow} {
		if stored := storedName(name, c.folding); stored < table {
			before = append(before, name)
		}
	}

	return before
}

// renameQueued reports a rename waiting on a metadata lock while it holds every name it takes before the table's.
func renameQueued(ctx context.Context, prober *sql.Conn, id int64, before []string) (bool, error) {
	var state sql.NullString

	err := prober.QueryRowContext(ctx, "SELECT STATE FROM information_schema.PROCESSLIST WHERE ID = ?", id).
		Scan(&state)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !strings.EqualFold(state.String, processStateWaiting)) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	for _, name := range before {
		held, err := lockHeld(ctx, prober, name)
		if err != nil || !held {
			return false, err
		}
	}

	return true, nil
}

// lockHeld reports whether some session holds an exclusive metadata lock on a name, which a high-priority read cannot pass.
func lockHeld(ctx context.Context, prober *sql.Conn, name string) (bool, error) {
	_, err := prober.ExecContext(ctx, "SHOW CREATE TABLE "+quote(name))
	if err == nil {
		return false, nil
	}

	if hasNumber(err, errLockWaitTimeout) {
		return true, nil
	}

	if hasNumber(err, errNoSuchTable) {
		return false, nil
	}

	return false, fmt.Errorf("reading whether %s is locked: %w", name, err)
}
