package mysql

import (
	"context"
	"fmt"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// holderQuery names the session holding one user-level lock, which needs no privilege to ask.
//
// IS_USED_LOCK is a function over the lock itself rather than a view of the server, so it answers an
// ordinary user -- measured on both servers as a connection id returned to a user holding nothing but
// SELECT on one database. That is what makes a report possible at all where the next query is not.
const holderQuery = `SELECT IS_USED_LOCK(?)`

// holderSessionQuery describes the session an id names, and is the half a privilege can withhold.
//
// information_schema.PROCESSLIST shows another session's row only to a user with PROCESS, and
// filters it out silently rather than refusing -- measured, zero rows on both servers for a user
// without it. So a missing row is the ordinary case for a production grant and not an error.
//
// TIME is seconds in the thread's *current state* and not the session's age: MySQL records no
// connection start time anywhere. Measured against a holder three seconds into a statement, which
// read 3. That is why driver.LockHolder carries what its duration measures.
//
// INFO is deliberately not selected: a statement carries literal column values, which is user data,
// the same class of reason this driver refuses to render a connection string.
const holderSessionQuery = `
SELECT USER, HOST, TIME
  FROM information_schema.PROCESSLIST
 WHERE ID = ?`

// measuredState names what this driver's holder duration is, which is neither of the other two.
const measuredState = "time in its current state"

// noHolder is why a report names nobody when the lock was released between the failure and the look.
const noHolder = "nobody held it by the time this session looked, so it was released in between"

// busy reports a wait that gave up, naming whoever holds the lock now.
func (c *mysqlConn) busy(ctx context.Context) *driver.LockBusyError {
	report := &driver.LockBusyError{Driver: Name, Wait: c.lockWait,
		Err: fmt.Errorf("%w: GET_LOCK answered 0, which is the wait giving up", ErrLock)}

	id, err := c.holdingConnection(ctx)
	switch {
	case err != nil:
		report.Unidentified = "reading IS_USED_LOCK failed: " + err.Error()
	case id == nil:
		report.Unidentified = noHolder
	default:
		report.Holder = c.holder(ctx, *id)
	}

	return report
}

// holdingConnection reads which connection holds the lock, on a context the failed wait cannot cancel.
func (c *mysqlConn) holdingConnection(ctx context.Context) (*int64, error) {
	var id *int64

	row := c.runner().QueryRowContext(context.WithoutCancel(ctx), holderQuery, c.lock.String())
	if err := row.Scan(&id); err != nil {
		return nil, err
	}

	return id, nil
}

// holder describes the connection holding the lock, naming it even where nothing else can be read.
func (c *mysqlConn) holder(ctx context.Context, id int64) *driver.LockHolder {
	held := &driver.LockHolder{Process: fmt.Sprintf("connection %d", id)}

	account, state, err := c.holdingSession(ctx, id)
	if err != nil {
		return held
	}

	held.Application, held.Elapsed, held.Measured = account, state, measuredState

	return held
}

// holdingSession reads what the server will say about one connection, and nothing where it will not.
func (c *mysqlConn) holdingSession(ctx context.Context, id int64) (string, time.Duration, error) {
	var user, host string
	var seconds *int64

	row := c.runner().QueryRowContext(context.WithoutCancel(ctx), holderSessionQuery, id)

	if err := row.Scan(&user, &host, &seconds); err != nil {
		return "", 0, err
	}

	return account(user, host), stateAge(seconds), nil
}

// account renders who the holder connected as, which is the nearest this engine has to a name.
func account(user, host string) string {
	if user == "" && host == "" {
		return ""
	}

	return user + "@" + host
}

// stateAge turns the seconds the server reported into a duration, taking a missing one as none.
func stateAge(seconds *int64) time.Duration {
	if seconds == nil || *seconds < 0 {
		return 0
	}

	return time.Duration(*seconds) * time.Second
}
