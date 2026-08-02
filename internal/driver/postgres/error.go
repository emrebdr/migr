package postgres

import (
	"errors"
	"fmt"
	"time"
)

// ErrDSN marks a connection string this driver cannot parse, for errors.Is.
var ErrDSN = errors.New("connection string cannot be parsed")

// ErrConnect marks a server this driver could not open a usable session against, for errors.Is.
var ErrConnect = errors.New("cannot open a PostgreSQL session")

// ErrServerTooOld marks a server older than the release this driver can reason about.
var ErrServerTooOld = errors.New("PostgreSQL server is older than this driver supports")

// ErrLock marks a migration lock this driver could not take or give back, for errors.Is.
var ErrLock = errors.New("migration lock")

// ErrLockHeld marks a second acquire on a session that already holds the lock.
var ErrLockHeld = errors.New("this session already holds the migration lock")

// ErrLockNotHeld marks a release of a lock the server says this session did not hold.
var ErrLockNotHeld = errors.New("the server reports this session did not hold the migration lock")

// ErrLockRelease marks a release that failed, leaving the lock held until the session ends.
var ErrLockRelease = errors.New("releasing the migration lock failed")

// LockError reports a migration lock failure, naming the key so pg_locks can be matched to it.
type LockError struct {
	// Key is the advisory key this driver locks the installation on.
	Key lockKey
	// Err is what went wrong, one of this package's lock sentinels or a server error.
	Err error
}

// Error names the key and what went wrong with it.
func (e *LockError) Error() string {
	return fmt.Sprintf("%v %s: %v", ErrLock, e.Key, e.Err)
}

// Unwrap reports the sentinel and the cause, so errors.Is finds either.
func (e *LockError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrLock}
	}

	return []error{ErrLock, e.Err}
}

// ErrWaitTooLong marks a configured wait longer than this engine can store, for errors.Is.
var ErrWaitTooLong = errors.New("the configured wait is longer than PostgreSQL can store")

// WaitError reports a configured wait this driver cannot pass to the server, naming its config key.
type WaitError struct {
	// Key is the migr.yaml key that set it, so an operator edits the line they wrote.
	Key string
	// Wait is what that key was set to.
	Wait time.Duration
	// Max is the longest wait this engine holds.
	Max time.Duration
}

// Error names the config key, what it was set to, and the longest value the engine would keep.
func (e *WaitError) Error() string {
	return fmt.Sprintf("%v: %s is %s, and lock_timeout is an integer of milliseconds, so the "+
		"longest it holds is %s", ErrWaitTooLong, e.Key, e.Wait, e.Max)
}

// Unwrap reports the sentinel so errors.Is(err, ErrWaitTooLong) succeeds.
func (e *WaitError) Unwrap() error {
	return ErrWaitTooLong
}

// ErrLockStrategy marks a migration lock strategy this driver does not implement, for errors.Is.
var ErrLockStrategy = errors.New("this driver does not implement that migration lock strategy")

// LockStrategyError reports a configured strategy this engine has no implementation of.
type LockStrategyError struct {
	// Requested is what lock.strategy asked for.
	Requested string
	// Implemented is the strategy this driver has, which is the only one it accepts.
	Implemented string
}

// Error names what was asked for and the one strategy this driver implements.
func (e *LockStrategyError) Error() string {
	return fmt.Sprintf("%v: lock.strategy is %q, and this driver locks with %q. PostgreSQL's "+
		"session advisory lock is released by the server when the backend goes away, which is a "+
		"stale-lock story the other strategies have to write for themselves",
		ErrLockStrategy, e.Requested, e.Implemented)
}

// Unwrap reports the sentinel so errors.Is(err, ErrLockStrategy) succeeds.
func (e *LockStrategyError) Unwrap() error {
	return ErrLockStrategy
}

// ErrTransaction marks a migration transaction this driver could not run to an end, for errors.Is.
var ErrTransaction = errors.New("migration transaction")

// ErrTransactionOpen marks a transaction asked for on a session that is already inside one.
var ErrTransactionOpen = errors.New("this session already has a transaction open")

// ErrCommitDiscarded marks a commit the server answered by discarding the transaction instead.
var ErrCommitDiscarded = errors.New("the server answered the commit by rolling back, so nothing " +
	"this migration did was kept")

// ErrStatementNeedsNoTransaction marks a statement run where it declared it cannot run.
var ErrStatementNeedsNoTransaction = errors.New("this statement declares it cannot run inside a " +
	"transaction, and one is open")

// ErrHistory marks a migration history operation this driver could not perform, for errors.Is.
var ErrHistory = errors.New("migration history")

// ErrHistoryShape marks a history table whose columns are not the ones this build writes.
var ErrHistoryShape = errors.New("the migration history table is not the shape this build writes")

// ErrHistoryNotATable marks a history name held by a view or some other relation.
var ErrHistoryNotATable = errors.New("the configured history name is not a table")

// ErrHistoryRace marks a history table another process created between the check and the create.
var ErrHistoryRace = errors.New("another process created the migration history table first")

// ErrAlreadyRecorded marks a run already written under the batch and ordinal it claims.
var ErrAlreadyRecorded = errors.New("this run is already recorded")

// HistoryError reports a history failure, naming the table so an operator knows which one.
type HistoryError struct {
	// Table is the history table migr.yaml resolved to, unqualified as this driver writes it.
	Table string
	// Detail states what went wrong with it, in terms an operator can act on.
	Detail string
	// Err is the sentinel this failure reports, and the server error under it where there was one.
	Err error
}

// Error names the history table, what went wrong, and what the server said.
func (e *HistoryError) Error() string {
	return fmt.Sprintf("%v %q: %s", ErrHistory, e.Table, e.Detail)
}

// Unwrap reports the sentinel and the cause, so errors.Is finds either.
func (e *HistoryError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrHistory}
	}

	return []error{ErrHistory, e.Err}
}

// ErrNoSchema marks a configured schema the connected server does not have, for errors.Is.
var ErrNoSchema = errors.New("the configured schema does not exist on this server")

// SchemaError reports a configured schema PostgreSQL would silently ignore.
type SchemaError struct {
	// Schema is the name migr.yaml configured, which the server does not have.
	Schema string
}

// Error names the schema and what PostgreSQL would have done with it.
func (e *SchemaError) Error() string {
	return fmt.Sprintf("%v: databases.<name>.schema is %q, and PostgreSQL drops an entry naming no "+
		"schema from search_path rather than reporting it, so the first migration would fail with "+
		"\"no schema has been selected to create in\" and name neither", ErrNoSchema, e.Schema)
}

// Unwrap reports the sentinel so errors.Is(err, ErrNoSchema) succeeds.
func (e *SchemaError) Unwrap() error {
	return ErrNoSchema
}

// DSNError reports an unparsable connection string without ever rendering it.
type DSNError struct {
	// Err is the underlying parse failure, unwrapped for errors.Is but never shown.
	Err error
}

// Error states that the connection string will not parse, and deliberately not what it was.
func (e *DSNError) Error() string {
	return fmt.Sprintf("%v (its text is withheld: it may carry a password)", ErrDSN)
}

// Unwrap reports the sentinel and the underlying cause, so errors.Is finds either.
func (e *DSNError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrDSN}
	}

	return []error{ErrDSN, e.Err}
}

// VersionError reports a server whose version this driver will not work against.
type VersionError struct {
	// Reported is the raw server_version_num, unformatted: see this package's doc.
	Reported int
	// Minimum is the oldest server_version_num this driver accepts.
	Minimum int
}

// Error names the version the server reported and the oldest one this driver accepts.
func (e *VersionError) Error() string {
	return fmt.Sprintf("%v: the server reports server_version_num %d, and this driver requires "+
		"at least %d (PostgreSQL %d)", ErrServerTooOld, e.Reported, e.Minimum, e.Minimum/versionScale)
}

// Unwrap reports the sentinel so errors.Is(err, ErrServerTooOld) succeeds.
func (e *VersionError) Unwrap() error {
	return ErrServerTooOld
}
