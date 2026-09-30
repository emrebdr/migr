package mysql

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrDSN marks a connection string this driver refuses to open, for errors.Is.
var ErrDSN = errors.New("connection string cannot be used")

// ErrConnect marks a server this driver could not open a usable session against, for errors.Is.
var ErrConnect = errors.New("cannot open a MySQL session")

// ErrServerTooOld marks a server older than the release this driver is written against.
var ErrServerTooOld = errors.New("the server is older than this driver supports")

// ErrServerUnreadable marks a version string this driver will not guess the meaning of.
var ErrServerUnreadable = errors.New("this driver cannot read the server's version")

// ErrVariable marks a session setting that did not take, or that the server clamped.
var ErrVariable = errors.New("a session variable did not take")

// ErrCharacterSet marks a connection whose character set cannot carry every value as text.
var ErrCharacterSet = errors.New("this driver needs a utf8mb4 connection")

// ErrSchema marks a configured schema that disagrees with the database the connection string names.
var ErrSchema = errors.New("MySQL's schema is its database")

// ErrLockStrategy marks a migration lock strategy this driver does not implement, for errors.Is.
var ErrLockStrategy = errors.New("this driver does not implement that migration lock strategy")

// ErrWait marks a configured wait this engine cannot be asked to wait, for errors.Is.
var ErrWait = errors.New("the configured wait is not one MySQL can be asked to wait")

// ErrTransaction marks a migration transaction this driver could not run to an end, for errors.Is.
var ErrTransaction = errors.New("migration transaction")

// ErrTransactionOpen marks a transaction asked for on a session that is already inside one.
var ErrTransactionOpen = errors.New("this session already has a transaction open")

// ErrCommitDiscarded marks a commit MySQL answered by discarding the transaction instead.
var ErrCommitDiscarded = errors.New("the commit was answered by rolling back, so nothing it did " +
	"survives")

// ErrStatementNeedsNoTransaction marks a statement run where it declared it cannot run.
var ErrStatementNeedsNoTransaction = errors.New("this statement declares it cannot run inside a " +
	"transaction")

// ErrQuery marks a migration's own read this driver could not run, for errors.Is.
var ErrQuery = errors.New("reading on behalf of a migration")

// ErrPerform marks a migration's own statement this driver could not run, for errors.Is.
var ErrPerform = errors.New("running a statement on behalf of a migration")

// ErrNotText marks a value no JSON string can carry, which a read cannot report as text.
var ErrNotText = errors.New("this value is not text and host.db.query carries text")

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

// ErrRedefine marks a column change this driver compiles against the database but cannot, for errors.Is.
var ErrRedefine = errors.New("redefining a column")

// ErrIndexKey marks a key the server refused over the columns it names, for errors.Is.
var ErrIndexKey = errors.New("the server refused this key over the columns it names")

// RedefineError reports a column MySQL's full-redefinition statement cannot restate faithfully.
type RedefineError struct {
	// Table is the table holding the column.
	Table string
	// Column is the column the operation asked to change.
	Column string
	// Detail states what the redefinition would lose, or what went wrong reading the column.
	Detail string
	// Err is the server's own failure, where the refusal is not this driver's own.
	Err error
}

// Error names the column and what stops it being redefined.
func (e *RedefineError) Error() string {
	return fmt.Sprintf("%v %s.%s: %s", ErrRedefine, e.Table, e.Column, e.Detail)
}

// Unwrap reports the sentinel and the server's failure, so errors.Is finds either.
func (e *RedefineError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrRedefine}
	}

	return []error{ErrRedefine, e.Err}
}

// IndexKeyError reports a key the server refused because of a column type no operation carries.
type IndexKeyError struct {
	// Number is the error the server reported, which is what an operator will search for.
	Number uint16
	// Detail states the rule the server applied and what a migration can do about it.
	Detail string
	// Err is the server's own failure, kept so its message survives.
	Err error
}

// Error names the server's own error and the rule behind it.
func (e *IndexKeyError) Error() string {
	return fmt.Sprintf("%v (error %d): %s. The server said: %v", ErrIndexKey, e.Number, e.Detail,
		e.Err)
}

// Unwrap reports the sentinel and the server's failure, so errors.Is finds either.
func (e *IndexKeyError) Unwrap() []error {
	return []error{ErrIndexKey, e.Err}
}

// DSNError reports a connection string this driver will not open, withholding the string itself.
type DSNError struct {
	// Detail states what is wrong with it, naming no part of the string a secret could be in.
	Detail string
	// Err is the library's own parse failure, where there was one.
	Err error
}

// Error states what is wrong with the connection string, and never renders the string.
func (e *DSNError) Error() string {
	return fmt.Sprintf("%v: %s", ErrDSN, e.Detail)
}

// Unwrap reports the sentinel and the parse failure, so errors.Is finds either.
func (e *DSNError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrDSN}
	}

	return []error{ErrDSN, e.Err}
}

// VariableError reports a session variable that reads back as something other than what was set.
type VariableError struct {
	// Variable is the session variable that was set.
	Variable string
	// Want is the value this driver asked for.
	Want string
	// Got is what reading it back reported instead.
	Got string
}

// Error names the variable, what it was set to, and what it reads back as.
func (e *VariableError) Error() string {
	return fmt.Sprintf("%v: %s was set to %s and reads back as %s. MySQL clamps a value out of range "+
		"and reports success -- lock_wait_timeout = 0 silently becomes 1 -- so every variable this "+
		"driver owns is read back rather than assumed", ErrVariable, e.Variable, e.Want, e.Got)
}

// Unwrap reports the sentinel so errors.Is(err, ErrVariable) succeeds.
func (e *VariableError) Unwrap() error {
	return ErrVariable
}

// CharacterSetError reports a connection whose character set would rewrite values on the way out.
type CharacterSetError struct {
	// Got is what character_set_connection reported.
	Got string
	// Want is the one character set this driver reads through.
	Want string
}

// Error names the character set the connection has and the one this driver needs.
func (e *CharacterSetError) Error() string {
	return fmt.Sprintf("%v: character_set_connection is %q and this driver reads through %q. Every "+
		"value crosses the wire as a JSON string, so a connection that transcodes on the way out "+
		"hands a migration characters that are not in the database. Add %s to the connection string; "+
		"it is checked rather than set, because a character set is the operator's statement about "+
		"their server", ErrCharacterSet, e.Got, e.Want, strconv.Quote("charset="+e.Want))
}

// Unwrap reports the sentinel so errors.Is(err, ErrCharacterSet) succeeds.
func (e *CharacterSetError) Unwrap() error {
	return ErrCharacterSet
}

// SchemaError reports a configured schema that is not the database the connection string names.
type SchemaError struct {
	// Schema is what migr.yaml set databases.<name>.schema to.
	Schema string
	// Database is what the connection string named, empty when it named none.
	Database string
}

// Error names the configured schema and the database, which MySQL has only one of.
func (e *SchemaError) Error() string {
	return fmt.Sprintf("databases.schema is %q and the connection string names %s: %v, so the two "+
		"cannot disagree. Remove the key, or point the connection string at the same database",
		e.Schema, e.describeDatabase(), ErrSchema)
}

// describeDatabase names the database the connection string carries, or says it carries none.
func (e *SchemaError) describeDatabase() string {
	if e.Database == "" {
		return "no database at all"
	}

	return strconv.Quote(e.Database)
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
	// Detail states what MySQL would do with it instead.
	Detail string
}

// Error names the config key, what it was set to, and what the engine would do with it.
func (e *WaitError) Error() string {
	return fmt.Sprintf("%s is %s and %v: %s", e.Key, e.Requested, ErrWait, e.Detail)
}

// Unwrap reports the sentinel so errors.Is(err, ErrWait) succeeds.
func (e *WaitError) Unwrap() error {
	return ErrWait
}

// VersionError reports a server older than the release this driver reasons about.
type VersionError struct {
	// Reported is what VERSION() said.
	Reported string
	// Variant is the flavor this driver read out of it.
	Variant Variant
	// Minimum is the oldest release of that variant this driver is written against.
	Minimum string
}

// Error names the version that was found and the oldest one this driver supports.
func (e *VersionError) Error() string {
	return fmt.Sprintf("%v: the server reported %q, which this driver reads as %s, and it is written "+
		"against %s %s or newer", ErrServerTooOld, e.Reported, e.Variant, e.Variant, e.Minimum)
}

// Unwrap reports the sentinel so errors.Is(err, ErrServerTooOld) succeeds.
func (e *VersionError) Unwrap() error {
	return ErrServerTooOld
}

// UnreadableVersionError reports a version string this driver will not compare against anything.
type UnreadableVersionError struct {
	// Reported is what VERSION() said.
	Reported string
	// Detail states what could not be read out of it.
	Detail string
}

// Error names what the server reported and what could not be read out of it.
func (e *UnreadableVersionError) Error() string {
	return fmt.Sprintf("%v: VERSION() reported %q and %s. A verdict this driver keys to a release "+
		"cannot be resolved against something it cannot read, and invariant #13 forbids assuming one",
		ErrServerUnreadable, e.Reported, e.Detail)
}

// Unwrap reports the sentinel so errors.Is(err, ErrServerUnreadable) succeeds.
func (e *UnreadableVersionError) Unwrap() error {
	return ErrServerUnreadable
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
	return fmt.Sprintf("the server returned it as %T, which this driver has no text rendering for",
		value)
}

// quoteList renders a set of names for a message, in the order they were declared.
func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = strconv.Quote(name)
	}

	return strings.Join(quoted, ", ")
}
