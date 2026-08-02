package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/driver"
)

// lockClassID is migr's half of the two-key advisory space, fixed so every migr lock is
// recognizable in pg_locks and so no namespace can collide with another tool's classid.
const lockClassID int32 = 0x6D677221

// namespaceSeparator keeps the two key inputs apart, so "a"+"bc" and "ab"+"c" differ.
const namespaceSeparator = "\x00"

// acquireQuery takes the migration lock, waiting on the server's own queue.
const acquireQuery = `SELECT pg_advisory_lock($1, $2)`

// releaseQuery gives the lock back, reporting false when this session did not hold it.
const releaseQuery = `SELECT pg_advisory_unlock($1, $2)`

// lockTimeoutQuery bounds an acquire server-side, so a wait that gives up leaves the session alive.
//
// set_config rather than SET, because SET takes no bound parameters -- the same reason
// applySearchPath uses it, and the reason migr.yaml's lock.timeout is bound here rather than
// concatenated. The value it binds is lockTimeoutSetting's, never Duration.String's.
const lockTimeoutQuery = `SELECT set_config('lock_timeout', $1, false)`

// lockTimeoutReset returns the setting to the per-statement bound pinLockTimeout pinned at Open.
//
// RESET restores reset_val, which only a source at or below "client" sets -- so this restores the
// startup-packet value and not the one an earlier SET left behind. See lockTimeoutParam: before
// that value was pinned, this line silently returned lock_timeout to 0.
const lockTimeoutReset = `RESET lock_timeout`

// lockNotAvailable is the SQLSTATE PostgreSQL raises when lock_timeout expires.
const lockNotAvailable = "55P03"

// lockKey is one migration lock, addressed the way PostgreSQL addresses advisory locks.
type lockKey struct {
	class int32
	obj   int32
}

// String renders the key as pg_locks reports it, so an operator can match the two.
func (k lockKey) String() string {
	return fmt.Sprintf("(%d, %d)", k.class, k.obj)
}

// objIDMask keeps the derived half non-negative, since pg_locks reports int4 as a signed number.
const objIDMask = 0x7FFFFFFF

// namespaceKey derives the advisory key one migr installation locks on.
func namespaceKey(schema, table string) lockKey {
	digest := sha256.Sum256([]byte(schema + namespaceSeparator + table))

	return lockKey{
		class: lockClassID,
		obj:   int32(binary.BigEndian.Uint32(digest[:4]) & objIDMask),
	}
}

// Lock takes the migration lock on this session, which is the session that must release it.
func (c *pgConn) Lock(ctx context.Context) (driver.Unlock, error) {
	if c.locked {
		return nil, &LockError{Key: c.key, Err: ErrLockHeld}
	}

	if err := c.acquire(ctx); err != nil {
		return nil, err
	}

	c.locked = true

	return c.releaser(), nil
}

// acquire waits for the lock under a server-side bound, so giving up does not end the session.
func (c *pgConn) acquire(ctx context.Context) error {
	if _, err := c.conn.Exec(ctx, lockTimeoutQuery, lockTimeoutSetting(c.lockWait)); err != nil {
		return fmt.Errorf("%w: bounding the wait for %s: %w", ErrLock, c.key, err)
	}

	defer c.resetLockTimeout(ctx)

	if _, err := c.conn.Exec(ctx, acquireQuery, c.key.class, c.key.obj); err != nil {
		return &LockError{Key: c.key, Err: c.classifyLockFailure(ctx, err)}
	}

	return nil
}

// resetLockTimeout puts the session's lock_timeout back, so the bound covers the acquire alone.
func (c *pgConn) resetLockTimeout(ctx context.Context) {
	_, _ = c.conn.Exec(context.WithoutCancel(ctx), lockTimeoutReset)
}

// classifyLockFailure separates giving up on the wait from every other way an acquire fails.
func (c *pgConn) classifyLockFailure(ctx context.Context, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return c.busy(ctx, err)
	}

	return err
}

// releaser returns a release that gives the lock back once, and reports success thereafter.
func (c *pgConn) releaser() driver.Unlock {
	return func(ctx context.Context) error {
		if !c.locked {
			return nil
		}

		if err := c.release(ctx); err != nil {
			return err
		}

		c.locked = false

		return nil
	}
}

// release gives the lock back, refusing to report success for a lock this session did not hold.
func (c *pgConn) release(ctx context.Context) error {
	if err := c.checkReleasable(); err != nil {
		return err
	}

	var released bool

	err := c.conn.QueryRow(ctx, releaseQuery, c.key.class, c.key.obj).Scan(&released)
	if err != nil {
		return &LockError{Key: c.key, Err: fmt.Errorf("%w: %w", ErrLockRelease, err)}
	}

	if !released {
		return &LockError{Key: c.key, Err: ErrLockNotHeld}
	}

	return nil
}

// checkReleasable reports an aborted transaction as itself rather than as a failed release.
func (c *pgConn) checkReleasable() error {
	if c.conn.PgConn().TxStatus() != aborted {
		return nil
	}

	return &LockError{Key: c.key, Err: fmt.Errorf("%w: this session has an aborted transaction "+
		"open, and PostgreSQL refuses every command until one ends -- roll back first, which is "+
		"what deferring the release after the transaction already does", ErrLockRelease)}
}
