package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// A statement outside any transaction is rolled back by nothing, so what it leaves is repaired here.
//
// Measured on 16 and 17: a concurrent build stopped by lock_timeout, its statement bound, a cancel or a
// duplicate leaves its index INVALID, which every write maintains and no query uses; a concurrent drop
// stopped by lock_timeout leaves the index in place and INVALID, half dropped; and REINDEX INDEX
// CONCURRENTLY over an INVALID index rebuilds it valid where its rows allow, and where they do not
// leaves a second INVALID index, <name>_ccnew, beside the first. So a repair after a failure drops a
// build's leftover and lets a drop run again, and a repair before a resume -- which may follow a killed
// run that repaired nothing -- rebuilds a leftover by REINDEX where it is exactly the index the
// statement builds, and drops anything else. A VALIDATE CONSTRAINT that failed leaves the constraint
// NOT VALID, which validating again finishes.

// The things a concurrent statement can be doing to one index, read from its text.
const (
	targetNone = iota
	targetBuild
	targetDrop
	targetValidate
)

// undefinedObject is the SQLSTATE PostgreSQL raises for a DROP INDEX of an index that is not there.
const undefinedObject = "42704"

// concurrentDrop reads the index a DROP INDEX CONCURRENTLY names, quoted or not.
var concurrentDrop = regexp.MustCompile(`(?is)^\s*DROP\s+INDEX\s+CONCURRENTLY\s+(?:IF\s+EXISTS\s+)?` +
	`("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)`)

// validateConstraint recognizes a VALIDATE CONSTRAINT, which leaves nothing a rerun cannot finish.
var validateConstraint = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+.+\s+VALIDATE\s+CONSTRAINT\s+`)

// indexStateQuery reads an index's validity and shape: its table, whether it is unique, whether it covers only columns, and those columns in order.
const indexStateQuery = `SELECT x.indisvalid, t.relname, x.indisunique,
       x.indpred IS NULL AND x.indexprs IS NULL,
       coalesce(array_agg(a.attname ORDER BY s) FILTER (WHERE a.attname IS NOT NULL), '{}')
  FROM pg_index x
  JOIN pg_class i ON i.oid = x.indexrelid
  JOIN pg_class t ON t.oid = x.indrelid
  CROSS JOIN LATERAL generate_subscripts(x.indkey, 1) AS s
  LEFT JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = x.indkey[s]
 WHERE i.relname = $1 AND i.relnamespace = current_schema()::regnamespace
 GROUP BY x.indisvalid, t.relname, x.indisunique, x.indpred, x.indexprs`

// rebuiltQuery lists the INVALID indexes a failed REINDEX CONCURRENTLY of one index left beside it.
const rebuiltQuery = `SELECT i.relname FROM pg_index x JOIN pg_class i ON i.oid = x.indexrelid
 WHERE NOT x.indisvalid AND i.relnamespace = current_schema()::regnamespace
   AND i.relname LIKE $1 || '\_ccnew%' ORDER BY i.relname`

// invalidIndexesQuery lists every INVALID index in this namespace, less those on migr's own tables.
const invalidIndexesQuery = `SELECT t.relname, i.relname, pg_get_indexdef(x.indexrelid)
  FROM pg_index x
  JOIN pg_class i ON i.oid = x.indexrelid
  JOIN pg_class t ON t.oid = x.indrelid
 WHERE NOT x.indisvalid AND i.relnamespace = current_schema()::regnamespace
   AND t.relname <> ALL($1)
 ORDER BY t.relname, i.relname`

// leftover is what one index stands as after a statement that touched it stopped.
type leftover struct {
	// present reports that the index exists.
	present bool
	// valid reports that PostgreSQL lets queries use it.
	valid bool
	// matches reports that it is exactly the index the operation builds: its table, its columns in order, its uniqueness.
	matches bool
}

// fix is what a repair does about one leftover.
type fix int

// The repairs there are, from leaving everything alone to rebuilding an index in place.
const (
	fixNothing fix = iota
	fixRerun
	fixDone
	fixDrop
	fixReindex
)

// decide is what to do about what a statement left, by what it was doing, when, whether it collided with the leftover, and on what server.
func decide(target int, when driver.RepairTime, left leftover, collided, reindexes bool) fix {
	switch target {
	case targetValidate:
		return fixRerun
	case targetDrop:
		if !left.present && (when == driver.BeforeResume || collided) {
			return fixDone
		}

		return fixRerun
	case targetBuild:
		return decideBuild(when, left, collided, reindexes)
	default:
		return fixNothing
	}
}

// decideBuild is what to do about the index a concurrent build left, where a valid one counts only as exactly the index built.
func decideBuild(when driver.RepairTime, left leftover, collided, reindexes bool) fix {
	settled := when == driver.BeforeResume || collided

	switch {
	case !left.present:
		return fixRerun
	case left.valid && left.matches && settled:
		return fixDone
	case left.valid:
		return fixNothing
	case when == driver.BeforeResume && left.matches && reindexes:
		return fixReindex
	default:
		return fixDrop
	}
}

// collidedWith reports a failure caused by the very object the statement creates or drops already being, or no longer being, there.
func collidedWith(cause error) bool {
	var pgErr *pgconn.PgError

	return errors.As(cause, &pgErr) && (pgErr.Code == duplicateTable || pgErr.Code == undefinedObject)
}

// Repair inspects and cleans up what a concurrent statement left behind, reporting what it ran.
func (c *pgConn) Repair(ctx context.Context, op ir.Operation, stmt driver.Statement, when driver.RepairTime,
	cause error,
) (driver.Repaired, error) {
	target, name := targetOf(stmt.SQL)
	if target == targetNone || c.tx != nil {
		return driver.Repaired{}, nil
	}

	left, err := c.leftoverOf(ctx, name, op)
	if err != nil {
		return driver.Repaired{}, err
	}

	collided := collidedWith(cause)

	switch decide(target, when, left, collided, c.version.AtLeast(checkedNotNullRelease)) {
	case fixRerun:
		return driver.Repaired{Rerunnable: true}, nil
	case fixDone:
		return driver.Repaired{Action: fmt.Sprintf("found %s already %s", quote(name), doneWord(target)),
			Done: true}, nil
	case fixDrop:
		repaired, err := c.dropLeftover(ctx, name)
		repaired.Retry = collided

		return repaired, err
	case fixReindex:
		return c.reindexLeftover(ctx, name)
	default:
		return driver.Repaired{}, nil
	}
}

// doneWord says what a statement found complete had done.
func doneWord(target int) string {
	if target == targetDrop {
		return "dropped"
	}

	return "built and valid"
}

// targetOf reads which index a concurrent statement touches and how, and none for any other statement.
func targetOf(sql string) (int, string) {
	if name, ok := builtIndex(sql); ok {
		return targetBuild, name
	}

	if found := concurrentDrop.FindStringSubmatch(sql); found != nil {
		return targetDrop, unquoteName(found[1])
	}

	if validateConstraint.MatchString(sql) {
		return targetValidate, ""
	}

	return targetNone, ""
}

// unquoteName reads an identifier back as PostgreSQL stores it.
func unquoteName(name string) string {
	if strings.HasPrefix(name, `"`) {
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
	}

	return strings.ToLower(name)
}

// leftoverOf reads what stands of an index, and whether it is the one an operation builds.
func (c *pgConn) leftoverOf(ctx context.Context, name string, op ir.Operation) (leftover, error) {
	if name == "" {
		return leftover{}, nil
	}

	var (
		left             leftover
		table            string
		unique, onlyCols bool
		columns          []string
	)

	err := c.conn.QueryRow(ctx, indexStateQuery, name).Scan(&left.valid, &table, &unique, &onlyCols, &columns)
	if errors.Is(err, pgx.ErrNoRows) {
		return leftover{}, nil
	}

	if err != nil {
		return leftover{}, fmt.Errorf("reading what is left of the index %s: %w", quote(name), err)
	}

	left.present = true
	wantTable, wantColumns, wantUnique := builds(op)
	left.matches = onlyCols && table == wantTable && unique == wantUnique && slices.Equal(columns, wantColumns)

	return left, nil
}

// builds reads the table, columns and uniqueness of the index an operation builds.
func builds(op ir.Operation) (string, []string, bool) {
	switch o := op.(type) {
	case ir.AddIndex:
		return o.Table, o.Columns, false
	case ir.AddUnique:
		return o.Table, o.Columns, true
	case ir.AddPrimaryKey:
		return o.Table, o.Columns, true
	default:
		return "", nil, false
	}
}

// dropLeftover drops an INVALID index a build left, so the build can be sent again.
func (c *pgConn) dropLeftover(ctx context.Context, name string) (driver.Repaired, error) {
	if err := c.dropConcurrently(ctx, name); err != nil {
		return driver.Repaired{}, err
	}

	return driver.Repaired{Action: fmt.Sprintf("dropped the INVALID index %s it left behind", quote(name)),
		Rerunnable: true}, nil
}

// dropConcurrently drops one index outside any transaction, which is nothing where it is already gone.
func (c *pgConn) dropConcurrently(ctx context.Context, name string) error {
	if _, err := c.conn.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+quote(name)+terminator); err != nil {
		return fmt.Errorf("dropping the INVALID index %s: %w", quote(name), classifyExec(err))
	}

	return nil
}

// reindexLeftover rebuilds an INVALID index that is exactly what the statement builds, and drops what a failed rebuild leaves.
func (c *pgConn) reindexLeftover(ctx context.Context, name string) (driver.Repaired, error) {
	_, err := c.conn.Exec(ctx, "REINDEX INDEX CONCURRENTLY "+quote(name)+terminator)
	if err == nil {
		return driver.Repaired{Action: fmt.Sprintf("rebuilt the INVALID index %s a stopped run left, with "+
			"REINDEX INDEX CONCURRENTLY", quote(name)), Done: true}, nil
	}

	failed := fmt.Errorf("rebuilding the INVALID index %s with REINDEX INDEX CONCURRENTLY: %w", quote(name),
		classifyExec(err))

	return driver.Repaired{}, errors.Join(failed, c.dropRebuilt(context.WithoutCancel(ctx), name))
}

// dropRebuilt drops an index and every INVALID copy a failed REINDEX CONCURRENTLY of it left.
func (c *pgConn) dropRebuilt(ctx context.Context, name string) error {
	rows, err := c.conn.Query(ctx, rebuiltQuery, name)
	if err != nil {
		return fmt.Errorf("reading what a failed rebuild of %s left: %w", quote(name), err)
	}

	copies, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("reading what a failed rebuild of %s left: %w", quote(name), err)
	}

	var problems []error

	for _, index := range append(copies, name) {
		problems = append(problems, c.dropConcurrently(ctx, index))
	}

	return errors.Join(problems...)
}

// InvalidIndexes lists every INVALID index in this session's namespace, less those on migr's own tables.
func (c *pgConn) InvalidIndexes(ctx context.Context) ([]driver.InvalidIndex, error) {
	var owned []string
	for _, name := range c.owned() {
		owned = append(owned, fmt.Sprint(name))
	}

	rows, err := c.runner().Query(ctx, invalidIndexesQuery, owned)
	if err != nil {
		return nil, fmt.Errorf("reading the INVALID indexes: %w", err)
	}

	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (driver.InvalidIndex, error) {
		var one driver.InvalidIndex
		err := row.Scan(&one.Table, &one.Index, &one.Definition)

		return one, err
	})
	if err != nil {
		return nil, fmt.Errorf("reading the INVALID indexes: %w", err)
	}

	return found, nil
}

// compile-time proof that this session repairs what a concurrent statement left and reports INVALID indexes.
var (
	_ driver.Repairer           = (*pgConn)(nil)
	_ driver.InvalidIndexReader = (*pgConn)(nil)
)
