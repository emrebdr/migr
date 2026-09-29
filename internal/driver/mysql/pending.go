package mysql

import (
	"context"
	"errors"

	"github.com/emrebdr/migr/internal/driver"
)

// The one half of the contract a later M9 task fills, refusing by name rather than by nil.
//
// A nil Unlock would reach a caller that cannot check for one, so this is a value that refuses and
// names the task that makes it real. That is M3-T1's shape: the driver ships complete and honestly
// unimplemented, so the conformance suite passes from this commit and M9-T4 arrives as a diff.

// errLockPending marks the migration lock this driver has not implemented yet.
var errLockPending = errors.New("the MySQL migration lock is M9-T4: GET_LOCK is connection-scoped, " +
	"so the same connection must hold and release it, and IS_USED_LOCK plus " +
	"information_schema.PROCESSLIST are what let a failed wait name the holder")

// Lock refuses: the migration lock is M9-T4.
func (c *mysqlConn) Lock(context.Context) (driver.Unlock, error) {
	return nil, errLockPending
}
