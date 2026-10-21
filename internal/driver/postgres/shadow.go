package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// A shadow is created LIKE the table, without its indexes, which are created under derived names; it is
// filled a keyed chunk at a time and swapped in under one transaction (ADR 0094).
//
// Each chunk is INSERT ... SELECT ... FOR SHARE ON CONFLICT (key) DO NOTHING: a row a replay already
// wrote is kept, and FOR SHARE waits out a writer still holding the row -- measured on 17, a copy
// reaching a row whose delete had not committed waited for it and skipped the row, so a delete replayed
// first is never undone by a stale copy. A replay is an upsert for a row an insert or update left, an
// UPDATE alone for a change that left a TOASTed column out, and a DELETE by key, so applying a change
// twice is applying it once. A partial update that also moved its key is the row deleted at the old key
// and inserted at the new one, every column the change left out carried from the deleted row: where the
// shadow has no row at the old key yet it inserts nothing, and the copy brings the row; where the copy
// already brought it to the new key, the insert updates that row to the same values -- measured, a
// TOASTed note moved this way survives, where a delete and a partial update lost the row.
//
// Every value is bound as text and cast to the column's type without its
// modifier, so the column's own length or precision is checked on assignment rather than truncated.
//
// The cutover locks both tables ACCESS EXCLUSIVE under safety.lock_timeout, catches up under the lock
// within that bound, carries a serial's sequence across and sets an identity's, swaps the index names
// and the table names, and commits with the engine's record of it: one transaction, so a crash leaves
// either the swap and its record or neither. Measured on 17, a writer queued behind the lock -- a
// prepared statement included -- lands in the new table once it is released.

// copyGroup is how many changes of one kind one replay statement writes.
const copyGroup = 100

// Shadows reports why an operation cannot be performed through a shadow table on PostgreSQL.
func (c *pgConn) Shadows(op ir.Operation) error {
	switch op.(type) {
	case ir.AddColumn, ir.DropColumn, ir.AlterColumn, ir.AddIndex, ir.DropIndex, ir.AddCheck, ir.DropCheck,
		ir.DropUnique:
		return nil
	case ir.AddUnique:
		return errors.New("a unique constraint added through a shadow would keep the first of each " +
			"duplicate the copy meets and silently drop the rest, where adding it in place refuses them")
	case ir.RenameColumn:
		return errors.New("the copy matches columns by name, so a renamed column's values would not " +
			"reach the shadow")
	case ir.AddForeignKey, ir.DropForeignKey:
		return errors.New("a foreign key would follow the original to its retired name at the cutover")
	default:
		return fmt.Errorf("%s is not a change to a table's columns, indexes or checks", op.Kind())
	}
}

// Admits reports why this table cannot be changed through a shadow on this server, reading its catalog.
func (c *pgConn) Admits(ctx context.Context, s driver.Shadow) error {
	r, err := c.relationOf(ctx, s.Table)
	if err != nil {
		return err
	}

	problems := relationProblems(r)

	for _, read := range []struct {
		query, says string
	}{
		{foreignKeysQuery, "takes part in the foreign keys %s, which the cutover's rename would leave on the retired original"},
		{dependentsQuery, "is read by %s, which would go on reading the retired original after the cutover's rename"},
		{triggersQuery, "has the triggers %s, which CREATE TABLE ... LIKE does not copy and the rename would leave on the retired original"},
		{policiesQuery, "has the row-level security policies %s, which CREATE TABLE ... LIKE does not copy"},
	} {
		found, err := c.names(ctx, read.query, r.oid)
		if err != nil {
			return fmt.Errorf("reading whether %s can be changed through a shadow: %w", s.Table, err)
		}

		if len(found) > 0 {
			problems = append(problems, fmt.Sprintf(read.says, strings.Join(found, ", ")))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%s %s", s.Table, strings.Join(problems, ", and it "))
	}

	return nil
}

// relationProblems lists what about the table itself a shadow cannot carry.
func relationProblems(r shadowedRelation) []string {
	var problems []string

	if r.kind == relkindPartitioned || r.partition {
		problems = append(problems, "is partitioned or a partition, which CREATE TABLE ... LIKE cannot recreate")
	}

	if r.inherits {
		problems = append(problems, "takes part in table inheritance, which CREATE TABLE ... LIKE cannot recreate")
	}

	if r.secured {
		problems = append(problems, "has row-level security switched on, which CREATE TABLE ... LIKE does not copy")
	}

	if r.identity != "d" && r.identity != "f" {
		problems = append(problems, "has REPLICA IDENTITY "+describeIdentity(r.identity)+", so a publication "+
			"of it would either refuse its updates and deletes or not identify their rows by the primary key")
	}

	return problems
}

// describeIdentity spells a relreplident the way ALTER TABLE does.
func describeIdentity(identity string) string {
	switch identity {
	case "n":
		return "NOTHING"
	case "i":
		return "USING INDEX"
	default:
		return identity
	}
}

// CreateShadow returns the statements creating the shadow LIKE the table, its indexes, its grants, and the operations' changes.
func (c *pgConn) CreateShadow(ctx context.Context, s driver.Shadow) ([]driver.Statement, error) {
	r, err := c.relationOf(ctx, s.Table)
	if err != nil {
		return nil, err
	}

	stmts := one("CREATE TABLE " + quote(s.Shadow) + " (LIKE " + quote(s.Table) + " INCLUDING ALL EXCLUDING INDEXES)")

	indexes, err := c.shadowIndexes(ctx, r.oid, s)
	if err != nil {
		return nil, err
	}

	grants, err := c.shadowGrants(ctx, r.oid, s)
	if err != nil {
		return nil, err
	}

	stmts = append(append(stmts, indexes...), grants...)

	for _, op := range s.Ops {
		moved, err := retarget(op, s.Shadow)
		if err != nil {
			return nil, err
		}

		compiled, err := compile(moved)
		if err != nil {
			return nil, err
		}

		stmts = append(stmts, compiled...)
	}

	if r.owner != r.current {
		stmts = append(stmts, one("ALTER TABLE "+quote(s.Shadow)+" OWNER TO "+quote(r.owner))...)
	}

	return stmts, nil
}

// shadowIndexes returns the statements giving the shadow every index of the table, under the names derived for it.
func (c *pgConn) shadowIndexes(ctx context.Context, oid uint32, s driver.Shadow) ([]driver.Statement, error) {
	constraints, plain, err := c.indexesOf(ctx, oid)
	if err != nil {
		return nil, fmt.Errorf("reading the indexes of %s: %w", s.Table, err)
	}

	var stmts []driver.Statement

	for _, con := range constraints {
		stmts = append(stmts, one("ALTER TABLE "+quote(s.Shadow)+" ADD CONSTRAINT "+
			quote(newIndexName(con.name, s.Shadow))+" "+con.definition)...)
	}

	for _, index := range plain {
		body, ok := indexBody(index.definition)
		if !ok {
			return nil, fmt.Errorf("the index %s of %s is defined as %q, which this driver cannot copy",
				index.name, s.Table, index.definition)
		}

		unique := ""
		if index.unique {
			unique = "UNIQUE "
		}

		stmts = append(stmts, one("CREATE "+unique+"INDEX "+quote(newIndexName(index.name, s.Shadow))+
			" ON "+quote(s.Shadow)+body)...)
	}

	return stmts, nil
}

// shadowGrants returns the statements granting on the shadow every privilege the table grants, the column ones included.
func (c *pgConn) shadowGrants(ctx context.Context, oid uint32, s driver.Shadow) ([]driver.Statement, error) {
	rows, err := c.runner().Query(ctx, grantsQuery, oid)
	if err != nil {
		return nil, fmt.Errorf("reading the privileges %s grants: %w", s.Table, err)
	}

	stmts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (driver.Statement, error) {
		var (
			role, privilege string
			grantable       bool
		)

		if err := row.Scan(&role, &privilege, &grantable); err != nil {
			return driver.Statement{}, err
		}

		return one(grantOf(privilege, "", s.Shadow, role, grantable))[0], nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the privileges %s grants: %w", s.Table, err)
	}

	rows, err = c.runner().Query(ctx, columnGrantsQuery, oid)
	if err != nil {
		return nil, fmt.Errorf("reading the column privileges %s grants: %w", s.Table, err)
	}

	columns, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (driver.Statement, error) {
		var (
			column, role, privilege string
			grantable               bool
		)

		if err := row.Scan(&column, &role, &privilege, &grantable); err != nil {
			return driver.Statement{}, err
		}

		return one(grantOf(privilege, column, s.Shadow, role, grantable))[0], nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the column privileges %s grants: %w", s.Table, err)
	}

	return append(stmts, columns...), nil
}

// grantOf renders one privilege granted on a table, or on one of its columns.
func grantOf(privilege, column, table, role string, grantable bool) string {
	on := privilege
	if column != "" {
		on += " (" + quote(column) + ")"
	}

	grant := "GRANT " + on + " ON TABLE " + quote(table) + " TO " + grantee(role)
	if grantable {
		grant += " WITH GRANT OPTION"
	}

	return grant
}

// retarget returns an operation naming the shadow where it named the table, and an index by its name on the shadow.
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
		o.Table, o.Name = shadow, newIndexName(o.Name, shadow)
		return o, nil
	case ir.AddCheck:
		o.Table = shadow
		return o, nil
	case ir.DropCheck:
		o.Table = shadow
		return o, nil
	case ir.DropUnique:
		o.Table, o.Name = shadow, newIndexName(o.Name, shadow)
		return o, nil
	default:
		return nil, fmt.Errorf("%s is not an operation a shadow carries on PostgreSQL", op.Kind())
	}
}

// shadowPair is what a copy and a replay know about a table and its shadow: the columns both write, and their types.
type shadowPair struct {
	// shared are the table's written columns the shadow also writes, in the table's order.
	shared []string
	// types are the shadow's written columns' types, by name.
	types map[string]string
}

// pairOf reads the columns a table and its shadow both write.
func (c *pgConn) pairOf(ctx context.Context, s driver.Shadow) (shadowPair, error) {
	source, err := c.writtenColumnsOf(ctx, s.Table)
	if err != nil {
		return shadowPair{}, err
	}

	target, err := c.writtenColumnsOf(ctx, s.Shadow)
	if err != nil {
		return shadowPair{}, err
	}

	p := shadowPair{types: map[string]string{}}
	for _, column := range target {
		p.types[column.name] = column.typ
	}

	for _, column := range source {
		if _, writes := p.types[column.name]; writes {
			p.shared = append(p.shared, column.name)
		}
	}

	return p, nil
}

// CopyChunk copies the rows after a key into the shadow inside the open transaction, keeping any already there.
func (c *pgConn) CopyChunk(ctx context.Context, s driver.Shadow, after []string, rows int) (out driver.Sliced, err error) {
	defer func() { err = stoppedBy(ctx, err) }()

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

	cols := quoteAll(p.shared)
	shape := chunkShape{table: s.Table, head: "INSERT INTO " + quote(s.Shadow) + " (" + cols +
		") OVERRIDING SYSTEM VALUE SELECT " + cols + " FROM " + quote(s.Table)}

	bound, found, err := c.copyBound(ctx, shape, s, after, rows)
	if err != nil {
		return driver.Sliced{}, err
	}

	statement := shape.write(s.Key, len(after) > 0, found) + " ORDER BY " + qualifiedKey(s.Table, s.Key) +
		" FOR SHARE ON CONFLICT (" + quoteAll(s.Key) + ") DO NOTHING"

	args := textArgs(after)
	if found {
		args = append(args, textArgs(bound)...)
	}

	tag, err := c.runner().Exec(ctx, statement, args...)
	if err != nil {
		return driver.Sliced{}, fmt.Errorf("copying rows of %s into %s: %w", s.Table, s.Shadow, classifyExec(err))
	}

	if !found {
		return driver.Sliced{Written: tag.RowsAffected(), Last: after, Done: true}, nil
	}

	return driver.Sliced{Written: tag.RowsAffected(), Last: bound}, nil
}

// copyBound reads the key of the last row a copy chunk covers, and reports none when fewer rows remain.
func (c *pgConn) copyBound(ctx context.Context, shape chunkShape, s driver.Shadow, after []string,
	rows int) ([]string, bool, error) {
	values := make([]*string, len(s.Key))
	targets := make([]any, len(values))

	for i := range values {
		targets[i] = &values[i]
	}

	err := c.runner().QueryRow(ctx, shape.boundQuery(s.Key, len(after) > 0), append(textArgs(after), rows-1)...).
		Scan(targets...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("reading where a copy chunk of %s ends: %w", s.Table, err)
	}

	return keyValues(driver.Slice{Op: ir.DataCopy{From: s.Table}, Key: s.Key}, values)
}

// ApplyChanges writes captured changes to the shadow inside the open transaction, each by its key.
func (c *pgConn) ApplyChanges(ctx context.Context, s driver.Shadow, changes []capture.Change) (err error) {
	defer func() { err = stoppedBy(ctx, err) }()

	p, err := c.pairOf(ctx, s)
	if err != nil {
		return err
	}

	for start := 0; start < len(changes); {
		end := start + 1
		for end < len(changes) && replayKind(changes[end]) == replayKind(changes[start]) && end-start < copyGroup {
			end++
		}

		if err := c.applyRun(ctx, s, p, changes[start:end]); err != nil {
			return err
		}

		start = end
	}

	return nil
}

// replayKind is how a change is written: an upsert, an update of the columns it carries, or a delete.
func replayKind(change capture.Change) string {
	switch {
	case change.Kind == capture.Delete:
		return "delete"
	case change.Partial && change.From != nil:
		return "move"
	case change.Partial:
		return "partial"
	default:
		return "upsert"
	}
}

// applyRun writes a run of changes of one kind.
func (c *pgConn) applyRun(ctx context.Context, s driver.Shadow, p shadowPair, run []capture.Change) error {
	switch replayKind(run[0]) {
	case "delete":
		return c.replay(ctx, s, run, func() (string, []any, error) { return deleteStatement(s, p, run) })
	case "partial", "move":
		for _, change := range run {
			if err := c.replay(ctx, s, run, func() (string, []any, error) {
				if change.From != nil {
					return moveStatement(s, p, change)
				}

				return partialStatement(s, p, change)
			}); err != nil {
				return err
			}
		}

		return nil
	default:
		return c.replay(ctx, s, run, func() (string, []any, error) { return upsertStatement(s, p, latest(s, run)) })
	}
}

// replay renders one statement and runs it, naming the shadow where it fails.
func (c *pgConn) replay(ctx context.Context, s driver.Shadow, run []capture.Change,
	render func() (string, []any, error)) error {
	statement, args, err := render()
	if err != nil || statement == "" {
		return err
	}

	if _, err := c.runner().Exec(ctx, statement, args...); err != nil {
		return fmt.Errorf("replaying %d captured changes into %s: %w", len(run), s.Shadow, classifyExec(err))
	}

	return nil
}

// latest keeps each key's last row of a run, since one upsert may not change a row twice.
func latest(s driver.Shadow, run []capture.Change) []capture.Change {
	last := map[string]int{}
	order := []string{}

	for i, change := range run {
		key := keyText(s, change)
		if _, seen := last[key]; !seen {
			order = append(order, key)
		}

		last[key] = i
	}

	kept := make([]capture.Change, 0, len(order))
	for _, key := range order {
		kept = append(kept, run[last[key]])
	}

	return kept
}

// keyText renders a change's key as one string, which only tells two keys apart.
func keyText(s driver.Shadow, change capture.Change) string {
	parts := make([]string, len(s.Key))

	for i, column := range s.Key {
		if at := indexOf(change.Columns, column); at >= 0 && change.Row[at] != nil {
			parts[i] = strconv.Quote(*change.Row[at])
		}
	}

	return strings.Join(parts, ",")
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

// binder numbers a statement's placeholders, each cast from text to its column's type.
type binder struct {
	// args are the values bound so far.
	args []any
}

// bind adds a value for a column and returns its placeholder, cast to the column's type.
func (b *binder) bind(p shadowPair, column string, value *string) string {
	b.args = append(b.args, value)

	return "$" + strconv.Itoa(len(b.args)) + "::text::" + p.types[column]
}

// upsertStatement renders rows as one insert that updates each key it meets.
func upsertStatement(s driver.Shadow, p shadowPair, run []capture.Change) (string, []any, error) {
	columns := writtenOf(p, run[0].Columns)

	var (
		b    binder
		rows []string
	)

	for _, change := range run {
		values := make([]string, len(columns))

		for i, column := range columns {
			at := indexOf(change.Columns, column)
			if at < 0 {
				return "", nil, fmt.Errorf("a captured change to %s carries no value for %s", change.Table, column)
			}

			values[i] = b.bind(p, column, change.Row[at])
		}

		rows = append(rows, "("+strings.Join(values, ", ")+")")
	}

	return "INSERT INTO " + quote(s.Shadow) + " (" + quoteAll(columns) + ") OVERRIDING SYSTEM VALUE VALUES " +
		strings.Join(rows, ", ") + " ON CONFLICT (" + quoteAll(s.Key) + ") " + onConflict(s, columns), b.args, nil
}

// onConflict sets every column a row carries beyond its key, or does nothing where it carries only the key.
func onConflict(s driver.Shadow, columns []string) string {
	var set []string

	for _, column := range columns {
		if indexOf(s.Key, column) < 0 {
			set = append(set, quote(column)+" = EXCLUDED."+quote(column))
		}
	}

	if len(set) == 0 {
		return "DO NOTHING"
	}

	return "DO UPDATE SET " + strings.Join(set, ", ")
}

// partialStatement renders one change that left columns out as an update of the row it names, inserting none.
func partialStatement(s driver.Shadow, p shadowPair, change capture.Change) (string, []any, error) {
	var (
		b   binder
		set []string
	)

	for _, column := range writtenOf(p, change.Columns) {
		if indexOf(s.Key, column) < 0 {
			set = append(set, quote(column)+" = "+b.bind(p, column, change.Row[indexOf(change.Columns, column)]))
		}
	}

	if len(set) == 0 {
		return "", nil, nil
	}

	where, err := keyMatch(s, p, &b, change)
	if err != nil {
		return "", nil, err
	}

	return "UPDATE " + quote(s.Shadow) + " SET " + strings.Join(set, ", ") + " WHERE " + where, b.args, nil
}

// moveStatement renders a partial update that moved its row's key as the shadow's row taken from the old key to the new.
func moveStatement(s driver.Shadow, p shadowPair, change capture.Change) (string, []any, error) {
	var b binder

	where, err := keyMatch(s, p, &b, *change.From)
	if err != nil {
		return "", nil, err
	}

	columns := make([]string, 0, len(p.types))
	values := make([]string, 0, len(p.types))

	for _, column := range p.shared {
		columns = append(columns, column)

		if at := indexOf(change.Columns, column); at >= 0 {
			values = append(values, b.bind(p, column, change.Row[at]))
		} else {
			values = append(values, "old."+quote(column))
		}
	}

	return "WITH old AS (DELETE FROM " + quote(s.Shadow) + " WHERE " + where + " RETURNING *) INSERT INTO " +
		quote(s.Shadow) + " (" + quoteAll(columns) + ") OVERRIDING SYSTEM VALUE SELECT " + strings.Join(values, ", ") +
		" FROM old ON CONFLICT (" + quoteAll(s.Key) + ") " + onConflict(s, columns), b.args, nil
}

// deleteStatement renders a run of removed keys as one delete.
func deleteStatement(s driver.Shadow, p shadowPair, run []capture.Change) (string, []any, error) {
	var (
		b       binder
		matches []string
	)

	for _, change := range run {
		where, err := keyMatch(s, p, &b, change)
		if err != nil {
			return "", nil, err
		}

		matches = append(matches, "("+where+")")
	}

	return "DELETE FROM " + quote(s.Shadow) + " WHERE " + strings.Join(matches, " OR "), b.args, nil
}

// keyMatch renders the condition matching a change's row by its key.
func keyMatch(s driver.Shadow, p shadowPair, b *binder, change capture.Change) (string, error) {
	conditions := make([]string, len(s.Key))

	for i, column := range s.Key {
		at := indexOf(change.Columns, column)
		if at < 0 || change.Row[at] == nil {
			return "", fmt.Errorf("a captured change to %s carries no value for its key column %s", change.Table, column)
		}

		conditions[i] = quote(column) + " = " + b.bind(p, column, change.Row[at])
	}

	return strings.Join(conditions, " AND "), nil
}

// writtenOf keeps the columns a change carries that the shadow writes, in the change's order.
func writtenOf(p shadowPair, columns []string) []string {
	var kept []string

	for _, column := range columns {
		if _, writes := p.types[column]; writes {
			kept = append(kept, column)
		}
	}

	return kept
}

// Cutover locks the table and the shadow, catches up under the lock, and swaps the two with every name the table carried.
func (c *pgConn) Cutover(ctx context.Context, s driver.Shadow, catchUp driver.CatchUp) (err error) {
	defer func() { err = stoppedBy(ctx, err) }()

	if c.tx == nil {
		return fmt.Errorf("%w: a cutover swaps the tables in the transaction its record is written in, "+
			"and this session has none open", ErrTransaction)
	}

	if _, err := c.runner().Exec(ctx, "LOCK TABLE "+quote(s.Table)+", "+quote(s.Shadow)+
		" IN ACCESS EXCLUSIVE MODE"); err != nil {
		return fmt.Errorf("locking %s for the cutover: %w", s.Table, classifyExec(err))
	}

	if err := c.catchUpBounded(ctx, catchUp); err != nil {
		return err
	}

	swaps, err := c.swaps(ctx, s)
	if err != nil {
		return err
	}

	for _, stmt := range swaps {
		if _, err := c.runner().Exec(ctx, stmt); err != nil {
			return fmt.Errorf("swapping %s in for %s: %w", s.Shadow, s.Table, classifyExec(err))
		}
	}

	return nil
}

// catchUpBounded runs the catch-up for at most safety.lock_timeout, since every writer waits on it.
func (c *pgConn) catchUpBounded(ctx context.Context, catchUp driver.CatchUp) error {
	wait := c.statementWait
	if wait <= 0 {
		wait = driver.DefaultStatementLockWait
	}

	bounded, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	err := catchUp(bounded)
	if err != nil && ctx.Err() == nil && errors.Is(bounded.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: catching the shadow up under the cutover's lock took longer than "+
			"safety.lock_timeout (%s), so the cutover was rolled back for writers to go on: %w",
			driver.ErrStatementLockTimeout, wait, err)
	}

	return err
}

// swaps returns the statements that carry the sequences across, swap every index's name, and swap the tables.
func (c *pgConn) swaps(ctx context.Context, s driver.Shadow) ([]string, error) {
	r, err := c.relationOf(ctx, s.Table)
	if err != nil {
		return nil, err
	}

	sequences, err := c.sequenceSwaps(ctx, r.oid, s)
	if err != nil {
		return nil, err
	}

	names, err := c.nameSwaps(ctx, r.oid, s)
	if err != nil {
		return nil, err
	}

	return append(append(sequences, names...),
		"ALTER TABLE "+quote(s.Table)+" RENAME TO "+quote(s.Retired),
		"ALTER TABLE "+quote(s.Shadow)+" RENAME TO "+quote(s.Table)), nil
}

// sequenceSwaps gives the shadow the sequences its serial columns draw from, and its identities the original's next value.
func (c *pgConn) sequenceSwaps(ctx context.Context, oid uint32, s driver.Shadow) ([]string, error) {
	p, err := c.pairOf(ctx, s)
	if err != nil {
		return nil, err
	}

	owned, err := c.pairs(ctx, ownedSequencesQuery, oid)
	if err != nil {
		return nil, fmt.Errorf("reading the sequences %s's columns own: %w", s.Table, err)
	}

	var stmts []string

	for _, o := range owned {
		if _, kept := p.types[o[1]]; kept {
			stmts = append(stmts, "ALTER SEQUENCE "+o[0]+" OWNED BY "+quote(s.Shadow)+"."+quote(o[1]))
		}
	}

	identities, err := c.names(ctx, identityColumnsQuery, oid)
	if err != nil {
		return nil, fmt.Errorf("reading the identity columns of %s: %w", s.Table, err)
	}

	for _, column := range identities {
		set, err := c.identitySwap(ctx, s, column)
		if err != nil {
			return nil, err
		}

		stmts = append(stmts, set...)
	}

	return stmts, nil
}

// identitySwap sets the shadow's identity sequence for a column to where the original's stands.
func (c *pgConn) identitySwap(ctx context.Context, s driver.Shadow, column string) ([]string, error) {
	var from, to *string

	if err := c.runner().QueryRow(ctx, "SELECT pg_get_serial_sequence(quote_ident($1), $3), "+
		"pg_get_serial_sequence(quote_ident($2), $3)", s.Table, s.Shadow, column).Scan(&from, &to); err != nil {
		return nil, fmt.Errorf("reading the identity sequence of %s.%s: %w", s.Table, column, err)
	}

	if from == nil || to == nil {
		return nil, nil
	}

	var (
		last   int64
		called bool
	)

	if err := c.runner().QueryRow(ctx, "SELECT last_value, is_called FROM "+*from).Scan(&last, &called); err != nil {
		return nil, fmt.Errorf("reading where the identity sequence %s stands: %w", *from, err)
	}

	return []string{"SELECT setval(" + quoteLiteral(*to) + ", " + strconv.FormatInt(last, 10) + ", " +
		strconv.FormatBool(called) + ")"}, nil
}

// nameSwaps renames the original's indexes to retired names, then the shadow's copies of them to the original names.
func (c *pgConn) nameSwaps(ctx context.Context, oid uint32, s driver.Shadow) ([]string, error) {
	constraints, plain, err := c.indexesOf(ctx, oid)
	if err != nil {
		return nil, fmt.Errorf("reading the indexes of %s: %w", s.Table, err)
	}

	shadowOID, err := c.relationOf(ctx, s.Shadow)
	if err != nil {
		return nil, err
	}

	held, heldPlain, err := c.indexesOf(ctx, shadowOID.oid)
	if err != nil {
		return nil, fmt.Errorf("reading the indexes of %s: %w", s.Shadow, err)
	}

	on := map[string]bool{}
	for _, con := range held {
		on[con.name] = true
	}

	for _, index := range heldPlain {
		on[index.name] = true
	}

	var retire, adopt []string

	for _, con := range constraints {
		retire = append(retire, "ALTER TABLE "+quote(s.Table)+" RENAME CONSTRAINT "+quote(con.name)+" TO "+
			quote(oldIndexName(con.name, s.Shadow)))

		if copied := newIndexName(con.name, s.Shadow); on[copied] {
			adopt = append(adopt, "ALTER TABLE "+quote(s.Shadow)+" RENAME CONSTRAINT "+quote(copied)+" TO "+
				quote(con.name))
		}
	}

	for _, index := range plain {
		retire = append(retire, "ALTER INDEX "+quote(index.name)+" RENAME TO "+quote(oldIndexName(index.name, s.Shadow)))

		if copied := newIndexName(index.name, s.Shadow); on[copied] {
			adopt = append(adopt, "ALTER INDEX "+quote(copied)+" RENAME TO "+quote(index.name))
		}
	}

	return append(retire, adopt...), nil
}

// DropShadow returns the statement dropping the shadow, which is nothing where it does not exist.
func (c *pgConn) DropShadow(s driver.Shadow) ([]driver.Statement, error) {
	return one("DROP TABLE IF EXISTS " + quote(s.Shadow)), nil
}

// DropRetired returns the statement dropping the retired original, which is nothing where it is gone.
func (c *pgConn) DropRetired(s driver.Shadow) ([]driver.Statement, error) {
	return one("DROP TABLE IF EXISTS " + quote(s.Retired)), nil
}

// compile-time proof that this session changes a table through a shadow.
var _ driver.Shadower = (*pgConn)(nil)
