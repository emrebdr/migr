package fold

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// The data operations change no schema, so folding one leaves it exactly as it was -- but each names
// tables and columns, and naming one that is not there is the same broken reference an add_column
// onto a table nothing created is. That is what makes M10-T3's reference check catch a backfill of a
// column nothing added, with nothing in the check knowing data operations exist.

// dataCopy checks a copy's two tables hold every column it reads and writes.
func (f *folder) dataCopy(at int32, op ir.DataCopy) {
	from, readable := f.table(at, op.Kind(), op.From)
	to, writable := f.table(at, op.Kind(), op.To)

	if !readable || !writable {
		return
	}

	for _, pair := range op.Columns {
		if _, held := f.held.Tables[from].Column(pair.From); !held {
			f.refuse(at, op.Kind(), qualified(op.From, pair.From), "this schema holds no such column")
		}

		if _, held := f.held.Tables[to].Column(pair.To); !held {
			f.refuse(at, op.Kind(), qualified(op.To, pair.To), "this schema holds no such column")
		}
	}
}

// dataBackfill checks a backfill's column is there and is the type it says it writes.
func (f *folder) dataBackfill(at int32, op ir.DataBackfill) {
	f.written(at, op.Kind(), op.Table, op.Column, op.Type)
}

// dataTransform checks a transform's two columns are there and the one it writes is the type it says.
func (f *folder) dataTransform(at int32, op ir.DataTransform) {
	i, held := f.written(at, op.Kind(), op.Table, op.Column, op.Type)
	if !held {
		return
	}

	if _, reads := f.held.Tables[i].Column(op.From); !reads {
		f.refuse(at, op.Kind(), qualified(op.Table, op.From), "this schema holds no such column")
	}
}

// written checks the column a data operation writes, and returns its table's place when it has one.
func (f *folder) written(at int32, kind ir.Kind, table, column string, stated schema.Type) (int, bool) {
	i, held := f.table(at, kind, table)
	if !held {
		return 0, false
	}

	found, carries := f.held.Tables[i].Column(column)
	if !carries {
		f.refuse(at, kind, qualified(table, column), "this schema holds no such column")

		return i, true
	}

	if problem := mistyped(found, stated); problem != "" {
		f.refuse(at, kind, qualified(table, column), problem)
	}

	return i, true
}

// mistyped says how a stated type contradicts the column's, and nothing where it agrees or is a guess.
func mistyped(held schema.Column, stated schema.Type) string {
	logical, exact := held.Type.Logical()
	if !exact || logical.String() == stated.String() {
		return ""
	}

	return fmt.Sprintf("it writes the column as %s, and this schema holds it as %s", stated, logical)
}
