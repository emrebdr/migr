package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// The reads one snapshot is built from, each over the database this session's DSN named.
//
// DATABASE() rather than a stored name, since that is the one namespace a session has. The bound
// parameter is the table migr records its own runs in, folded the way the catalog holds it -- a
// differ would otherwise propose dropping it.
const (
	tableQuery = "SELECT TABLE_NAME, TABLE_TYPE, coalesce(TABLE_COLLATION, '') " +
		"FROM information_schema.TABLES " +
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME <> ? ORDER BY TABLE_NAME"

	viewQuery = "SELECT TABLE_NAME, VIEW_DEFINITION FROM information_schema.VIEWS " +
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME <> ?"

	columnsQuery = "SELECT c.TABLE_NAME, c.COLUMN_NAME, c.COLUMN_TYPE, c.IS_NULLABLE, " +
		"c.COLUMN_DEFAULT, c.EXTRA, coalesce(c.COLLATION_NAME, '') " +
		"FROM information_schema.COLUMNS c " +
		"JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA " +
		"AND t.TABLE_NAME = c.TABLE_NAME " +
		"WHERE c.TABLE_SCHEMA = DATABASE() AND t.TABLE_TYPE = 'BASE TABLE' AND c.TABLE_NAME <> ? " +
		"ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION"

	keysQuery = "SELECT k.TABLE_NAME, k.CONSTRAINT_NAME, t.CONSTRAINT_TYPE, k.COLUMN_NAME, " +
		"coalesce(k.REFERENCED_TABLE_NAME, ''), coalesce(k.REFERENCED_COLUMN_NAME, ''), " +
		"coalesce(r.DELETE_RULE, ''), coalesce(r.UPDATE_RULE, '') " +
		"FROM information_schema.KEY_COLUMN_USAGE k " +
		"JOIN information_schema.TABLE_CONSTRAINTS t ON t.TABLE_SCHEMA = k.TABLE_SCHEMA " +
		"AND t.TABLE_NAME = k.TABLE_NAME AND t.CONSTRAINT_NAME = k.CONSTRAINT_NAME " +
		"LEFT JOIN information_schema.REFERENTIAL_CONSTRAINTS r " +
		"ON r.CONSTRAINT_SCHEMA = k.TABLE_SCHEMA AND r.TABLE_NAME = k.TABLE_NAME " +
		"AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME " +
		"WHERE k.TABLE_SCHEMA = DATABASE() AND k.TABLE_NAME <> ? " +
		"ORDER BY k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION"

	indexesQuery = "SELECT TABLE_NAME, INDEX_NAME, coalesce(COLUMN_NAME, ''), NON_UNIQUE, " +
		"coalesce(COLLATION, ''), coalesce(SUB_PART, 0) " +
		"FROM information_schema.STATISTICS " +
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME <> ? " +
		"ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX"
)

// The two spellings of the check probe, because only one variant records what table a check is on.
//
// information_schema.CHECK_CONSTRAINTS has no TABLE_NAME column on MySQL, so the table comes from
// TABLE_CONSTRAINTS there; MariaDB has it and has no TABLE_CONSTRAINTS.ENFORCED. Measured on 8.0
// and 11.4 -- the other spelling is error 1054 on each.
const (
	mysqlChecksQuery = "SELECT t.TABLE_NAME, c.CONSTRAINT_NAME, c.CHECK_CLAUSE, t.ENFORCED " +
		"FROM information_schema.CHECK_CONSTRAINTS c " +
		"JOIN information_schema.TABLE_CONSTRAINTS t " +
		"ON t.CONSTRAINT_SCHEMA = c.CONSTRAINT_SCHEMA AND t.CONSTRAINT_NAME = c.CONSTRAINT_NAME " +
		"WHERE c.CONSTRAINT_SCHEMA = DATABASE() AND t.TABLE_NAME <> ? " +
		"ORDER BY t.TABLE_NAME, c.CONSTRAINT_NAME"

	mariaChecksQuery = "SELECT TABLE_NAME, CONSTRAINT_NAME, CHECK_CLAUSE, 'YES' " +
		"FROM information_schema.CHECK_CONSTRAINTS " +
		"WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME <> ? " +
		"ORDER BY TABLE_NAME, CONSTRAINT_NAME"
)

// The catalog's own words for what it answers, each of which decides a branch.
const (
	baseTable        = "BASE TABLE"
	primaryIndex     = "PRIMARY"
	constraintUnique = "UNIQUE"
	constraintKey    = "FOREIGN KEY"
	descendingOrder  = "D"
	notEnforced      = "NO"
)

// Introspect reads this session's own database back, less the table migr records its own runs in.
func (c *mysqlConn) Introspect(ctx context.Context) (*schema.Snapshot, error) {
	read, err := c.readCatalog(ctx)
	if err != nil {
		return nil, err
	}

	snapshot := read.assemble()

	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("%w: this driver read a snapshot it cannot stand behind: %w",
			ErrIntrospect, err)
	}

	return snapshot, nil
}

// catalog is everything the probes answered, keyed the way the assembly needs it.
type catalog struct {
	// tables are the base tables and views this database holds, in the order names sort.
	tables []tableRow
	// views are the bodies, keyed on the view that has one.
	views map[string]string
	// columns are each table's columns, in declaration order.
	columns map[string][]schema.Column
	// keys are each table's primary key, unique constraints and foreign keys.
	keys map[string]*constrained
	// checks are each table's check constraints, less any absorbed into a column's type.
	checks map[string][]schema.Check
	// indexes are each table's indexes, less the ones a constraint already stands for.
	indexes map[string]*carried
}

// tableRow is one row of the relation probe, which is a base table or a view.
type tableRow struct {
	// name is the object's own name, as the catalog holds it.
	name string
	// kind is what the catalog calls it: a base table or a view.
	kind string
	// collation is the table's own default, which is what a column's own is compared against.
	collation string
}

// readCatalog runs every probe this snapshot is built from, in the order the assembly needs them.
func (c *mysqlConn) readCatalog(ctx context.Context) (*catalog, error) {
	read := &catalog{}

	var err error

	if read.tables, err = c.readTables(ctx); err != nil {
		return nil, err
	}

	if read.views, err = c.readViews(ctx); err != nil {
		return nil, err
	}

	absorbed, checks, err := c.readChecks(ctx)
	if err != nil {
		return nil, err
	}

	read.checks = checks

	if read.columns, err = c.readColumns(ctx, read.tables, absorbed); err != nil {
		return nil, err
	}

	if read.keys, err = c.readKeys(ctx); err != nil {
		return nil, err
	}

	read.indexes, err = c.readIndexes(ctx, read.keys)

	return read, err
}

// assemble joins what the probes answered into one snapshot, in the order names sort.
func (r *catalog) assemble() *schema.Snapshot {
	snapshot := &schema.Snapshot{}

	for _, found := range r.tables {
		if found.kind != baseTable {
			snapshot.Views = append(snapshot.Views,
				schema.View{Name: found.name, Query: r.views[found.name]})

			continue
		}

		snapshot.Tables = append(snapshot.Tables, r.tableOf(found))
	}

	return snapshot
}

// tableOf joins one table's columns, constraints and indexes into the table they describe.
func (r *catalog) tableOf(found tableRow) schema.Table {
	table := schema.Table{
		Name:    found.name,
		Columns: r.columns[found.name],
		Checks:  r.checks[found.name],
	}

	var findings []string

	if holds := r.keys[found.name]; holds != nil {
		table.PrimaryKey, table.Uniques, table.ForeignKeys = holds.key, holds.uniques, holds.keys
		findings = append(findings, holds.beyond...)
	}

	if carries := r.indexes[found.name]; carries != nil {
		table.Indexes = carries.indexes
		table.Uniques = append(table.Uniques, carries.uniques...)
		findings = append(findings, carries.beyond...)
	}

	table.Beyond = marked(findings)

	return table
}

// marked returns the one marker a set of findings makes, and nothing at all where there are none.
func marked(findings []string) *schema.Unrepresented {
	if len(findings) == 0 {
		return nil
	}

	return &schema.Unrepresented{Detail: strings.Join(findings, "; ")}
}

// readTables lists the base tables and views this database holds, in the order names sort.
func (c *mysqlConn) readTables(ctx context.Context) ([]tableRow, error) {
	rows, err := c.probe(ctx, tableQuery)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var found []tableRow

	for rows.Next() {
		var one tableRow

		if err := rows.Scan(&one.name, &one.kind, &one.collation); err != nil {
			return nil, c.failedProbe("the tables", err)
		}

		found = append(found, one)
	}

	return found, c.finished(rows, "the tables")
}

// readViews reads every view's body, which this engine qualifies with the database's own name.
func (c *mysqlConn) readViews(ctx context.Context) (map[string]string, error) {
	rows, err := c.probe(ctx, viewQuery)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	bodies := make(map[string]string)

	for rows.Next() {
		var name, body string

		if err := rows.Scan(&name, &body); err != nil {
			return nil, c.failedProbe("the views", err)
		}

		bodies[name] = body
	}

	return bodies, c.finished(rows, "the views")
}

// readColumns reads every base table's columns, resolving a json column MariaDB spells otherwise.
func (c *mysqlConn) readColumns(ctx context.Context, tables []tableRow,
	absorbed map[string]bool,
) (map[string][]schema.Column, error) {
	rows, err := c.probe(ctx, columnsQuery)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	collations := tableCollations(tables)
	held := make(map[string][]schema.Column)

	for rows.Next() {
		table, column, err := c.readColumnRow(rows, collations, absorbed)
		if err != nil {
			return nil, err
		}

		held[table] = append(held[table], column)
	}

	return held, c.finished(rows, "the columns")
}

// tableCollations keys each table's own default collation, which a column's own is compared against.
func tableCollations(tables []tableRow) map[string]string {
	collations := make(map[string]string, len(tables))
	for _, found := range tables {
		collations[found.name] = found.collation
	}

	return collations
}

// readColumnRow reads one catalog row back as a column, marking what no operation can express.
func (c *mysqlConn) readColumnRow(rows *sql.Rows, collations map[string]string,
	absorbed map[string]bool,
) (string, schema.Column, error) {
	var (
		table, name, native, nullable, extra, collation string
		recorded                                        *string
	)

	if err := rows.Scan(&table, &name, &native, &nullable, &recorded, &extra,
		&collation); err != nil {
		return "", schema.Column{}, c.failedProbe("the columns", err)
	}

	typed, err := columnType(native, absorbed[table+"\x00"+name])
	if err != nil {
		return "", schema.Column{}, fmt.Errorf("%w: column %q of table %q: %w",
			ErrIntrospect, name, table, err)
	}

	return table, schema.Column{
		Name:     name,
		Type:     typed,
		Nullable: nullable == nullableYes,
		Default:  columnDefault(recorded, extra, c.version.Variant()),
		Beyond:   marked(columnFindings(extra, collation, collations[table])),
	}, nil
}

// A collation is reported only where it differs from the table's own: every text column on this
// engine has one, inherited from the table, so marking all of them would make a varchar column
// compare unequal to the same column on another engine for a reason nobody declared.

// columnFindings lists what one column carries that no operation can express.
func columnFindings(extra, collation, table string) []string {
	var findings []string

	if strings.Contains(strings.ToUpper(extra), "GENERATED") &&
		!strings.Contains(strings.ToUpper(extra), defaultGenerated) {
		findings = append(findings, "this server records it as a generated column, which no "+
			"operation can create")
	}

	if strings.Contains(strings.ToLower(extra), "auto_increment") {
		findings = append(findings, "this server records it as AUTO_INCREMENT, which no operation "+
			"can carry")
	}

	if collation != "" && collation != table {
		findings = append(findings, "it declares the collation "+collation+
			", which differs from its table's own and which no operation can carry")
	}

	return findings
}

// probe runs one of this file's reads, inside this session's transaction when it has one open.
func (c *mysqlConn) probe(ctx context.Context, query string) (*sql.Rows, error) {
	rows, err := c.runner().QueryContext(ctx, query, c.stored)
	if err != nil {
		return nil, c.failedProbe("the catalog", err)
	}

	return rows, nil
}

// finished reports a read that ended in a failure rather than at the end of its rows.
func (c *mysqlConn) finished(rows *sql.Rows, what string) error {
	if err := rows.Err(); err != nil {
		return c.failedProbe(what, err)
	}

	return nil
}

// failedProbe names what this driver was reading when the server stopped answering.
func (c *mysqlConn) failedProbe(what string, err error) error {
	return fmt.Errorf("%w: reading %s of database %q: %w", ErrIntrospect, what, c.database, err)
}
