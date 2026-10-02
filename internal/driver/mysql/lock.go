package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// lockPrefix marks every name migr locks on, so one is recognizable in IS_USED_LOCK.
//
// The 1 is the derivation's version and not migr's: a later release that hashed something else would
// have to lock on a different name, and two migr builds locking on two names would not exclude each
// other. Changing it is therefore a breaking change to the lock itself.
const lockPrefix = "migr1:"

// lockDigestBytes is how much of the digest the name carries, which decides the name's length.
const lockDigestBytes = 16

// nameSeparator keeps the two inputs apart, so "a"+"bc" and "ab"+"c" derive different names.
const nameSeparator = "\x00"

// maxLockNameLength is the longest user-level lock name MySQL keeps, which MariaDB does not bound.
//
// Measured: MySQL 8.0.46 answers error 4163 for 65 characters and accepts 64, and MariaDB 11.4.13
// accepts 65 without complaint. So the bound is MySQL's alone and a derived name is well inside it --
// lockPrefix plus 32 hex characters is 38.
const maxLockNameLength = 64

// acquireQuery takes the migration lock, waiting the decimal number of seconds it is given.
const acquireQuery = `SELECT GET_LOCK(?, ?)`

// releaseQuery gives the lock back, answering 0 for another session's lock and NULL for no lock.
const releaseQuery = `SELECT RELEASE_LOCK(?)`

// lockGranted is what GET_LOCK answers when the lock is now held by this session.
const lockGranted = 1

// shortestWait is what a wait too small to render as a positive decimal becomes.
//
// GET_LOCK takes a negative timeout as "wait forever" on MySQL -- measured, it waited and then
// granted -- and MariaDB answers NULL for one. Neither is a bound, so no value this driver renders
// may be negative or zero, which is PostgreSQL's millisecondsCeil rule in this engine's units.
const shortestWait = time.Millisecond

// lockName is the name one migr installation locks on, derived rather than spelled.
type lockName string

// String renders the name, so every failure carries what an operator passes to IS_USED_LOCK.
func (n lockName) String() string {
	return string(n)
}

// namespaceLock derives the name one migr installation locks on, which is server-wide.
func namespaceLock(database, table string) lockName {
	digest := sha256.Sum256([]byte(database + nameSeparator + table))

	return lockName(lockPrefix + hex.EncodeToString(digest[:lockDigestBytes]))
}

// lockWaitSeconds renders a wait the way GET_LOCK reads it, which is a decimal number of seconds.
func lockWaitSeconds(wait time.Duration) string {
	if wait < shortestWait {
		wait = shortestWait
	}

	return strconv.FormatFloat(wait.Seconds(), 'f', -1, 64)
}

// Lock takes the migration lock on this session, which is the session that must release it.
func (c *mysqlConn) Lock(ctx context.Context) (driver.Unlock, error) {
	if c.locked {
		return nil, &LockError{Name: c.lock, Err: ErrLockHeld}
	}

	if err := c.acquire(ctx); err != nil {
		return nil, err
	}

	c.locked = true

	return c.releaser(), nil
}

// acquire waits for the lock for as long as GET_LOCK was told to, and no longer.
func (c *mysqlConn) acquire(ctx context.Context) error {
	granted, err := c.offerLock(ctx)
	if err != nil {
		return &LockError{Name: c.lock, Err: err}
	}

	if granted == nil {
		return &LockError{Name: c.lock, Err: fmt.Errorf("%w: GET_LOCK answered null, which is "+
			"neither granted nor given up on: the server reports that for a wait it refused and for "+
			"one it interrupted", ErrLock)}
	}

	if *granted != lockGranted {
		return &LockError{Name: c.lock, Err: c.busy(ctx)}
	}

	return nil
}

// offerLock asks for the lock once, under the server-side bound the wait was rendered into.
func (c *mysqlConn) offerLock(ctx context.Context) (*int64, error) {
	var granted *int64

	row := c.runner().QueryRowContext(ctx, acquireQuery, c.lock.String(),
		lockWaitSeconds(c.lockWait))

	if err := row.Scan(&granted); err != nil {
		return nil, fmt.Errorf("%w: waiting for %s: %w", ErrLock, c.lock, err)
	}

	return granted, nil
}

// releaser returns a release that gives the lock back once, and reports success thereafter.
func (c *mysqlConn) releaser() driver.Unlock {
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
func (c *mysqlConn) release(ctx context.Context) error {
	var released *int64

	err := c.runner().QueryRowContext(ctx, releaseQuery, c.lock.String()).Scan(&released)
	if err != nil {
		return c.classifyRelease(err)
	}

	if released == nil {
		return &LockError{Name: c.lock, Err: fmt.Errorf("%w: RELEASE_LOCK answered null, which this "+
			"server reports for a lock nobody holds", ErrLockRelease)}
	}

	if *released != lockGranted {
		return &LockError{Name: c.lock, Err: fmt.Errorf("%w: %w, which this server reports by "+
			"answering 0", ErrLockRelease, ErrLockNotHeld)}
	}

	return nil
}

// classifyRelease reports a release the session did not survive as the release it already was.
func (c *mysqlConn) classifyRelease(err error) error {
	if sessionGone(err) {
		return nil
	}

	return &LockError{Name: c.lock, Err: fmt.Errorf("%w: %w", ErrLockRelease, err)}
}

// sessionGone reports a connection that can no longer carry a statement, however it was lost.
func sessionGone(err error) bool {
	return errors.Is(err, sqldriver.ErrBadConn) || errors.Is(err, sql.ErrConnDone)
}
