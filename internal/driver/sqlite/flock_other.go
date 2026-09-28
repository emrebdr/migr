//go:build !unix

package sqlite

import (
	"errors"
	"os"
)

// errNoFileLock marks a platform offering no advisory whole-file lock for this strategy to use.
var errNoFileLock = errors.New("this platform has no advisory whole-file lock, and lock.strategy " +
	"file is the only migration lock SQLite has: a second migration attempt could not be excluded")

// takeExclusive refuses: there is no advisory whole-file lock on this platform to take.
func takeExclusive(*os.File) (bool, error) {
	return false, errNoFileLock
}

// releaseExclusive is a no-op here, since takeExclusive never granted anything to give back.
func releaseExclusive(*os.File) error {
	return nil
}

// alive reports every process as still running, which is the pessimistic half of a missing check.
func alive(int) bool {
	return true
}
