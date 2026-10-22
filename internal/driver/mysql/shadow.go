package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// A shadow is created LIKE the table, changed, filled a keyed chunk at a time, and swapped in under LOCK TABLES.
//
// Each chunk is INSERT ... SELECT ... FOR SHARE ON DUPLICATE KEY UPDATE k = k: a row a replay already
// wrote is kept, and nothing else is downgraded -- INSERT IGNORE would also turn a truncation or a NULL
// into a NOT NULL column into a warning, which a narrowing change would meet silently. FOR SHARE waits
// for a writer whose transaction is in the log but not yet visible, since a reader of the log can see an
// event before InnoDB's commit is; without it a delete replayed first would be undone by a stale copy. A
// replay is REPLACE for a row an insert or update left and DELETE by key for one removed, so applying a
// change twice is applying it once.
//
// The cutover is LOCK TABLES on the table, the shadow and the shadow-state table, the catch-up under it,
// the AUTO_INCREMENT copied across, RENAME TABLE of both, and UNLOCK TABLES -- measured on 8.0.46, a
// writer queued behind the lock lands in the new table once it is released. LOCK TABLES ends the
// transaction the engine opened around the cutover, and a START TRANSACTION would release the locks, so
// nothing under them begins one: each write commits on its own, and the crash window between the swap
// and its record is the one the engine already reads back from the catalog.

// checkQuery lists one table's check constraints by name.
const checkQuery = "SELECT CONSTRAINT_NAME FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = " +
	"DATABASE() AND TABLE_NAME = ? AND CONSTRAINT_TYPE = 'CHECK' ORDER BY CONSTRAINT_NAME"

// foreignKeyQuery lists every foreign key on one table or into it.
const foreignKeyQuery = "SELECT CONSTRAINT_NAME, TABLE_NAME FROM information_schema.REFERENTIAL_CONSTRAINTS " +
	"WHERE CONSTRAINT_SCHEMA = DATABASE() AND (TABLE_NAME = ? OR REFERENCED_TABLE_NAME = ?) " +
	"ORDER BY TABLE_NAME, CONSTRAINT_NAME"

// triggerQuery lists the triggers on one table.
const triggerQuery = "SELECT TRIGGER_NAME FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = DATABASE() " +
	"AND EVENT_OBJECT_TABLE = ? ORDER BY TRIGGER_NAME"

// versionedQuery names a table where it is MariaDB's system-versioned kind.
const versionedQuery = "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() " +
	"AND TABLE_NAME = ? AND TABLE_TYPE = 'SYSTEM VERSIONED'"

// autoIncrementOption reads the next AUTO_INCREMENT value out of a SHOW CREATE TABLE.
var autoIncrementOption = regexp.MustCompile(`\) .*AUTO_INCREMENT=(\d+)`)

// deleteGroup is how many deleted keys one DELETE removes, and upsertGroup how many rows one REPLACE writes.
const (
	deleteGroup = 100
	upsertGroup = 100
)

// Shadows reports why an operation cannot be performed through a shadow table on MySQL.
func (c *mysqlConn) Shadows(op ir.Operation) error {
	switch op.(type) {
	case ir.AddColumn, ir.DropColumn, ir.AlterColumn, ir.AddIndex, ir.DropIndex, ir.AddCheck, ir.DropUnique:
		return nil
	case ir.AddUnique:
		return errors.New("a unique constraint added through a shadow would keep the first of each " +
			"duplicate the copy meets and silently drop the rest, where adding it in place refuses them")
	case ir.RenameColumn:
		return errors.New("the copy matches columns by name, so a renamed column's values would not " +
			"reach the shadow")
	case ir.DropCheck:
		return errors.New("CREATE TABLE ... LIKE gives every check a name of the shadow's own, so the " +
			"check named here is not one the shadow holds")
	case ir.AddForeignKey, ir.DropForeignKey:
		return errors.New("a foreign key would follow the original to its retired name at the cutover")
	default:
		return fmt.Errorf("%s is not a change to a table's columns, indexes or checks", op.Kind())
	}
}

// Admits reports why this table cannot be changed through a shadow on this server, reading its catalog.
func (c *mysqlConn) Admits(ctx context.Context, s driver.Shadow) error {
	if refusal := streamRefusal(c.version); refusal != "" {
		return errors.New(refusal)
	}

	table := storedName(s.Table, c.folding)

	var problems []string

	for _, read := range []func(context.Context, string) (string, error){
		c.foreignKeysOf, c.triggersOf, c.namedChecksOf, c.versionedOf, c.unreplayableOf,
	} {
		problem, err := read(ctx, table)
		if err != nil {
			return fmt.Errorf("reading whether %s can be changed through a shadow: %w", s.Table, err)
		}

		if problem != "" {
			problems = append(problems, problem)
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%s %s", s.Table, strings.Join(problems, ", and it "))
	}

	return nil
}

// foreignKeysOf says which foreign keys name a table, which a rename would leave pointing at the retired one.
func (c *mysqlConn) foreignKeysOf(ctx context.Context, table string) (string, error) {
	keys, err := c.names(ctx, foreignKeyQuery, table, table)
	if err != nil || len(keys) == 0 {
		return "", err
	}

	return "takes part in the foreign keys " + strings.Join(keys, ", ") + ", which the cutover's rename " +
		"would leave on the retired original", nil
}

// triggersOf says which triggers a table has, which a rename would carry to the retired original.
func (c *mysqlConn) triggersOf(ctx context.Context, table string) (string, error) {
	triggers, err := c.names(ctx, triggerQuery, table)
	if err != nil || len(triggers) == 0 {
		return "", err
	}

	return "has the triggers " + strings.Join(triggers, ", ") + ", which the cutover's rename would " +
		"carry to the retired original", nil
}

// namedChecksOf says which checks carry a name of their own, which CREATE TABLE ... LIKE renames on MySQL alone.
func (c *mysqlConn) namedChecksOf(ctx context.Context, table string) (string, error) {
	if c.version.Variant() == MariaDB {
		return "", nil
	}

	checks, err := c.names(ctx, checkQuery, table)
	if err != nil {
		return "", err
	}

	generated := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(table) + `_chk_\d+$`)

	var named []string

	for _, check := range checks {
		if !generated.MatchString(check) {
			named = append(named, check)
		}
	}

	if len(named) == 0 {
		return "", nil
	}

	return "has the named checks " + strings.Join(named, ", ") + ", which CREATE TABLE ... LIKE renames " +
		"on the shadow, so they would not survive the cutover by name", nil
}

// versionedOf says whether a table keeps system-versioned history, which MariaDB alone does and a copy would not carry.
func (c *mysqlConn) versionedOf(ctx context.Context, table string) (string, error) {
	if c.version.Variant() != MariaDB {
		return "", nil
	}

	versioned, err := c.names(ctx, versionedQuery, table)
	if err != nil || len(versioned) == 0 {
		return "", err
	}

	return "is SYSTEM VERSIONED, and a copy carries its current rows and none of the history the " +
		"retired original would keep", nil
}

// unreplayableOf says which columns hold a type no captured change can be replayed into.
func (c *mysqlConn) unreplayableOf(ctx context.Context, table string) (string, error) {
	columns, err := readSourceColumns(ctx, c.runner(), table)
	if err != nil {
		return "", err
	}

	if bad := unreplayableColumns(columns); len(bad) > 0 {
		return "has columns whose values a change stream cannot replay: " + strings.Join(bad, ", "), nil
	}

	return "", nil
}

// names runs a query listing names in its first column.
func (c *mysqlConn) names(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := c.runner().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	values := make([]sql.NullString, len(columns))
	targets := make([]any, len(values))

	for i := range values {
		targets[i] = &values[i]
	}

	var found []string

	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}

		found = append(found, described(values))
	}

	return found, rows.Err()
}

// described renders a catalog row naming something, the table it is on following in parentheses.
func described(values []sql.NullString) string {
	if len(values) > 1 {
		return values[0].String + " (on " + values[1].String + ")"
	}

	return values[0].String
}

// CreateShadow returns the statements creating the shadow LIKE the table and making the operations' changes to it.
func (c *mysqlConn) CreateShadow(ctx context.Context, s driver.Shadow) ([]driver.Statement, error) {
	stmts := one("CREATE TABLE " + quote(s.Shadow) + " LIKE " + quote(s.Table))

	for _, op := range s.Ops {
		moved, err := retarget(op, s.Shadow)
		if err != nil {
			return nil, err
		}

		compiled, err := compile(moved)
		if errors.Is(err, driver.ErrNeedsSession) {
			compiled, err = c.expandOnto(ctx, op, moved)
		}

		if err != nil {
			return nil, err
		}

		stmts = append(stmts, compiled...)
	}

	return stmts, nil
}

// expandOnto redefines a shadow's column from the original's catalog row, the two being identical before the change.
func (c *mysqlConn) expandOnto(ctx context.Context, op, moved ir.Operation) ([]driver.Statement, error) {
	altered, ok := op.(ir.AlterColumn)
	if !ok {
		return nil, fmt.Errorf("%w: %s is not an operation this driver compiles against the database",
			ErrRedefine, op.Kind())
	}

	found, err := c.readColumn(ctx, altered.Table, altered.Column)
	if err != nil {
		return nil, err
	}

	if err := c.checkRestatable(ctx, altered, found); err != nil {
		return nil, err
	}

	restated, err := restate(moved.(ir.AlterColumn), found)
	if err != nil {
		return nil, err
	}

	return one(restated.render()), nil
}

// retarget returns an operation naming the shadow where it named the table.
func retarget(op ir.Operation, shadow string) (ir.Operation, error) {
	switch o := op.(type) {
	case ir.AddColumn:
		o.Table = shadow
		return o, nil
	case ir.DropColumn:
		o.Table = shadow
		return o, nil
	case ir.AlterColumn:
		o.Table = shadow
		return o, nil
	case ir.AddIndex:
		o.Table = shadow
		return o, nil
	case ir.DropIndex:
		o.Table = shadow
		return o, nil
	case ir.AddCheck:
		o.Table = shadow
		return o, nil
	case ir.DropUnique:
		o.Table = shadow
		return o, nil
	default:
		return nil, fmt.Errorf("%s is not an operation a shadow carries on MySQL", op.Kind())
	}
}

// pair is what a copy and a replay know about a table and its shadow: the columns both write.
type pair struct {
	// shared are the table's written columns the shadow also writes, in the table's order.
	shared []sourceColumn
	// byName are the table's columns by name, which say how each captured value is bound.
	byName map[string]sourceColumn
	// writes reports a column the shadow writes.
	writes map[string]bool
}

// pairOf reads the columns a table and its shadow both write, once per shadow.
func (c *mysqlConn) pairOf(ctx context.Context, s driver.Shadow) (pair, error) {
	if found, ok := c.pairs[s.Shadow]; ok {
		return found, nil
	}

	source, err := readSourceColumns(ctx, c.runner(), storedName(s.Table, c.folding))
	if err != nil {
		return pair{}, err
	}

	target, err := readSourceColumns(ctx, c.runner(), storedName(s.Shadow, c.folding))
	if err != nil {
		return pair{}, err
	}

	p := pair{byName: named(source), writes: map[string]bool{}}
	for name := range named(target) {
		p.writes[strings.ToLower(name)] = true
	}

	for _, column := range written(source) {
		if p.writes[strings.ToLower(column.name)] {
			p.shared = append(p.shared, column)
		}
	}

	if c.pairs == nil {
		c.pairs = map[string]pair{}
	}

	c.pairs[s.Shadow] = p

	return p, nil
}

// columnNames returns the columns' names in order.
func columnNames(columns []sourceColumn) []string {
	out := make([]string, len(columns))
	for i, column := range columns {
		out[i] = column.name
	}

	return out
}

// CopyChunk copies the rows after a key into the shadow inside the open transaction, keeping any already there.
func (c *mysqlConn) CopyChunk(ctx context.Context, s driver.Shadow, after []string, rows int) (driver.Sliced, error) {
	var out driver.Sliced

	err := c.interruptible(ctx, func(ctx context.Context) error {
		sliced, err := c.copyChunk(ctx, s, after, rows)
		out = sliced

		return err
	})

	return out, err
}

// copyChunk finds the chunk's bound and copies the rows up to it.
func (c *mysqlConn) copyChunk(ctx context.Context, s driver.Shadow, after []string, rows int) (driver.Sliced, error) {
	if c.tx == nil {
		return driver.Sliced{}, fmt.Errorf("%w: a copy chunk runs inside the transaction its progress is "+
			"recorded in, and this session has none open", ErrTransaction)
	}

	if rows < 1 || len(s.Key) == 0 {
		return driver.Sliced{}, fmt.Errorf("a copy chunk of %s must cover a row and page by a key", s.Table)
	}

	p, err := c.pairOf(ctx, s)
	if err != nil {
		return driver.Sliced{}, err
	}

	cols := quoteAll(columnNames(p.shared))
	shape := chunkShape{table: s.Table, head: "INSERT INTO " + quote(s.Shadow) + " (" + cols + ") SELECT " +
		cols + " FROM " + quote(s.Table)}

	bound, found, err := c.copyBound(ctx, shape, s, after, rows)
	if err != nil {
		return driver.Sliced{}, err
	}

	statement := shape.write(s.Key, len(after) > 0, found) + " ORDER BY " + quoteAll(s.Key) +
		c.shareLock() + " ON DUPLICATE KEY UPDATE " + quote(s.Key[0]) + " = " + quote(s.Shadow) + "." + quote(s.Key[0])

	args := spelled(after)
	if found {
		args = append(args, spelled(bound)...)
	}

	result, err := c.runner().ExecContext(ctx, statement, args...)
	if err != nil {
		return driver.Sliced{}, fmt.Errorf("copying rows of %s into %s: %w", s.Table, s.Shadow, explainExec(err))
	}

	copied, err := result.RowsAffected()
	if err != nil {
		return driver.Sliced{}, fmt.Errorf("counting the rows copied into %s: %w", s.Shadow, err)
	}

	if !found {
		return driver.Sliced{Written: copied, Last: after, Done: true}, nil
	}

	return driver.Sliced{Written: copied, Last: bound}, nil
}

// shareLock is the clause a copy reads its rows under, which MariaDB spells the older way and refuses FOR SHARE.
func (c *mysqlConn) shareLock() string {
	if c.version.Variant() == MariaDB {
		return " LOCK IN SHARE MODE"
	}

	return " FOR SHARE"
}

// copyBound reads the key of the last row a copy chunk covers, and reports none when fewer rows remain.
func (c *mysqlConn) copyBound(ctx context.Context, shape chunkShape, s driver.Shadow, after []string,
	rows int,
) ([]string, bool, error) {
	values := make([]sql.NullString, len(s.Key))
	targets := make([]any, len(values))

	for i := range values {
		targets[i] = &values[i]
	}

	err := c.runner().QueryRowContext(ctx, shape.boundQuery(s.Key, len(after) > 0),
		append(spelled(after), rows-1)...).Scan(targets...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("reading where a copy chunk of %s ends: %w", s.Table, explainExec(err))
	}

	bound := make([]string, len(values))

	for i, value := range values {
		if !value.Valid {
			return nil, false, fmt.Errorf("the copy of %s reached a row whose key column %q is NULL, "+
				"which no cursor can resume after", s.Table, s.Key[i])
		}

		bound[i] = value.String
	}

	return bound, true, nil
}

// ApplyChanges writes captured changes to the shadow inside the open transaction, each by its key.
func (c *mysqlConn) ApplyChanges(ctx context.Context, s driver.Shadow, changes []capture.Change) error {
	return c.interruptible(ctx, func(ctx context.Context) error {
		return c.applyChanges(ctx, s, changes)
	})
}

// applyChanges writes the changes in order, a run of one kind at a time, with timestamps read as UTC.
func (c *mysqlConn) applyChanges(ctx context.Context, s driver.Shadow, changes []capture.Change) (err error) {
	p, err := c.pairOf(ctx, s)
	if err != nil {
		return err
	}

	restore, err := c.inUTC(ctx)
	if err != nil {
		return err
	}

	defer func() { err = errors.Join(err, restore()) }()

	for start := 0; start < len(changes); {
		end := start + 1
		for end < len(changes) && removes(changes[end]) == removes(changes[start]) {
			end++
		}

		if err := c.applyRun(ctx, s, p, changes[start:end]); err != nil {
			return err
		}

		start = end
	}

	return nil
}

// removes reports a change that deletes its row.
func removes(change capture.Change) bool {
	return change.Kind == capture.Delete
}

// applyRun writes a run of changes of one kind, a group at a time.
func (c *mysqlConn) applyRun(ctx context.Context, s driver.Shadow, p pair, run []capture.Change) error {
	group := upsertGroup
	if removes(run[0]) {
		group = deleteGroup
	}

	for start := 0; start < len(run); start += group {
		part := run[start:min(start+group, len(run))]

		statement, args, err := c.applyStatement(s, p, part)
		if err != nil {
			return err
		}

		if _, err := c.runner().ExecContext(ctx, statement, args...); err != nil {
			return fmt.Errorf("replaying %d captured changes into %s: %w", len(part), s.Shadow, explainExec(err))
		}
	}

	return nil
}

// applyStatement renders one group of changes as a REPLACE or a DELETE, and the arguments it binds.
func (c *mysqlConn) applyStatement(s driver.Shadow, p pair, part []capture.Change) (string, []any, error) {
	if removes(part[0]) {
		return c.deleteStatement(s, p, part)
	}

	return c.replaceStatement(s, p, part)
}

// replaceStatement renders a group of rows as one REPLACE, every row carrying the columns the shadow writes.
func (c *mysqlConn) replaceStatement(s driver.Shadow, p pair, part []capture.Change) (string, []any, error) {
	var (
		columns []string
		rows    []string
		args    []any
	)

	for _, name := range part[0].Columns {
		if p.writes[strings.ToLower(name)] {
			columns = append(columns, name)
		}
	}

	for _, change := range part {
		values, bound, err := c.bindRow(p, change, columns)
		if err != nil {
			return "", nil, err
		}

		rows = append(rows, "("+strings.Join(values, ", ")+")")
		args = append(args, bound...)
	}

	return "REPLACE INTO " + quote(s.Shadow) + " (" + quoteAll(columns) + ") VALUES " +
		strings.Join(rows, ", "), args, nil
}

// bindRow binds one change's values for the named columns, by the class of the table's own column.
func (c *mysqlConn) bindRow(p pair, change capture.Change, columns []string) ([]string, []any, error) {
	values := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))

	for _, name := range columns {
		index := indexOf(change.Columns, name)
		if index < 0 || index >= len(change.Row) {
			return nil, nil, fmt.Errorf("a captured change to %s carries no value for %s", change.Table, name)
		}

		placeholder, arg, err := bind(p.byName[name], change.Row[index])
		if err != nil {
			return nil, nil, err
		}

		values, args = append(values, placeholder), append(args, arg)
	}

	return values, args, nil
}

// indexOf returns where a name is in a list, and -1 where it is not.
func indexOf(names []string, name string) int {
	for i, candidate := range names {
		if candidate == name {
			return i
		}
	}

	return -1
}

// deleteStatement renders a group of deleted keys as one DELETE.
func (c *mysqlConn) deleteStatement(s driver.Shadow, p pair, part []capture.Change) (string, []any, error) {
	var (
		matches []string
		args    []any
	)

	for _, change := range part {
		values, bound, err := c.bindRow(p, change, s.Key)
		if err != nil {
			return "", nil, err
		}

		conditions := make([]string, len(s.Key))
		for i, column := range s.Key {
			conditions[i] = quote(column) + " = " + values[i]
		}

		matches = append(matches, "("+strings.Join(conditions, " AND ")+")")
		args = append(args, bound...)
	}

	return "DELETE FROM " + quote(s.Shadow) + " WHERE " + strings.Join(matches, " OR "), args, nil
}

// inUTC sets this session's time zone to UTC, which every captured timestamp is rendered in, and returns its undo.
func (c *mysqlConn) inUTC(ctx context.Context) (func() error, error) {
	var zone string
	if err := c.runner().QueryRowContext(ctx, "SELECT @@session.time_zone").Scan(&zone); err != nil {
		return nil, fmt.Errorf("reading this session's time zone: %w", err)
	}

	if _, err := c.runner().ExecContext(ctx, "SET time_zone = '+00:00'"); err != nil {
		return nil, fmt.Errorf("reading captured timestamps as UTC: %w", err)
	}

	return func() error {
		if _, err := c.runner().ExecContext(context.WithoutCancel(ctx), "SET time_zone = ?", zone); err != nil {
			return fmt.Errorf("restoring this session's time zone %s: %w", zone, err)
		}

		return nil
	}, nil
}

// Cutover locks the table, the shadow and the shadow state, catches up under the lock, and swaps the two by rename.
func (c *mysqlConn) Cutover(ctx context.Context, s driver.Shadow, catchUp driver.CatchUp) (err error) {
	if _, err := c.pairOf(ctx, s); err != nil {
		return err
	}

	if c.version.Variant() == MariaDB {
		return c.cutoverMariaDB(ctx, s, catchUp)
	}

	locked := "LOCK TABLES " + quote(s.Table) + " WRITE, " + quote(s.Shadow) + " WRITE, " +
		quote(history.ShadowTable(c.table)) + " WRITE"
	if _, err := c.runner().ExecContext(ctx, locked); err != nil {
		return fmt.Errorf("locking %s for the cutover: %w", s.Table, explainExec(err))
	}

	defer func() { err = errors.Join(err, c.unlockTables(ctx)) }()

	if err := c.catchUpBounded(ctx, catchUp); err != nil {
		return err
	}

	if err := c.carryAutoIncrement(ctx, s); err != nil {
		return err
	}

	renamed := "RENAME TABLE " + quote(s.Table) + " TO " + quote(s.Retired) + ", " + quote(s.Shadow) +
		" TO " + quote(s.Table)
	if _, err := c.runner().ExecContext(ctx, renamed); err != nil {
		return fmt.Errorf("swapping %s in for %s: %w", s.Shadow, s.Table, explainExec(err))
	}

	return nil
}

// unlockTables releases the cutover's lock, on a context that has not been canceled.
func (c *mysqlConn) unlockTables(ctx context.Context) error {
	if _, err := c.runner().ExecContext(context.WithoutCancel(ctx), "UNLOCK TABLES"); err != nil {
		return fmt.Errorf("releasing the cutover's lock: %w", err)
	}

	return nil
}

// catchUpBounded runs the catch-up for at most safety.lock_timeout, since every writer waits on it.
func (c *mysqlConn) catchUpBounded(ctx context.Context, catchUp driver.CatchUp) error {
	bounded, cancel := context.WithTimeout(ctx, c.cutoverWait())
	defer cancel()

	err := catchUp(bounded)
	if err != nil && ctx.Err() == nil && errors.Is(bounded.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: catching the shadow up under the cutover's lock took longer than "+
			"safety.lock_timeout (%s), so the lock was released for writers to go on: %w",
			driver.ErrStatementLockTimeout, c.cutoverWait(), err)
	}

	return err
}

// cutoverWait is how long writers may wait on the cutover's catch-up, which is the statement lock wait.
func (c *mysqlConn) cutoverWait() time.Duration {
	if c.statementWait > 0 {
		return c.statementWait
	}

	return driver.DefaultStatementLockWait
}

// carryAutoIncrement gives the shadow the table's next AUTO_INCREMENT value, so no id is handed out twice.
func (c *mysqlConn) carryAutoIncrement(ctx context.Context, s driver.Shadow) error {
	var name, created string
	if err := c.runner().QueryRowContext(ctx, "SHOW CREATE TABLE "+quote(s.Table)).Scan(&name, &created); err != nil {
		return fmt.Errorf("reading the AUTO_INCREMENT of %s: %w", s.Table, err)
	}

	next := autoIncrementOption.FindStringSubmatch(created)
	if next == nil {
		return nil
	}

	value, err := strconv.ParseUint(next[1], 10, 64)
	if err != nil {
		return fmt.Errorf("reading the AUTO_INCREMENT of %s: %q is not a number", s.Table, next[1])
	}

	if _, err := c.runner().ExecContext(ctx, "ALTER TABLE "+quote(s.Shadow)+" AUTO_INCREMENT = "+
		strconv.FormatUint(value, 10)); err != nil {
		return fmt.Errorf("carrying the AUTO_INCREMENT of %s to %s: %w", s.Table, s.Shadow, explainExec(err))
	}

	return nil
}

// DropShadow returns the statement dropping the shadow, which is nothing where it does not exist.
func (c *mysqlConn) DropShadow(s driver.Shadow) ([]driver.Statement, error) {
	return one("DROP TABLE IF EXISTS " + quote(s.Shadow)), nil
}

// DropRetired returns the statement dropping the retired original, which is nothing where it is gone.
func (c *mysqlConn) DropRetired(s driver.Shadow) ([]driver.Statement, error) {
	return one("DROP TABLE IF EXISTS " + quote(s.Retired)), nil
}

// compile-time proof that this session changes a table through a shadow.
var _ driver.Shadower = (*mysqlConn)(nil)
