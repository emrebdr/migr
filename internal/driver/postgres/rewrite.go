package postgres

import (
	"fmt"
	"hash/fnv"
	"strings"
	"unicode/utf8"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// checkedNotNullRelease is the release whose SET NOT NULL trusts a validated check instead of scanning.
const checkedNotNullRelease = 12

// identifierLimit is the longest name PostgreSQL keeps, in bytes, truncating anything longer.
const identifierLimit = 63

// notNullSuffix ends the name of the check a not_null_check rewrite adds and drops again.
const notNullSuffix = "_nn"

// The reasons a plan prints beside each rewrite, measured on 16 and 17 over 15 million rows (ADR 0085).
const (
	notValidReason = "added NOT VALID and validated on its own, so writes go on while the rows are " +
		"checked -- the plain statement blocked them for the whole scan"
	notNullReason = "a validated check stands in for the scan, so SET NOT NULL holds its lock for " +
		"a moment rather than for the whole table"
	concurrentReason = "built CONCURRENTLY, outside any transaction, so writes go on while it builds " +
		"-- a build that fails leaves the index INVALID, to be dropped before migr resolve"
)

// compile-time proof that a session offers the rewrites this server can perform.
var _ driver.Rewriter = (*pgConn)(nil)

// Rewrite returns the safer sequence this server performs an operation as, where it has one.
func (c *pgConn) Rewrite(op ir.Operation) (driver.Rewrite, bool) {
	return rewriteFor(c.version, op)
}

// rewriteFor returns the safer sequence an operation runs as on one server version, compiling nothing it refuses.
func rewriteFor(v serverVersion, op ir.Operation) (driver.Rewrite, bool) {
	plain, err := compile(op)
	if err != nil {
		return driver.Rewrite{}, false
	}

	switch o := op.(type) {
	case ir.AddCheck:
		return notValid(plain[0], o.Table, o.Name), true
	case ir.AddForeignKey:
		return notValid(plain[0], o.Table, o.Name), true
	case ir.AlterColumn:
		return notNullBehindCheck(v, o)
	case ir.AddIndex:
		return concurrently(o), true
	default:
		return driver.Rewrite{}, false
	}
}

// notValid adds a constraint without checking existing rows, and checks them in a statement of its own.
func notValid(plain driver.Statement, table, name string) driver.Rewrite {
	return driver.Rewrite{
		Name: driver.RewriteNotValid,
		Statements: []driver.Statement{
			{SQL: strings.TrimSuffix(plain.SQL, terminator) + " NOT VALID" + terminator},
			validate(table, name),
		},
		Reason: notValidReason,
		Lock:   capability.BriefExclusive,
	}
}

// validate checks a NOT VALID constraint's rows outside any transaction, so the ADD's lock is gone first.
func validate(table, name string) driver.Statement {
	return driver.Statement{
		SQL:           "ALTER TABLE " + quote(table) + " VALIDATE CONSTRAINT " + quote(name) + terminator,
		NoTransaction: true,
	}
}

// notNullBehindCheck sets NOT NULL behind a validated check, on a server whose SET NOT NULL trusts one.
func notNullBehindCheck(v serverVersion, op ir.AlterColumn) (driver.Rewrite, bool) {
	if op.Nullable == nil || *op.Nullable || op.Type != nil || !v.AtLeast(checkedNotNullRelease) {
		return driver.Rewrite{}, false
	}

	check := notNullCheckName(op.Table, op.Column)
	table := "ALTER TABLE " + quote(op.Table) + " "

	stmts := []driver.Statement{
		{SQL: table + "ADD CONSTRAINT " + quote(check) + " CHECK (" + quote(op.Column) +
			" IS NOT NULL) NOT VALID;"},
		validate(op.Table, check),
		{SQL: table + "ALTER COLUMN " + quote(op.Column) + " SET NOT NULL" + terminator},
		{SQL: table + "DROP CONSTRAINT " + quote(check) + terminator},
	}

	rest, err := remainingChanges(op)
	if err != nil {
		return driver.Rewrite{}, false
	}

	return driver.Rewrite{
		Name:       driver.RewriteNotNullCheck,
		Statements: append(stmts, rest...),
		Reason:     notNullReason,
		Lock:       capability.BriefExclusive,
	}, true
}

// remainingChanges compiles what an alteration asks for besides its nullability, in the order it always runs.
func remainingChanges(op ir.AlterColumn) ([]driver.Statement, error) {
	if op.Default == nil && !op.DropDefault {
		return nil, nil
	}

	rest := op
	rest.Nullable = nil

	return compileAlterColumn(rest)
}

// notNullCheckName derives the check a not_null_check rewrite adds, within the length PostgreSQL keeps.
func notNullCheckName(table, column string) string {
	name := table + "_" + column + notNullSuffix
	if len(name) <= identifierLimit {
		return name
	}

	sum := fnv.New32a()
	_, _ = sum.Write([]byte(name))
	tag := fmt.Sprintf("_%08x%s", sum.Sum32(), notNullSuffix)

	cut := identifierLimit - len(tag)
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}

	return name[:cut] + tag
}

// concurrently builds an index without blocking writes, which PostgreSQL refuses inside a transaction.
func concurrently(op ir.AddIndex) driver.Rewrite {
	return driver.Rewrite{
		Name: driver.RewriteConcurrentIndex,
		Statements: []driver.Statement{{
			SQL: "CREATE INDEX CONCURRENTLY " + quote(op.Name) + " ON " + quote(op.Table) +
				" (" + quoteAll(op.Columns) + ")" + terminator,
			NoTransaction: true,
		}},
		Reason: concurrentReason,
		Lock:   capability.LockFree,
	}
}
