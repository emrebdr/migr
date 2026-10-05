package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// Measured on 11.4: a json column is a longtext plus a check named after the column whose clause is
// exactly this. Reporting it as a constraint would have a differ propose dropping the column's own
// type, and absorbing it is also what makes a json column compare equal between the two variants --
// a longtext without it is plain text. A hand-written check on that variant is named after its
// column too, so the clause and not the name is what decides.

// jsonCheck is the check MariaDB attaches to a json column, which is how that variant has the type.
func jsonCheck(column string) string {
	return "json_valid(" + quote(column) + ")"
}

// constrained is everything the key probe says about one table, in the shape the model holds it.
type constrained struct {
	// key is the table's primary key, which this engine always calls PRIMARY.
	key *schema.PrimaryKey
	// uniques are its unique constraints, which this engine cannot tell from unique indexes.
	uniques []schema.Unique
	// keys are the references it makes to other tables.
	keys []schema.ForeignKey
	// named is every constraint name on this table, which is what an index is suppressed by.
	named map[string]bool
	// beyond is what it carries that belongs on the table's own marker.
	beyond []string
}

// pending is one constraint as the catalog answers it, one row per column of its key.
type pending struct {
	// name is the constraint's own name, PRIMARY for every primary key on this engine.
	name string
	// kind is what TABLE_CONSTRAINTS calls it.
	kind string
	// parent is the table a foreign key references, empty for every other kind.
	parent string
	// onDelete and onUpdate are the rules REFERENTIAL_CONSTRAINTS answers for a foreign key.
	onDelete, onUpdate string
	// columns are the constrained columns, in the order the constraint declares them.
	columns []string
	// referenced are the columns on the other side of a foreign key, in the order that pairs them.
	referenced []string
}

// readKeys reads every table's primary key, unique constraints and foreign keys.
func (c *mysqlConn) readKeys(ctx context.Context) (map[string]*constrained, error) {
	rows, err := c.probe(ctx, keysQuery)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	grouped := make(map[string][]*pending)

	for rows.Next() {
		table, found, err := c.scanKey(rows)
		if err != nil {
			return nil, err
		}

		at, started := indexOfPending(grouped[table], found.name)
		if !started {
			grouped[table] = append(grouped[table], found)

			continue
		}

		grouped[table][at].columns = append(grouped[table][at].columns, found.columns...)
		grouped[table][at].referenced = append(grouped[table][at].referenced, found.referenced...)
	}

	if err := c.finished(rows, "the key constraints"); err != nil {
		return nil, err
	}

	return c.resolveKeys(grouped)
}

// scanKey reads one catalog row, which is one column of one constraint.
func (c *mysqlConn) scanKey(rows *sql.Rows) (string, *pending, error) {
	var (
		table, column, referenced string
		found                     pending
	)

	if err := rows.Scan(&table, &found.name, &found.kind, &column, &found.parent, &referenced,
		&found.onDelete, &found.onUpdate); err != nil {
		return "", nil, c.failedProbe("the key constraints", err)
	}

	found.columns = []string{column}

	if referenced != "" {
		found.referenced = []string{referenced}
	}

	return table, &found, nil
}

// indexOfPending reports where a named constraint is already being collected, and whether it is.
func indexOfPending(found []*pending, name string) (int, bool) {
	for at, one := range found {
		if one.name == name {
			return at, true
		}
	}

	return 0, false
}

// resolveKeys turns every collected constraint into the element the model holds it as.
func (c *mysqlConn) resolveKeys(grouped map[string][]*pending) (map[string]*constrained, error) {
	resolved := make(map[string]*constrained, len(grouped))

	for table, found := range grouped {
		holds := &constrained{named: make(map[string]bool, len(found))}

		for _, one := range found {
			holds.named[one.name] = true

			if err := c.file(holds, one); err != nil {
				return nil, fmt.Errorf("%w of table %q: %w", ErrIntrospect, table, err)
			}
		}

		resolved[table] = holds
	}

	return resolved, nil
}

// file puts one constraint under the element it is, refusing an action this vocabulary cannot name.
func (c *mysqlConn) file(holds *constrained, one *pending) error {
	switch one.kind {
	case primaryIndex, "PRIMARY KEY":
		holds.key = &schema.PrimaryKey{Name: one.name, Columns: one.columns}
	case constraintUnique:
		holds.uniques = append(holds.uniques, schema.Unique{Name: one.name, Columns: one.columns})
	case constraintKey:
		return c.fileForeignKey(holds, one)
	default:
		holds.beyond = append(holds.beyond, fmt.Sprintf("it carries constraint %q, which this "+
			"server records as %q and nothing here can represent", one.name, one.kind))
	}

	return nil
}

// fileForeignKey files one foreign key, reading both of its actions back as the names they have.
func (c *mysqlConn) fileForeignKey(holds *constrained, one *pending) error {
	onDelete, err := catalogAction(one.onDelete, c.version.Variant())
	if err != nil {
		return err
	}

	onUpdate, err := catalogAction(one.onUpdate, c.version.Variant())
	if err != nil {
		return err
	}

	holds.keys = append(holds.keys, schema.ForeignKey{
		Name:              one.name,
		Columns:           one.columns,
		ReferencesTable:   one.parent,
		ReferencesColumns: one.referenced,
		OnDelete:          onDelete,
		OnUpdate:          onUpdate,
	})

	return nil
}

// readChecks reads every check constraint, reporting the ones a column's own type absorbs apart.
func (c *mysqlConn) readChecks(ctx context.Context) (map[string]bool,
	map[string][]schema.Check, error,
) {
	rows, err := c.probe(ctx, c.checksQuery())
	if err != nil {
		return nil, nil, err
	}

	defer rows.Close()

	absorbed := make(map[string]bool)
	checks := make(map[string][]schema.Check)

	for rows.Next() {
		var table, name, clause, enforced string

		if err := rows.Scan(&table, &name, &clause, &enforced); err != nil {
			return nil, nil, c.failedProbe("the check constraints", err)
		}

		if clause == jsonCheck(name) {
			absorbed[table+"\x00"+name] = true

			continue
		}

		checks[table] = append(checks[table], schema.Check{
			Name: name, Expression: clause, Beyond: enforcedMarker(enforced),
		})
	}

	return absorbed, checks, c.finished(rows, "the check constraints")
}

// checksQuery returns the spelling of the check probe this variant's catalog answers.
func (c *mysqlConn) checksQuery() string {
	if c.version.Variant() == MariaDB {
		return mariaChecksQuery
	}

	return mysqlChecksQuery
}

// enforcedMarker marks a check the server records but does not apply, which no operation can create.
func enforcedMarker(enforced string) *schema.Unrepresented {
	if strings.ToUpper(strings.TrimSpace(enforced)) != notEnforced {
		return nil
	}

	return &schema.Unrepresented{Detail: "this server records it as NOT ENFORCED, so it holds " +
		"nothing, and no operation can create one"}
}

// carried is every index one table has that no constraint already stands for.
type carried struct {
	// indexes are its secondary indexes.
	indexes []schema.Index
	// uniques are the unique indexes no constraint row named, which are reported as constraints.
	uniques []schema.Unique
	// beyond is what it carries that belongs on the table's own marker.
	beyond []string
}

// waiting is one index as the catalog answers it, one row per entry of its key.
type waiting struct {
	// name is the index's own name, which this engine records for every index.
	name string
	// unique reports whether it enforces uniqueness, which is what makes it a constraint here.
	unique bool
	// expression reports whether its key covers anything that is not a column.
	expression bool
	// prefixed reports whether it covers a column's first bytes only, which no operation carries.
	prefixed bool
	// descending reports whether any covered column is covered in descending order.
	descending bool
	// columns are the covered columns, in the order the index declares them.
	columns []string
}

// Three kinds are suppressed, and the third is this engine's alone: the index a primary key is, the
// index behind a unique constraint, and the index this server creates for a foreign key over an
// unindexed column -- which it names after the constraint. Reporting any of them would have a differ
// propose creating a second one.

// readIndexes reads every table's indexes, less the ones a constraint already stands for.
func (c *mysqlConn) readIndexes(ctx context.Context,
	keys map[string]*constrained,
) (map[string]*carried, error) {
	rows, err := c.probe(ctx, indexesQuery)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	grouped := make(map[string][]*waiting)

	for rows.Next() {
		table, found, err := c.scanIndex(rows)
		if err != nil {
			return nil, err
		}

		if suppressed(keys[table], found.name) {
			continue
		}

		at, started := indexOfWaiting(grouped[table], found.name)
		if !started {
			grouped[table] = append(grouped[table], found)

			continue
		}

		grouped[table][at].merge(found)
	}

	if err := c.finished(rows, "the indexes"); err != nil {
		return nil, err
	}

	return resolveIndexes(grouped), nil
}

// One rung rather than two: a primary key is a constraint on this engine as well as an index, and
// both are named PRIMARY, so the set of constraint names already holds it.

// suppressed reports an index a constraint already stands for, which the key probe reported.
func suppressed(holds *constrained, name string) bool {
	return holds != nil && holds.named[name]
}

// scanIndex reads one catalog row, which is one entry of one index's key.
func (c *mysqlConn) scanIndex(rows *sql.Rows) (string, *waiting, error) {
	var (
		table, name, column, collation string
		nonUnique, prefix              int
	)

	if err := rows.Scan(&table, &name, &column, &nonUnique, &collation, &prefix); err != nil {
		return "", nil, c.failedProbe("the indexes", err)
	}

	found := &waiting{
		name:       name,
		unique:     nonUnique == 0,
		expression: column == "",
		prefixed:   prefix > 0,
		descending: collation == descendingOrder,
	}

	if column != "" {
		found.columns = []string{column}
	}

	return table, found, nil
}

// merge folds one more key entry into the index already being collected.
func (w *waiting) merge(found *waiting) {
	w.columns = append(w.columns, found.columns...)
	w.expression = w.expression || found.expression
	w.prefixed = w.prefixed || found.prefixed
	w.descending = w.descending || found.descending
}

// indexOfWaiting reports where a named index is already being collected, and whether it is.
func indexOfWaiting(found []*waiting, name string) (int, bool) {
	for at, one := range found {
		if one.name == name {
			return at, true
		}
	}

	return 0, false
}

// resolveIndexes turns every collected index into the element the model holds it as.
func resolveIndexes(grouped map[string][]*waiting) map[string]*carried {
	resolved := make(map[string]*carried, len(grouped))

	for table, found := range grouped {
		carries := &carried{}

		for _, one := range found {
			carries.add(one)
		}

		resolved[table] = carries
	}

	return resolved
}

// add files one index under the element it stands for, which its uniqueness decides.
func (i *carried) add(one *waiting) {
	if one.expression || len(one.columns) == 0 {
		i.beyond = append(i.beyond, fmt.Sprintf("it carries index %q, whose key covers something "+
			"that is not a column, which nothing here can represent", one.name))

		return
	}

	beyond := marked(indexFindings(one))

	if one.unique {
		i.uniques = append(i.uniques, schema.Unique{
			Name: one.name, Columns: one.columns, Beyond: beyond,
		})

		return
	}

	i.indexes = append(i.indexes, schema.Index{
		Name: one.name, Columns: one.columns, Beyond: beyond,
	})
}

// indexFindings lists what one index carries that no operation can express.
func indexFindings(one *waiting) []string {
	var findings []string

	if one.prefixed {
		findings = append(findings, "it covers a prefix of a column rather than the whole of it, "+
			"which the operation set has no field for")
	}

	if one.descending {
		findings = append(findings, "it covers at least one column in descending order, which no "+
			"operation can carry")
	}

	return findings
}
