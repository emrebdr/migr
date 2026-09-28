package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// pollInterval is how often the wait re-offers, flock having no form that takes a deadline.
const pollInterval = 50 * time.Millisecond

// stampQuery asks the engine for the time, it being the only clock forbidigo leaves this driver.
const stampQuery = `SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now')`

// heldQuery asks the engine how long ago a stamp it wrote was written, in whole milliseconds.
const heldQuery = `SELECT CAST((julianday('now') - julianday(?)) * 86400000 AS INTEGER)`

// errLockFileBusy is why a wait ended, and is the cause driver.LockBusyError carries underneath.
var errLockFileBusy = errors.New("another process holds the lock file beside this database")

// errUnenforced is why a granted lock was given straight back, which is evidence flock is advisory.
var errUnenforced = errors.New("the lock file was granted while its record still names a running " +
	"process on this host, which cannot happen where flock is enforced -- so it is not being " +
	"enforced here, which a network filesystem is the usual reason for")

// Lock takes the migration lock, which for SQLite is an exclusive hold on a file beside the database.
func (c *sqliteConn) Lock(ctx context.Context) (driver.Unlock, error) {
	if c.locked {
		return nil, &LockError{Path: c.lockedPath(), Err: ErrLockHeld}
	}

	if c.path == "" {
		c.locked = true

		return c.releaser(), nil
	}

	file, err := openLockFile(lockPath(c.path))
	if err != nil {
		return nil, err
	}

	if err := c.acquire(ctx, file); err != nil {
		return nil, errors.Join(err, closeLockFile(file))
	}

	c.lock, c.locked = file, true

	return c.releaser(), nil
}

// lockedPath names the lock file this session would use, and nothing for a database with no file.
func (c *sqliteConn) lockedPath() string {
	if c.path == "" {
		return ""
	}

	return lockPath(c.path)
}

// acquire waits for the lock file under one total budget, since flock has no form that takes one.
func (c *sqliteConn) acquire(ctx context.Context, file *os.File) error {
	budget := time.NewTimer(c.lockWait)
	defer budget.Stop()

	poll := time.NewTicker(pollInterval)
	defer poll.Stop()

	for {
		taken, err := c.attempt(ctx, file)
		if err != nil || taken {
			return err
		}

		select {
		case <-ctx.Done():
			return &LockError{Path: file.Name(),
				Err: fmt.Errorf("%w: waiting for it: %w", ErrLockFile, ctx.Err())}
		case <-budget.C:
			return &LockError{Path: file.Name(), Err: c.busy(ctx, file)}
		case <-poll.C:
		}
	}
}

// attempt takes the lock once, giving it straight back where the record says it is not free.
func (c *sqliteConn) attempt(ctx context.Context, file *os.File) (bool, error) {
	granted, err := takeExclusive(file)
	if err != nil {
		return false, &LockError{Path: file.Name(),
			Err: fmt.Errorf("%w: taking an exclusive lock on it: %w", ErrLockFile, err)}
	}

	if !granted {
		return false, nil
	}

	free, err := c.checkFree(file)
	if err != nil || !free {
		return false, errors.Join(err, releaseExclusive(file))
	}

	return true, c.claim(ctx, file)
}

// checkFree reports whether a granted lock is really free, which its own record can contradict.
func (c *sqliteConn) checkFree(file *os.File) (bool, error) {
	held, _, err := readRecord(file)
	if err != nil {
		return false, err
	}

	return !held.contested(), nil
}

// claim records who holds the lock, and gives it back where the record could not be written.
func (c *sqliteConn) claim(ctx context.Context, file *os.File) error {
	stamp, err := c.stamp(ctx)
	if err != nil {
		return errors.Join(err, releaseExclusive(file))
	}

	written := writeRecord(file, record{PID: os.Getpid(), Host: hostname(),
		Application: applicationName(), Acquired: stamp})
	if written != nil {
		return errors.Join(written, releaseExclusive(file))
	}

	return nil
}

// stamp asks the engine when it is, since the record has to outlive the process that wrote it.
func (c *sqliteConn) stamp(ctx context.Context) (string, error) {
	var written string

	if err := c.runner().QueryRowContext(ctx, stampQuery).Scan(&written); err != nil {
		return "", fmt.Errorf("%w: reading the time the engine keeps: %w", ErrLockFile, err)
	}

	return written, nil
}

// busy reports a wait that gave up, naming whoever the lock file says holds it now.
func (c *sqliteConn) busy(ctx context.Context, file *os.File) error {
	report := &driver.LockBusyError{Driver: Name, Wait: c.lockWait, Err: errLockFileBusy}

	held, why, err := readRecord(file)

	switch {
	case err != nil:
		report.Unidentified = "reading the lock file failed: " + err.Error()
	case why != "":
		report.Unidentified = why
	case held.contested():
		report.Holder, report.Err = c.holder(ctx, held), errUnenforced
	default:
		report.Holder = c.holder(ctx, held)
	}

	return report
}

// holder renders what the lock file recorded into the shape every driver reports a holder in.
func (c *sqliteConn) holder(ctx context.Context, held record) *driver.LockHolder {
	return &driver.LockHolder{
		Process:     held.describeProcess(),
		Application: held.Application,
		Connected:   c.heldFor(ctx, held.Acquired),
	}
}

// heldFor reports how long the lock has been held, and zero where the stamp cannot be read back.
func (c *sqliteConn) heldFor(ctx context.Context, acquired string) time.Duration {
	var elapsed *int64

	row := c.runner().QueryRowContext(context.WithoutCancel(ctx), heldQuery, acquired)

	if err := row.Scan(&elapsed); err != nil || elapsed == nil || *elapsed < 0 {
		return 0
	}

	return time.Duration(*elapsed) * time.Millisecond
}

// releaser returns a release that gives the lock back once, and reports success thereafter.
func (c *sqliteConn) releaser() driver.Unlock {
	return func(context.Context) error {
		if !c.locked {
			return nil
		}

		err := c.release()
		c.lock, c.locked = nil, false

		return err
	}
}

// release blanks the record before giving the lock back, so no record outlives the hold it names.
func (c *sqliteConn) release() error {
	if c.lock == nil {
		return nil
	}

	blanked, released := blankRecord(c.lock), releaseExclusive(c.lock)

	return errors.Join(blanked, c.describeRelease(released), closeLockFile(c.lock))
}

// describeRelease names a flock this driver could not give back, which closing then gives back for it.
func (c *sqliteConn) describeRelease(err error) error {
	if err == nil {
		return nil
	}

	return &LockError{Path: c.lock.Name(), Err: fmt.Errorf("%w: %w", ErrLockRelease, err)}
}
