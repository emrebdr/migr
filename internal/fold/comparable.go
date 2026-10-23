package fold

import "github.com/emrebdr/migr/internal/schema"

// Three things in a snapshot are engine text and a projection has never seen an engine,
// so it holds what the migration wrote and the database holds what the server made of it. Measured
// on four servers, one view asked for as `SELECT id FROM t` reads back expanded by PostgreSQL and
// database-qualified by MySQL, and one check expression `id > 0` reads back four ways:
//
//	SQLite   id > 0                PostgreSQL  ((id > 0))
//	MySQL    (`id` > 0)            MariaDB     `id` > 0
//
// So the text is the one thing a projection and a live schema cannot be held to each other on.
// Compared as it is, every view and every check differs and a differ proposes replacing all of them
// -- on four of the five engines, over a database that is exactly up to date.
//
// Comparable is what a caller uses instead: for an element both schemas hold under one name, the
// live text is carried across, so only a genuine arrival or departure is left to propose. An element
// only one side holds keeps its own text, which is what lets the operation that creates or drops it
// carry the real body.
//
// The third engine-text field is a column default, and it needs none of this: every driver
// normalizes one into the neutral vocabulary, so a literal and a neutral function do
// compare. What a driver cannot normalize it records as an expression, and internal/diff refuses
// that rather than comparing it.

// Comparable returns the desired schema with every engine-text body the live schema also holds.
func Comparable(live, desired schema.Snapshot) schema.Snapshot {
	held := desired.Canonical()
	reference := live.Canonical()

	held.Views = mapped(held.Views, func(view schema.View) schema.View {
		if from, shared := reference.View(view.Name); shared {
			view.Query = from.Query
		}

		return view
	})

	held.Tables = mapped(held.Tables, func(table schema.Table) schema.Table {
		from, shared := reference.Table(table.Name)
		if !shared {
			return table
		}

		table.Checks = sharedChecks(table.Checks, from.Checks)

		return table
	})

	return held.Canonical()
}

// sharedChecks returns one table's checks with every expression the live table also names taken from it.
func sharedChecks(held, reference []schema.Check) []schema.Check {
	return mapped(held, func(check schema.Check) schema.Check {
		for _, from := range reference {
			if from.Name == check.Name {
				check.Expression = from.Expression

				break
			}
		}

		return check
	})
}
