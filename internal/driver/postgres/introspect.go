package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/schema"
)

// The four probes one snapshot is built from, each over the namespace the session is pointed at.
//
// current_schema() rather than Options.Schema: migr.yaml need not name one, and an unset schema
// leaves the search path to the server, so the namespace is asked for rather than assumed. The one
// bound parameter is the table migr records its own runs in, which a differ would otherwise propose
// dropping -- its indexes go with it, since they are not reported independently.
const (
	relationQuery = `
SELECT c.relkind::text, c.relname, coalesce(pg_get_viewdef(c.oid), ''), c.relispartition
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p', 'v', 'm') AND c.relname <> $1
 ORDER BY c.relname`

	columnQuery = `
SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
       coalesce(pg_get_expr(d.adbin, d.adrelid), ''), a.attgenerated::text, a.attidentity::text,
       coalesce(co.collname, '')
  FROM pg_attribute a
  JOIN pg_class c ON c.oid = a.attrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
  LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
  LEFT JOIN pg_collation co ON co.oid = a.attcollation AND co.collname <> 'default'
 WHERE n.nspname = current_schema() AND a.attnum > 0 AND NOT a.attisdropped
   AND c.relkind IN ('r', 'p') AND c.relname <> $1
 ORDER BY c.relname, a.attnum`

	constraintQuery = `
SELECT t.relname, c.conname, c.contype::text, c.condeferrable,
       coalesce(f.relname, ''), c.confdeltype::text, c.confupdtype::text,
       coalesce(a.attname, ''), coalesce(fa.attname, ''),
       coalesce(pg_get_constraintdef(c.oid), '')
  FROM pg_constraint c
  JOIN pg_class t ON t.oid = c.conrelid
  JOIN pg_namespace n ON n.oid = t.relnamespace
  LEFT JOIN pg_class f ON f.oid = c.confrelid
  LEFT JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
  LEFT JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
  LEFT JOIN pg_attribute fa ON fa.attrelid = c.confrelid AND fa.attnum = c.confkey[k.ord]
 WHERE n.nspname = current_schema() AND c.contype IN ('p', 'u', 'c', 'f') AND t.relname <> $1
 ORDER BY t.relname, c.conname, k.ord`

	indexQuery = `
SELECT t.relname, i.relname, x.indisunique, x.indpred IS NOT NULL, x.indexprs IS NOT NULL,
       coalesce(a.attname, ''), (x.indoption[s] & 1) = 1
  FROM pg_index x
  JOIN pg_class i ON i.oid = x.indexrelid
  JOIN pg_class t ON t.oid = x.indrelid
  JOIN pg_namespace n ON n.oid = t.relnamespace
  CROSS JOIN LATERAL generate_subscripts(x.indkey, 1) AS s
  LEFT JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = x.indkey[s]
 WHERE n.nspname = current_schema() AND t.relname <> $1
   AND NOT EXISTS (SELECT 1 FROM pg_constraint o
                    WHERE o.conindid = x.indexrelid AND o.conrelid = x.indrelid
                      AND o.contype IN ('p', 'u', 'x'))
 ORDER BY t.relname, i.relname, s`
)

// The pg_class relkinds a snapshot reads, each of which holds a name in one namespace.
const (
	relkindPartitioned  = "p"
	relkindView         = "v"
	relkindMaterialized = "m"
)

// The pg_constraint contypes a snapshot reads, which are the four the operation set can express.
const (
	contypePrimaryKey = "p"
	contypeUnique     = "u"
	contypeCheck      = "c"
	contypeForeignKey = "f"
)

// Introspect reads the session's own namespace back, less the table migr records its own runs in.
func (c *pgConn) Introspect(ctx context.Context) (*schema.Snapshot, error) {
	relations, err := c.readRelations(ctx)
	if err != nil {
		return nil, err
	}

	columns, err := c.readColumns(ctx)
	if err != nil {
		return nil, err
	}

	constraints, err := c.readConstraints(ctx)
	if err != nil {
		return nil, err
	}

	indexes, err := c.readIndexes(ctx)
	if err != nil {
		return nil, err
	}

	snapshot := assemble(relations, columns, constraints, indexes)

	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("%w: this driver read a snapshot it cannot stand behind: %w",
			ErrIntrospect, err)
	}

	return snapshot, nil
}

// relation is one named thing in the namespace, which is a table, a view, or something marked.
type relation struct {
	// kind is the relkind the catalog records, which decides whether it is a table or a view.
	kind string
	// name is its own name, unqualified, as the operations name it.
	name string
	// definition is the view body, which the catalog holds for a view and nothing else.
	definition string
	// partition reports whether it is a partition of another table, which no operation creates.
	partition bool
}

// assemble joins what the four probes answered into one snapshot, in the order names sort.
func assemble(relations []relation, columns map[string][]schema.Column,
	constraints map[string]*constrained, indexes map[string]*indexed,
) *schema.Snapshot {
	snapshot := &schema.Snapshot{}

	for _, found := range relations {
		if found.kind == relkindView || found.kind == relkindMaterialized {
			snapshot.Views = append(snapshot.Views, viewOf(found))

			continue
		}

		snapshot.Tables = append(snapshot.Tables,
			tableOf(found, columns[found.name], constraints[found.name], indexes[found.name]))
	}

	return snapshot
}

// viewOf reads a view back, marking a materialized one, which no operation can create.
func viewOf(found relation) schema.View {
	view := schema.View{Name: found.name, Query: found.definition}

	if found.kind == relkindMaterialized {
		view.Beyond = &schema.Unrepresented{Detail: "PostgreSQL records it as a materialized " +
			"view, which no operation can create"}
	}

	return view
}

// tableOf joins one table's columns, constraints and indexes into the table they describe.
func tableOf(found relation, columns []schema.Column, holds *constrained,
	carries *indexed,
) schema.Table {
	table := schema.Table{Name: found.name, Columns: columns}

	if holds != nil {
		table.PrimaryKey, table.Uniques = holds.key, holds.uniques
		table.Checks, table.ForeignKeys = holds.checks, holds.keys
	}

	if carries != nil {
		table.Indexes, table.Uniques = carries.indexes, append(table.Uniques, carries.uniques...)
	}

	table.Beyond = marked(tableFindings(found, holds, carries))

	return table
}

// tableFindings lists what this table carries that no operation can express.
func tableFindings(found relation, holds *constrained, carries *indexed) []string {
	var findings []string

	if found.kind == relkindPartitioned {
		findings = append(findings, "PostgreSQL records it as a partitioned table, which no "+
			"operation can create")
	}

	if found.partition {
		findings = append(findings, "PostgreSQL records it as a partition of another table, which "+
			"no operation can create")
	}

	if holds != nil {
		findings = append(findings, holds.beyond...)
	}

	if carries != nil {
		findings = append(findings, carries.beyond...)
	}

	return findings
}

// marked returns the one marker a set of findings makes, and nothing at all where there are none.
func marked(findings []string) *schema.Unrepresented {
	if len(findings) == 0 {
		return nil
	}

	return &schema.Unrepresented{Detail: strings.Join(findings, "; ")}
}

// readRelations lists the tables and views this namespace holds, in the order names sort.
func (c *pgConn) readRelations(ctx context.Context) ([]relation, error) {
	rows, err := c.probe(ctx, relationQuery)
	if err != nil {
		return nil, err
	}

	var found []relation

	for rows.Next() {
		var one relation

		if err := rows.Scan(&one.kind, &one.name, &one.definition, &one.partition); err != nil {
			return nil, c.failedProbe("the relations", err)
		}

		found = append(found, one)
	}

	return found, c.finished(rows, "the relations")
}

// readColumns reads every base table's columns, keyed on the table that holds them.
func (c *pgConn) readColumns(ctx context.Context) (map[string][]schema.Column, error) {
	rows, err := c.probe(ctx, columnQuery)
	if err != nil {
		return nil, err
	}

	held := make(map[string][]schema.Column)

	for rows.Next() {
		var (
			table, name, native, recorded, generated, identity, collation string
			notNull                                                       bool
		)

		if err := rows.Scan(&table, &name, &native, &notNull, &recorded, &generated, &identity,
			&collation); err != nil {
			return nil, c.failedProbe("the columns", err)
		}

		column, err := columnOf(name, native, recorded, generated, identity, collation, !notNull)
		if err != nil {
			return nil, err
		}

		held[table] = append(held[table], column)
	}

	return held, c.finished(rows, "the columns")
}

// columnOf reads one catalog row back as a column, marking what no operation can express.
func columnOf(name, native, recorded, generated, identity, collation string,
	nullable bool,
) (schema.Column, error) {
	typed, err := columnType(native)
	if err != nil {
		return schema.Column{}, fmt.Errorf("%w: column %q: %w", ErrIntrospect, name, err)
	}

	return schema.Column{
		Name:     name,
		Type:     typed,
		Nullable: nullable,
		Default:  columnDefault(recorded),
		Beyond:   marked(columnFindings(recorded, generated, identity, collation)),
	}, nil
}

// columnFindings lists what one column carries that no operation can express.
func columnFindings(recorded, generated, identity, collation string) []string {
	var findings []string

	if strings.TrimSpace(generated) != "" {
		findings = append(findings, "PostgreSQL records it as a generated column, which no "+
			"operation can create -- the catalog reports it as an ordinary column with a default")
	}

	if strings.TrimSpace(identity) != "" {
		findings = append(findings, "PostgreSQL records it as an identity column, which no "+
			"operation can create")
	}

	if collation != "" {
		findings = append(findings, "it declares the collation "+collation+
			", which no operation can carry")
	}

	if strings.HasPrefix(recorded, "nextval(") {
		findings = append(findings, "its default draws from a sequence, which no operation can "+
			"create -- a serial column is this shape")
	}

	return findings
}

// probe runs one of this file's reads, inside this session's transaction when it has one open.
func (c *pgConn) probe(ctx context.Context, query string) (pgx.Rows, error) {
	rows, err := c.runner().Query(ctx, query, c.history.table)
	if err != nil {
		return nil, c.failedProbe("the catalog", err)
	}

	return rows, nil
}

// finished reports a read that ended in a failure rather than at the end of its rows.
func (c *pgConn) finished(rows pgx.Rows, what string) error {
	rows.Close()

	if err := rows.Err(); err != nil {
		return c.failedProbe(what, err)
	}

	return nil
}

// failedProbe names what this driver was reading when the server stopped answering.
func (c *pgConn) failedProbe(what string, err error) error {
	return fmt.Errorf("%w: reading %s: %w", ErrIntrospect, what, err)
}
