package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// constrained is everything pg_constraint says about one table, in the shape the model holds it.
type constrained struct {
	// key is the table's primary key, absent where it has none.
	key *schema.PrimaryKey
	// uniques are its unique constraints, which this engine records apart from their indexes.
	uniques []schema.Unique
	// checks are its check constraints, whose expressions are this engine's own text.
	checks []schema.Check
	// keys are the references it makes to other tables.
	keys []schema.ForeignKey
	// beyond is what it carries that belongs on the table's own marker.
	beyond []string
}

// pending is one constraint as the catalog answers it, one row per column of its key.
type pending struct {
	// name is the constraint's own name, which this engine records for every one of them.
	name string
	// contype is the letter the catalog records its kind as.
	contype string
	// deferrable reports whether its check can be held until commit, which no operation carries.
	deferrable bool
	// parent is the table a foreign key references, empty for every other kind.
	parent string
	// onDelete and onUpdate are the letters a foreign key's actions are recorded as.
	onDelete, onUpdate string
	// columns are the constrained columns, in the order the constraint declares them.
	columns []string
	// referenced are the columns on the other side of a foreign key, in the order that pairs them.
	referenced []string
	// definition is this engine's own rendering, which is where a check's condition comes from.
	definition string
}

// readConstraints reads every table's constraints, keyed on the table that holds them.
func (c *pgConn) readConstraints(ctx context.Context) (map[string]*constrained, error) {
	rows, err := c.probe(ctx, constraintQuery)
	if err != nil {
		return nil, err
	}

	grouped := make(map[string][]*pending)

	for rows.Next() {
		table, found, err := c.scanConstraint(rows)
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

	if err := c.finished(rows, "the constraints"); err != nil {
		return nil, err
	}

	return resolveConstraints(grouped)
}

// scanConstraint reads one catalog row, which is one column of one constraint.
func (c *pgConn) scanConstraint(rows interface {
	Scan(dest ...any) error
},
) (string, *pending, error) {
	var (
		table, column, referenced string
		found                     pending
	)

	if err := rows.Scan(&table, &found.name, &found.contype, &found.deferrable, &found.parent,
		&found.onDelete, &found.onUpdate, &column, &referenced, &found.definition); err != nil {
		return "", nil, c.failedProbe("the constraints", err)
	}

	if column != "" {
		found.columns = []string{column}
	}

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

// resolveConstraints turns every collected constraint into the element the model holds it as.
func resolveConstraints(grouped map[string][]*pending) (map[string]*constrained, error) {
	resolved := make(map[string]*constrained, len(grouped))

	for table, found := range grouped {
		holds := &constrained{}

		for _, one := range found {
			if err := holds.add(one); err != nil {
				return nil, fmt.Errorf("%w of table %q: %w", ErrIntrospect, table, err)
			}
		}

		resolved[table] = holds
	}

	return resolved, nil
}

// add files one constraint under the element it is, refusing an action this vocabulary cannot name.
func (h *constrained) add(one *pending) error {
	switch one.contype {
	case contypePrimaryKey:
		h.key = &schema.PrimaryKey{Name: one.name, Columns: one.columns}
	case contypeUnique:
		h.uniques = append(h.uniques, schema.Unique{Name: one.name, Columns: one.columns})
	case contypeCheck:
		h.checks = append(h.checks, schema.Check{Name: one.name,
			Expression: checkCondition(one.definition)})
	case contypeForeignKey:
		return h.addForeignKey(one)
	default:
		h.beyond = append(h.beyond, fmt.Sprintf("it carries constraint %q, which PostgreSQL "+
			"records as %q and nothing here can represent", one.name, one.contype))
	}

	return nil
}

// addForeignKey files one foreign key, reading both of its actions back as the names they have.
func (h *constrained) addForeignKey(one *pending) error {
	onDelete, err := referentialAction(one.onDelete)
	if err != nil {
		return err
	}

	onUpdate, err := referentialAction(one.onUpdate)
	if err != nil {
		return err
	}

	h.keys = append(h.keys, schema.ForeignKey{
		Name:              one.name,
		Columns:           one.columns,
		ReferencesTable:   one.parent,
		ReferencesColumns: one.referenced,
		OnDelete:          onDelete,
		OnUpdate:          onUpdate,
		Beyond:            deferrableMarker(one.deferrable),
	})

	return nil
}

// deferrableMarker marks a key whose check can be held until commit, which no operation carries.
func deferrableMarker(deferrable bool) *schema.Unrepresented {
	if !deferrable {
		return nil
	}

	return &schema.Unrepresented{Detail: "PostgreSQL records it as DEFERRABLE, which no " +
		"operation can carry"}
}

// checkCondition returns the condition a rendered check constraint holds, as this engine wrote it.
func checkCondition(definition string) string {
	condition, found := strings.CutPrefix(definition, "CHECK ")
	if !found {
		return definition
	}

	return strings.TrimSpace(condition)
}

// indexed is every index one table carries that no constraint already stands for.
type indexed struct {
	// indexes are its secondary indexes.
	indexes []schema.Index
	// uniques are the unique indexes no constraint backs, which are reported as constraints.
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
	// partial reports whether it covers some rows only, which no operation can express.
	partial bool
	// expression reports whether its key covers anything that is not a column.
	expression bool
	// columns are the covered columns, in the order the index declares them.
	columns []string
	// descending reports whether any covered column is covered in descending order.
	descending bool
	// invalid reports an index PostgreSQL marked INVALID, which every write maintains and no query uses.
	invalid bool
}

// An index backing a primary key, a unique constraint or an exclusion constraint is excluded by the
// probe itself: reporting it as an index of its own would have a differ propose creating a second
// one. The join is on conindid together with conrelid, because a foreign key's conindid names the
// index on the *referenced* side -- measured, joining on conindid alone reports the parent's key
// twice.

// readIndexes reads every table's indexes, less the ones a constraint already stands for.
func (c *pgConn) readIndexes(ctx context.Context) (map[string]*indexed, error) {
	rows, err := c.probe(ctx, indexQuery)
	if err != nil {
		return nil, err
	}

	grouped := make(map[string][]*waiting)

	for rows.Next() {
		table, found, err := c.scanIndex(rows)
		if err != nil {
			return nil, err
		}

		at, started := indexOfWaiting(grouped[table], found.name)
		if !started {
			grouped[table] = append(grouped[table], found)

			continue
		}

		grouped[table][at].columns = append(grouped[table][at].columns, found.columns...)
		grouped[table][at].descending = grouped[table][at].descending || found.descending
	}

	if err := c.finished(rows, "the indexes"); err != nil {
		return nil, err
	}

	return resolveIndexes(grouped), nil
}

// scanIndex reads one catalog row, which is one entry of one index's key.
func (c *pgConn) scanIndex(rows interface {
	Scan(dest ...any) error
},
) (string, *waiting, error) {
	var (
		table, column string
		found         waiting
	)

	if err := rows.Scan(&table, &found.name, &found.unique, &found.partial, &found.expression,
		&column, &found.descending, &found.invalid); err != nil {
		return "", nil, c.failedProbe("the indexes", err)
	}

	if column != "" {
		found.columns = []string{column}
	}

	return table, &found, nil
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
func resolveIndexes(grouped map[string][]*waiting) map[string]*indexed {
	resolved := make(map[string]*indexed, len(grouped))

	for table, found := range grouped {
		carries := &indexed{}

		for _, one := range found {
			carries.add(one)
		}

		resolved[table] = carries
	}

	return resolved
}

// add files one index under the element it stands for, which its uniqueness decides.
func (i *indexed) add(one *waiting) {
	if one.expression || len(one.columns) == 0 {
		i.beyond = append(i.beyond, fmt.Sprintf("it carries index %q, whose key covers something "+
			"that is not a column, which nothing here can represent", one.name))

		return
	}

	beyond := marked(indexFindings(one))

	if one.unique {
		i.uniques = append(i.uniques, schema.Unique{
			Name: one.name, Columns: one.columns, Beyond: beyond, Invalid: one.invalid,
		})

		return
	}

	i.indexes = append(i.indexes, schema.Index{
		Name: one.name, Columns: one.columns, Beyond: beyond, Invalid: one.invalid,
	})
}

// indexFindings lists what one index carries that no operation can express.
func indexFindings(one *waiting) []string {
	var findings []string

	if one.partial {
		findings = append(findings, "PostgreSQL records it as a partial index, which no operation "+
			"can create")
	}

	if one.descending {
		findings = append(findings, "it covers at least one column in descending order, which no "+
			"operation can carry")
	}

	if one.invalid {
		findings = append(findings, "PostgreSQL marks it INVALID -- a concurrent build or drop that "+
			"failed -- so every write maintains it and no query uses it; REINDEX INDEX CONCURRENTLY "+
			"or DROP INDEX CONCURRENTLY it before migr diff can compare it")
	}

	return findings
}
