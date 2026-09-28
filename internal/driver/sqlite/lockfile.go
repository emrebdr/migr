package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/emrebdr/migr/internal/version"
)

// lockSuffix names the lock file beside the database, following SQLite's own sidecar convention.
const lockSuffix = "-migr.lock"

// lockMode is what the lock file is created with, since it carries no secret and names a process.
const lockMode = 0o644

// maxRecord bounds what is read back, a record this driver wrote being one short line.
const maxRecord = 4096

// pathQuery asks the engine where the database it opened actually is, rather than parsing the DSN.
const pathQuery = `SELECT file FROM pragma_database_list WHERE name = 'main'`

// The three reasons a lock file names no holder, each of which a failed wait reports as itself.
const (
	emptyRecord      = "the lock file is there and empty, so its holder had not recorded itself yet"
	unreadableRecord = "the lock file holds something this driver cannot read as a record, which a " +
		"read catching a write mid-flight also looks like"
	incompleteRecord = "the lock file records no process, so there is nothing in it to name"
)

// detectPath asks SQLite where the database is, which is empty for every in-memory spelling.
func detectPath(ctx context.Context, c *sqliteConn) (string, error) {
	var path string

	if err := c.runner().QueryRowContext(ctx, pathQuery).Scan(&path); err != nil {
		return "", fmt.Errorf("%w: asking where the database file is: %w", ErrConnect, err)
	}

	return path, nil
}

// lockPath names the lock file for a database file, which is one per file and not per installation.
func lockPath(database string) string {
	return database + lockSuffix
}

// record is what a holder writes into the lock file, and is all a failed wait has to go on.
type record struct {
	// PID is the holder's process, which means nothing except on the host that recorded it.
	PID int `json:"pid"`
	// Host is what that machine calls itself, empty where the operating system would not say.
	Host string `json:"host"`
	// Application is what the holder called itself, as PostgreSQL's application_name is.
	Application string `json:"application"`
	// Acquired is when the lock was taken, as SQLite rendered it, this driver having no clock.
	Acquired string `json:"acquired"`
}

// contested reports a record naming a process that is running here and is not this one.
func (r record) contested() bool {
	return r.Host == hostname() && r.PID != os.Getpid() && alive(r.PID)
}

// describeProcess names the holder the way an operator would have to address it.
func (r record) describeProcess() string {
	if r.Host == "" {
		return fmt.Sprintf("pid %d", r.PID)
	}

	return fmt.Sprintf("pid %d on host %s", r.PID, r.Host)
}

// hostname is what this machine calls itself, read once, and empty where the system will not say.
var hostname = sync.OnceValue(func() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}

	return name
})

// applicationName is what this binary records itself as, matching what it tells PostgreSQL.
func applicationName() string {
	return version.Program + " " + version.Version
}

// openLockFile opens the lock file beside a database, creating one where no run has yet.
func openLockFile(path string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, lockMode)
	if err != nil {
		return nil, &LockError{Path: path, Err: fmt.Errorf("%w: %w", ErrLockFile, err)}
	}

	return file, nil
}

// closeLockFile gives the descriptor back, which is also the last thing that could hold the lock.
func closeLockFile(file *os.File) error {
	if err := file.Close(); err != nil {
		return &LockError{Path: file.Name(), Err: fmt.Errorf("%w: closing it: %w", ErrLockFile, err)}
	}

	return nil
}

// readRecord reads what the lock file says, reporting why there is no holder rather than failing.
func readRecord(file *os.File) (record, string, error) {
	buffer := make([]byte, maxRecord)

	read, err := file.ReadAt(buffer, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return record{}, "", &LockError{Path: file.Name(),
			Err: fmt.Errorf("%w: reading it: %w", ErrLockFile, err)}
	}

	return decodeRecord(bytes.TrimSpace(buffer[:read]))
}

// decodeRecord reads one record's bytes, treating anything it cannot read as naming nobody.
func decodeRecord(encoded []byte) (record, string, error) {
	if len(encoded) == 0 {
		return record{}, emptyRecord, nil
	}

	var held record

	if err := json.Unmarshal(encoded, &held); err != nil {
		return record{}, unreadableRecord, nil //nolint:nilerr // unreadable is a reason, not a failure
	}

	if held.PID <= 0 {
		return record{}, incompleteRecord, nil
	}

	return held, "", nil
}

// writeRecord records who holds the lock, so a run that gives up waiting has something to name.
func writeRecord(file *os.File, held record) error {
	encoded, err := json.Marshal(held)
	if err != nil {
		return &LockError{Path: file.Name(),
			Err: fmt.Errorf("%w: rendering the record: %w", ErrLockFile, err)}
	}

	encoded = append(encoded, '\n')

	if _, err := file.WriteAt(encoded, 0); err != nil {
		return &LockError{Path: file.Name(),
			Err: fmt.Errorf("%w: writing the record: %w", ErrLockFile, err)}
	}

	return truncateRecord(file, int64(len(encoded)))
}

// blankRecord empties the file, so a record can never outlive the hold it was written for.
func blankRecord(file *os.File) error {
	return truncateRecord(file, 0)
}

// truncateRecord cuts the file to a length, which is what leaves nothing of an older record behind.
func truncateRecord(file *os.File, length int64) error {
	if err := file.Truncate(length); err != nil {
		return &LockError{Path: file.Name(),
			Err: fmt.Errorf("%w: truncating it: %w", ErrLockFile, err)}
	}

	return nil
}
