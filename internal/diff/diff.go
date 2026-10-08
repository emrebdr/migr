package diff

import (
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// Diff returns the operations that turn one schema into the other, and every change it will not propose.
func Diff(from, to schema.Snapshot, confirmed Renames) (ir.Operations, []Refusal) {
	d := &differ{
		from:    from.Canonical(),
		to:      to.Canonical(),
		buckets: make(map[ir.Kind]ir.Operations, len(order)),
		absent:  make(map[string]bool),
		missing: make(map[string]map[string]bool),
	}

	d.planRenames(confirmed)
	d.planTables()
	d.planConstraints()
	d.planViews()

	return d.operations(), d.refusals
}

// differ holds one comparison: the two schemas, what it has emitted, and what it would not.
type differ struct {
	// from is the schema being changed, in canonical order and with every confirmed rename applied.
	from schema.Snapshot
	// to is the schema being changed into, in canonical order.
	to schema.Snapshot
	// buckets holds the emitted operations by kind, which order assembles into a sequence.
	buckets map[ir.Kind]ir.Operations
	// refusals are the changes this differ would not propose, in the order it reached them.
	refusals []Refusal
	// absent names every table of the target schema whose creation was refused.
	absent map[string]bool
	// missing names every column of the target schema whose addition was refused, by table.
	missing map[string]map[string]bool
}

// A refusal must not leave an operation behind that depends on it: an index over a column that was
// refused would name something that is never going to be there. So the differ keeps a model of the
// target state and consults it before every constraint it emits -- names and nothing else, which is
// deliberately not the IR-onto-snapshot projection M10-T3 needs and must not grow into one.

// refuseTable records a table of the target schema that cannot be built, and everything on it.
func (d *differ) refuseTable(name string, kind ir.Kind, reason string) {
	d.absent[name] = true
	d.refuse(kind, name, reason+" -- and nothing else on that table is proposed either")
}

// refuseColumn records a column of the target schema that cannot be added, and so will not be there.
func (d *differ) refuseColumn(table, column string) {
	if d.missing[table] == nil {
		d.missing[table] = make(map[string]bool)
	}

	d.missing[table][column] = true
}

// willExist reports whether a table named by the target schema is going to be there afterwards.
func (d *differ) willExist(table string) bool {
	if d.absent[table] {
		return false
	}

	_, named := d.to.Table(table)

	return named
}

// willHold reports whether a table named by the target schema is going to hold a given column.
func (d *differ) willHold(table, column string) bool {
	if !d.willExist(table) || d.missing[table][column] {
		return false
	}

	held, _ := d.to.Table(table)

	_, carries := held.Column(column)

	return carries
}

// unavailable names the first column of a list that is not going to be there, and whether one is.
func (d *differ) unavailable(table string, columns []string) (string, bool) {
	for _, column := range columns {
		if !d.willHold(table, column) {
			return column, true
		}
	}

	return "", false
}

// hasEqual reports whether a set already holds something equal to one element of another.
func hasEqual[T any](held []T, item T, equal func(T, T) bool) bool {
	for _, one := range held {
		if equal(one, item) {
			return true
		}
	}

	return false
}
