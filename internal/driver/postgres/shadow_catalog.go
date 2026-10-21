package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The catalog reads a shadow is built from and swapped in by, each about one table by name. A shadow
// is created LIKE the table without its indexes, and each of them -- a key, a unique, an exclusion
// constraint, or a plain index -- is created on it under a name derived from its own, since an index
// name is held by the original for as long as it exists. At the cutover both sets are renamed: the
// original's to names derived for the retired table, then the shadow's to the original names, so the
// table that comes out carries the names every migration recorded.

// nameLimit is NAMEDATALEN - 1, the longest identifier PostgreSQL keeps.
const nameLimit = 63

// digestWidth is how many hexadecimal digits of a digest a derived name keeps.
const digestWidth = 8

// relationQuery reads what decides whether a table can be changed through a shadow at all.
const shadowedRelationQuery = `SELECT c.oid, c.relkind::text, c.relispartition, c.relrowsecurity, c.relreplident::text,
  EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid OR i.inhparent = c.oid),
  pg_get_userbyid(c.relowner), current_user
 FROM pg_class c WHERE c.oid = to_regclass(quote_ident($1))`

// foreignKeysQuery lists every foreign key on a table or into it.
const foreignKeysQuery = `SELECT conname || ' (on ' || conrelid::regclass::text || ')' FROM pg_constraint
 WHERE contype = 'f' AND (conrelid = $1 OR confrelid = $1) ORDER BY 1`

// dependentsQuery lists the views and rules that read a table, which a rename would leave reading the retired one.
const dependentsQuery = `SELECT DISTINCT v.oid::regclass::text FROM pg_depend d
 JOIN pg_rewrite r ON r.oid = d.objid JOIN pg_class v ON v.oid = r.ev_class
 WHERE d.classid = 'pg_rewrite'::regclass AND d.refobjid = $1 AND v.oid <> $1 ORDER BY 1`

// triggersQuery lists a table's own triggers.
const triggersQuery = `SELECT tgname FROM pg_trigger WHERE tgrelid = $1 AND NOT tgisinternal ORDER BY 1`

// policiesQuery lists a table's row-level security policies.
const policiesQuery = `SELECT polname FROM pg_policy WHERE polrelid = $1 ORDER BY 1`

// constraintIndexesQuery lists the constraints a table's indexes enforce: its key, its uniques and its exclusions.
const constraintIndexesQuery = `SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint
 WHERE conrelid = $1 AND contype IN ('p', 'u', 'x') ORDER BY conname`

// plainIndexesQuery lists a table's valid indexes that enforce no constraint, with their definitions.
const plainIndexesQuery = `SELECT i.relname, pg_get_indexdef(x.indexrelid), x.indisunique FROM pg_index x
 JOIN pg_class i ON i.oid = x.indexrelid
 WHERE x.indrelid = $1 AND x.indisvalid AND NOT EXISTS (SELECT 1 FROM pg_constraint c
   WHERE c.conindid = x.indexrelid AND c.conrelid = x.indrelid AND c.contype IN ('p', 'u', 'x'))
 ORDER BY i.relname`

// grantsQuery lists the privileges a table grants anyone but its owner.
const grantsQuery = `SELECT coalesce(r.rolname, ''), a.privilege_type, a.is_grantable
 FROM pg_class c CROSS JOIN LATERAL aclexplode(c.relacl) a LEFT JOIN pg_roles r ON r.oid = a.grantee
 WHERE c.oid = $1 AND a.grantee <> c.relowner ORDER BY 1, 2`

// columnGrantsQuery lists the privileges a table's columns grant anyone but its owner.
const columnGrantsQuery = `SELECT att.attname, coalesce(r.rolname, ''), a.privilege_type, a.is_grantable
 FROM pg_attribute att JOIN pg_class c ON c.oid = att.attrelid
 CROSS JOIN LATERAL aclexplode(att.attacl) a LEFT JOIN pg_roles r ON r.oid = a.grantee
 WHERE att.attrelid = $1 AND att.attacl IS NOT NULL AND NOT att.attisdropped AND a.grantee <> c.relowner
 ORDER BY att.attnum, 2, 3`

// writtenColumnsQuery lists a table's columns a row can be written to, with the type a value is read back as.
const writtenColumnsQuery = `SELECT a.attname, format_type(a.atttypid, NULL) FROM pg_attribute a
 WHERE a.attrelid = to_regclass(quote_ident($1)) AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = ''
 ORDER BY a.attnum`

// ownedSequencesQuery lists the sequences a table's serial columns own, by column.
const ownedSequencesQuery = `SELECT s.oid::regclass::text, a.attname FROM pg_depend d
 JOIN pg_class s ON s.oid = d.objid AND s.relkind = 'S'
 JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
 WHERE d.refobjid = $1 AND d.classid = 'pg_class'::regclass AND d.deptype = 'a' ORDER BY a.attname`

// identityColumnsQuery lists a table's identity columns.
const identityColumnsQuery = `SELECT attname FROM pg_attribute
 WHERE attrelid = $1 AND attidentity <> '' AND NOT attisdropped ORDER BY attnum`

// shadowedRelation is what the catalog says about the table a shadow is made of.
type shadowedRelation struct {
	// oid is the table's.
	oid uint32
	// kind is its relkind.
	kind string
	// partition reports a partition of another table.
	partition bool
	// secured reports row-level security switched on.
	secured bool
	// identity is its replica identity: d, f, i or n.
	identity string
	// inherits reports a table that inherits or is inherited from.
	inherits bool
	// owner is the role that owns it.
	owner string
	// current is the role this session runs as, which owns whatever it creates.
	current string
}

// relationOf reads the table a shadow is made of, refusing a name that is not a table here.
func (c *pgConn) relationOf(ctx context.Context, table string) (shadowedRelation, error) {
	var r shadowedRelation

	err := c.runner().QueryRow(ctx, shadowedRelationQuery, table).Scan(&r.oid, &r.kind, &r.partition, &r.secured,
		&r.identity, &r.inherits, &r.owner, &r.current)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fmt.Errorf("%s is not a table in this schema", table)
	}

	if err != nil {
		return r, fmt.Errorf("reading %s from the catalog: %w", table, err)
	}

	return r, nil
}

// names runs a catalog query whose rows are one name each.
func (c *pgConn) names(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := c.runner().Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// pairs runs a catalog query whose rows are two texts each.
func (c *pgConn) pairs(ctx context.Context, query string, args ...any) ([][2]string, error) {
	rows, err := c.runner().Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	return pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
		var p [2]string

		return p, row.Scan(&p[0], &p[1])
	})
}

// indexedConstraint is a constraint an index enforces, as its definition reads.
type indexedConstraint struct {
	// name is the constraint's, which is also its index's.
	name string
	// definition is pg_get_constraintdef's text: PRIMARY KEY (...), UNIQUE (...), or EXCLUDE ....
	definition string
}

// plainIndex is an index that enforces no constraint.
type plainIndex struct {
	// name is the index's.
	name string
	// definition is pg_get_indexdef's text.
	definition string
	// unique reports a unique index.
	unique bool
}

// indexesOf reads a table's constraint indexes and its plain ones.
func (c *pgConn) indexesOf(ctx context.Context, oid uint32) ([]indexedConstraint, []plainIndex, error) {
	held, err := c.pairs(ctx, constraintIndexesQuery, oid)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the constraints an index enforces: %w", err)
	}

	constraints := make([]indexedConstraint, len(held))
	for i, p := range held {
		constraints[i] = indexedConstraint{name: p[0], definition: p[1]}
	}

	rows, err := c.runner().Query(ctx, plainIndexesQuery, oid)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the indexes: %w", err)
	}

	plain, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (plainIndex, error) {
		var p plainIndex

		return p, row.Scan(&p.name, &p.definition, &p.unique)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("reading the indexes: %w", err)
	}

	return constraints, plain, nil
}

// writtenColumn is a column a row is written to, and the type a captured value is read back as.
type writtenColumn struct {
	// name is the column's.
	name string
	// typ is its type without a modifier, so an assignment applies the column's own length or precision.
	typ string
}

// writtenColumnsOf reads the columns of a table a row is written to, in order.
func (c *pgConn) writtenColumnsOf(ctx context.Context, table string) ([]writtenColumn, error) {
	held, err := c.pairs(ctx, writtenColumnsQuery, table)
	if err != nil {
		return nil, fmt.Errorf("reading the columns of %s: %w", table, err)
	}

	columns := make([]writtenColumn, len(held))
	for i, p := range held {
		columns[i] = writtenColumn{name: p[0], typ: p[1]}
	}

	return columns, nil
}

// derivedName names an object of a shadow-table migration, unique to its shadow and fit to NAMEDATALEN.
func derivedName(name, shadow, suffix string) string {
	stem := "_" + name + "_" + digestOf(shadow)
	if len(stem)+len(suffix) <= nameLimit {
		return stem + suffix
	}

	keep := nameLimit - len(suffix) - digestWidth - 1

	return stem[:keep] + "_" + digestOf(stem) + suffix
}

// newIndexName is what an index of the original is called on the shadow, until the cutover gives it its own name.
func newIndexName(name, shadow string) string {
	return derivedName(name, shadow, "_new")
}

// oldIndexName is what an index of the original is called once the original is retired.
func oldIndexName(name, shadow string) string {
	return derivedName(name, shadow, "_old")
}

// digestOf returns a short, stable hexadecimal digest of a name.
func digestOf(name string) string {
	sum := sha256.Sum256([]byte(name))

	return hex.EncodeToString(sum[:])[:digestWidth]
}

// grantee renders a role a privilege is granted to, PUBLIC being the empty name aclexplode reports for it.
func grantee(role string) string {
	if role == "" {
		return "PUBLIC"
	}

	return quote(role)
}

// indexBody is an index definition from its access method on, which a copy prefixes with its own name and table.
func indexBody(definition string) (string, bool) {
	at := strings.Index(definition, " USING ")
	if at < 0 {
		return "", false
	}

	return definition[at:], true
}
