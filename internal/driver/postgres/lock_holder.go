package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/driver"
)

// holderQuery names the session holding migr's advisory lock, and how long that session has existed.
//
// objsubid is 2 for the two-integer key form and 1 for the bigint form, and dropping it is not a
// harmless simplification: pg_advisory_lock((migr's class id::bigint << 32) | migr's obj id)
// produces a row with an identical classid and objid, differing in nothing else. Measured on both
// servers. The existing waitingQuery test helper omits it, which is fine for counting and wrong
// for a report that tells an operator which process to terminate.
//
// granted separates the holder from the sessions queued behind it. The join is a LEFT JOIN and
// backend_start is read as a nullable, because pg_stat_activity withholds it from a session whose
// role this one is not a member of -- measured, along with the fact that application_name is
// *not* withheld. Coalescing it to zero would render "open for 0s" for a holder of an hour, so
// the degraded answer is "who, but not how long" rather than a confident wrong number.
//
// query is deliberately not selected: a DDL statement carries literal column defaults, which is
// user data, and this driver already refuses to render a DSN for the same class of reason.
//
// It runs on context.WithoutCancel, which is what keeps it from destroying the session it is
// diagnosing. A wait is abandoned exactly when its context is done, and pgx closes the connection
// on a query canceled mid-flight -- so reading the holder on the caller's context would kill the
// pinned session on the one path that exists to explain a failure. resetLockTimeout does the same.
const holderQuery = `
SELECT l.pid,
       coalesce(a.application_name, ''),
       (extract(epoch FROM clock_timestamp() - a.backend_start) * 1000)::bigint
  FROM pg_locks l
  LEFT JOIN pg_stat_activity a ON a.pid = l.pid
 WHERE l.locktype = 'advisory'
   AND l.classid = $1
   AND l.objid = $2
   AND l.objsubid = 2
   AND l.granted
 ORDER BY l.pid
 LIMIT 1`

// measuredAge names what this driver's holder duration is, PostgreSQL recording no grant time.
//
// The session's age bounds the hold from above: the backend cannot have held the lock for longer
// than it has existed. It was chosen for that reason, and it is labeled because
// three engines measure three different durations, and only the one that measured can say which.
const measuredAge = "session age"

// noHolder is why a report names nobody when the lock was released between the failure and the look.
const noHolder = "nobody held it by the time this session looked, so it was released in between"

// busy reports a wait that gave up, naming whoever holds the lock now.
func (c *pgConn) busy(ctx context.Context, cause error) *driver.LockBusyError {
	report := &driver.LockBusyError{Driver: Name, Wait: c.lockWait, Err: cause}

	holder, err := c.holder(ctx)
	switch {
	case err != nil:
		report.Unidentified = "reading pg_locks failed: " + err.Error()
	case holder == nil:
		report.Unidentified = noHolder
	default:
		report.Holder = holder
	}

	return report
}

// holder reads who holds this session's lock, on a context the failed wait cannot have canceled.
func (c *pgConn) holder(ctx context.Context) (*driver.LockHolder, error) {
	var pid int32
	var application string
	var connectedMS *int64

	row := c.conn.QueryRow(context.WithoutCancel(ctx), holderQuery, c.key.class, c.key.obj)

	switch err := row.Scan(&pid, &application, &connectedMS); {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, err
	}

	age, read := sessionAge(connectedMS)

	return &driver.LockHolder{
		Process:     fmt.Sprintf("pid %d", pid),
		Application: application,
		Elapsed:     age,
		Measured:    measuredWhere(read),
	}, nil
}

// measuredWhere labels the duration only where one was read, a label being what says it was.
func measuredWhere(read bool) string {
	if !read {
		return ""
	}

	return measuredAge
}

// sessionAge turns the milliseconds the server measured into a duration, reporting one it withheld.
func sessionAge(connectedMS *int64) (time.Duration, bool) {
	if connectedMS == nil || *connectedMS < 0 {
		return 0, false
	}

	return time.Duration(*connectedMS) * time.Millisecond, true
}
