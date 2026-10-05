package sqlite

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrDSN marks a connection string this driver refuses to open, for errors.Is.
var ErrDSN = errors.New("connection string cannot be used")

// ErrConnect marks a database this driver could not open a usable session against, for errors.Is.
var ErrConnect = errors.New("cannot open a SQLite session")

// ErrServerTooOld marks a library older than the release this driver is written against.
var ErrServerTooOld = errors.New("SQLite is older than this driver supports")

// ErrPragma marks a session setting that did not take, which SQLite reports by not reporting.
var ErrPragma = errors.New("a session pragma did not take")

// ErrSchema marks a configured schema, which SQLite has nothing to resolve one against.
var ErrSchema = errors.New("SQLite has no schemas")

// ErrIntrospect marks a catalog this driver could not read a snapshot out of, for errors.Is.
var ErrIntrospect = errors.New("reading the schema")

// ErrLockStrategy marks a migration lock strategy this driver does not implement, for errors.Is.
var ErrLockStrategy = errors.New("this driver does not implement that migration lock strategy")

// ErrLock marks a migration lock this driver could not take or give back, for errors.Is.
var ErrLock = errors.New("migration lock")

// ErrLockHeld marks a second acquire on a session that already holds the lock.
var ErrLockHeld = errors.New("this session already holds the migration lock")

// ErrLockFile marks the file this strategy locks, which is where every such failure happens.
var ErrLockFile = errors.New("the lock file beside the database")

// ErrLockRelease marks a release that failed, leaving the lock held until the session ends.
var ErrLockRelease = errors.New("releasing the migration lock failed")

// LockError reports a migration lock this driver could not take or give back, naming the file.
type LockError struct {
	// Path is the lock file, and is empty for a database that has no file to put one beside.
	Path string
	// Err is what went wrong, unwrapped so errors.Is finds the sentinel underneath it.
	Err error
}

// Error names the lock file and what went wrong with it.
func (e *LockError) Error() string {
	return fmt.Sprintf("%v %s: %v", ErrLock, e.describePath(), e.Err)
}

// describePath names the file, or says there is none, which is the in-memory database's answer.
func (e *LockError) describePath() string {
	if e.Path == "" {
		return "for a database with no file"
	}

	return strconv.Quote(e.Path)
}

// Unwrap reports the sentinel and what went wrong, so errors.Is finds either.
func (e *LockError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrLock}
	}

	return []error{ErrLock, e.Err}
}

// ErrWaitTooLong marks a configured wait longer than this engine can store, for errors.Is.
var ErrWaitTooLong = errors.New("the configured wait is longer than SQLite can store")

// ErrTransaction marks a migration transaction this driver could not run to an end, for errors.Is.
var ErrTransaction = errors.New("migration transaction")

// ErrTransactionOpen marks a transaction asked for on a session that is already inside one.
var ErrTransactionOpen = errors.New("this session already has a transaction open")

// ErrCommitDiscarded marks a commit SQLite answered by discarding the transaction instead.
var ErrCommitDiscarded = errors.New("the commit was answered by rolling back, so nothing it did " +
	"survives")

// ErrStatementNeedsNoTransaction marks a statement run where it declared it cannot run.
var ErrStatementNeedsNoTransaction = errors.New("this statement declares it cannot run inside a " +
	"transaction")

// ErrQuery marks a migration's own read this driver could not run, for errors.Is.
var ErrQuery = errors.New("reading on behalf of a migration")

// ErrPerform marks a migration's own statement this driver could not run, for errors.Is.
var ErrPerform = errors.New("running a statement on behalf of a migration")

// ErrRebuild marks a table rebuild this driver could not build the statements for, for errors.Is.
var ErrRebuild = errors.New("cannot rebuild this table")

// RebuildError reports a table this driver will not rebuild, and what stopped it.
type RebuildError struct {
	// Table is the table the rebuild was for.
	Table string
	// Detail states what stopped it, in terms an operator can act on.
	Detail string
}

// Error names the table and what stopped the rebuild.
func (e *RebuildError) Error() string {
	return fmt.Sprintf("%v %q: %s", ErrRebuild, e.Table, e.Detail)
}

// Unwrap reports the sentinel so errors.Is reaches it.
func (e *RebuildError) Unwrap() error {
	return ErrRebuild
}

// ErrHistory marks a migration history operation this driver could not perform, for errors.Is.
var ErrHistory = errors.New("migration history")

// ErrHistoryShape marks a history table whose columns are not the ones this build writes.
var ErrHistoryShape = errors.New("the migration history table is not the shape this build writes")

// ErrHistoryNotATable marks a history name held by a view or some other object.
var ErrHistoryNotATable = errors.New("the configured history name is not a table")

// ErrHistoryRace marks a history table another process created between the check and the create.
var ErrHistoryRace = errors.New("another process created the migration history table first")

// ErrAlreadyRecorded marks a run already written under the batch and ordinal it claims.
var ErrAlreadyRecorded = errors.New("this run is already recorded")

// ErrNotText marks a value no JSON string can carry, which a read cannot report as text.
var ErrNotText = errors.New("this value is not text and host.db.query carries text")

// DSNError reports a connection string this driver will not open, withholding the string itself.
type DSNError struct {
	// Detail states what is wrong with it, naming no part of the string a secret could be in.
	Detail string
}

// Error states what is wrong with the connection string, and never renders the string.
func (e *DSNError) Error() string {
	return fmt.Sprintf("%v: %s", ErrDSN, e.Detail)
}

// Unwrap reports the sentinel so errors.Is(err, ErrDSN) succeeds.
func (e *DSNError) Unwrap() error {
	return ErrDSN
}

// PragmaError reports a session setting SQLite accepted and did not apply.
type PragmaError struct {
	// Pragma is the setting that was asked for.
	Pragma string
	// Want is the value this driver set it to.
	Want string
	// Got is what reading it back reported instead.
	Got string
}

// Error names the pragma, what it was set to, and what it reads back as.
func (e *PragmaError) Error() string {
	return fmt.Sprintf("%v: PRAGMA %s was set to %s and reads back as %s. SQLite reports success for "+
		"a pragma it ignores -- inside a transaction, foreign_keys is silently a no-op -- so this is "+
		"read back rather than assumed", ErrPragma, e.Pragma, e.Want, e.Got)
}

// Unwrap reports the sentinel so errors.Is(err, ErrPragma) succeeds.
func (e *PragmaError) Unwrap() error {
	return ErrPragma
}

// SchemaError reports a configured schema, which this engine has no namespace to resolve.
type SchemaError struct {
	// Schema is what migr.yaml set databases.<name>.schema to.
	Schema string
}

// Error names the configured schema and says why SQLite cannot be pointed at one.
func (e *SchemaError) Error() string {
	return fmt.Sprintf("databases.schema is %q and %v: a database is one file, and the only other "+
		"namespaces are the ones ATTACH creates, which no migration here opens. Remove the key rather "+
		"than leaving it to be ignored", e.Schema, ErrSchema)
}

// Unwrap reports the sentinel so errors.Is(err, ErrSchema) succeeds.
func (e *SchemaError) Unwrap() error {
	return ErrSchema
}

// LockStrategyError reports a migration lock strategy this driver has no implementation of.
type LockStrategyError struct {
	// Requested is what lock.strategy asked for.
	Requested string
	// Implemented is the one strategy this driver has.
	Implemented string
}

// Error names what was asked for and the one strategy this driver implements.
func (e *LockStrategyError) Error() string {
	return fmt.Sprintf("lock.strategy is %q and %v: %s implements %q", e.Requested, ErrLockStrategy,
		Name, e.Implemented)
}

// Unwrap reports the sentinel so errors.Is(err, ErrLockStrategy) succeeds.
func (e *LockStrategyError) Unwrap() error {
	return ErrLockStrategy
}

// WaitError reports a configured wait this engine cannot be asked to wait.
type WaitError struct {
	// Key is the migr.yaml key that set it.
	Key string
	// Requested is what it was set to.
	Requested string
	// Limit is the longest value SQLite keeps.
	Limit string
}

// Error names the config key, what it was set to, and the longest value the engine would keep.
func (e *WaitError) Error() string {
	return fmt.Sprintf("%s is %s and %v: busy_timeout is a whole number of milliseconds and the "+
		"longest it holds is %s", e.Key, e.Requested, ErrWaitTooLong, e.Limit)
}

// Unwrap reports the sentinel so errors.Is(err, ErrWaitTooLong) succeeds.
func (e *WaitError) Unwrap() error {
	return ErrWaitTooLong
}

// VersionError reports a library older than the release this driver reasons about.
type VersionError struct {
	// Reported is what sqlite_version() said.
	Reported string
	// Minimum is the oldest release this driver is written against.
	Minimum string
}

// Error names the version that was found and the oldest one this driver supports.
func (e *VersionError) Error() string {
	return fmt.Sprintf("%v: this build links SQLite %s and this driver is written against %s or "+
		"newer", ErrServerTooOld, e.Reported, e.Minimum)
}

// Unwrap reports the sentinel so errors.Is(err, ErrServerTooOld) succeeds.
func (e *VersionError) Unwrap() error {
	return ErrServerTooOld
}

// NotTextError reports a value a read cannot carry as a JSON string, naming its column.
type NotTextError struct {
	// Column is the column the value came from, as the statement selected it.
	Column string
	// Detail states what the value is, without rendering bytes no document could hold.
	Detail string
}

// Error names the column and what about its value cannot be carried.
func (e *NotTextError) Error() string {
	return fmt.Sprintf("column %q: %s, and %v", e.Column, e.Detail, ErrNotText)
}

// Unwrap reports the sentinel so errors.Is(err, ErrNotText) succeeds.
func (e *NotTextError) Unwrap() error {
	return ErrNotText
}

// HistoryError reports a migration history this driver could not carry, create, read, or write.
type HistoryError struct {
	// Table is the name history.table was set to.
	Table string
	// Detail states what is wrong, in terms an operator can act on.
	Detail string
	// Err is the failure underneath, where there was one.
	Err error
}

// Error names the history table and what went wrong with it.
func (e *HistoryError) Error() string {
	return fmt.Sprintf("%v %q: %s", ErrHistory, e.Table, e.Detail)
}

// Unwrap reports the sentinel and the failure underneath, so errors.Is finds either.
func (e *HistoryError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrHistory}
	}

	return []error{ErrHistory, e.Err}
}

// describeValue names a value's Go type for a refusal, since its bytes are what cannot be rendered.
func describeValue(value any) string {
	return fmt.Sprintf("SQLite returned it as %T, which this driver has no text rendering for", value)
}

// quoteList renders a set of names for a message, in the order they were declared.
func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = strconv.Quote(name)
	}

	return strings.Join(quoted, ", ")
}
