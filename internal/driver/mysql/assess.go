package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

// rowFormatQuery reads a table's row format, which information_schema answers without PROCESS.
const rowFormatQuery = "SELECT coalesce(ROW_FORMAT, '') FROM information_schema.TABLES " +
	"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?"

// indexQuery reads every index column of a table and its index type, also without PROCESS.
const indexQuery = "SELECT INDEX_TYPE, COLUMN_NAME FROM information_schema.STATISTICS " +
	"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?"

// rowVersionQuery reads MySQL's instant row-version count, which is the one fact here behind PROCESS.
const rowVersionQuery = "SELECT TOTAL_ROW_VERSIONS FROM information_schema.INNODB_TABLES " +
	"WHERE NAME = ?"

// compressedFormat is how information_schema spells ROW_FORMAT=COMPRESSED, KEY_BLOCK_SIZE included.
const compressedFormat = "compressed"

// fulltextIndex is the index type that keeps a hidden FTS_DOC_ID column and blocks an instant change.
const fulltextIndex = "FULLTEXT"

// privilegeRefused is ER_SPECIFIC_ACCESS_DENIED_ERROR, the answer without PROCESS to INNODB_TABLES.
const privilegeRefused = 1227

// tableFacts is what the catalog says about one table that decides whether a change can be instant.
type tableFacts struct {
	// exists is whether the table is there at all; a table not yet created is assessed at its worst.
	exists bool
	// compressed is whether the table is ROW_FORMAT=COMPRESSED, which no instant change supports.
	compressed bool
	// fulltext is whether the table carries a FULLTEXT index, which no instant change supports.
	fulltext bool
	// indexed holds every column some index covers, in the catalog's own spelling.
	indexed map[string]bool
}

// Assess returns the verdict at its cheaper cost where this table meets the condition it was lowered on.
func (c *mysqlConn) Assess(ctx context.Context, op ir.Operation, v capability.Verdict,
) (capability.Verdict, error) {
	if v.Condition() == "" {
		return v, nil
	}

	table, column := assessedTarget(op)
	if table == "" {
		return v, nil
	}

	permits, err := c.permitsInstant(ctx, table, column)
	if err != nil || !permits {
		return v, err
	}

	return v.Confirmed(), nil
}

// assessedTarget names the table, and the column for a drop, that an instant change would touch.
func assessedTarget(op ir.Operation) (string, string) {
	switch o := op.(type) {
	case ir.AddColumn:
		return o.Table, ""
	case ir.DropColumn:
		return o.Table, o.Column
	default:
		return "", ""
	}
}

// permitsInstant reports whether this server can change the table instantly, reading every fact it needs.
func (c *mysqlConn) permitsInstant(ctx context.Context, table, column string) (bool, error) {
	facts, err := c.readTableFacts(ctx, table)
	if err != nil {
		return false, err
	}

	if !facts.exists || facts.compressed || facts.fulltext {
		return false, nil
	}

	if column != "" && c.version.variant != MariaDB && facts.indexed[strings.ToLower(column)] {
		return false, nil
	}

	if !c.version.countsRowVersions() {
		return true, nil
	}

	return c.belowRowVersionCeiling(ctx, table)
}

// readTableFacts reads a table's row format and its indexes from information_schema.
func (c *mysqlConn) readTableFacts(ctx context.Context, table string) (tableFacts, error) {
	stored := storedName(table, c.folding)

	var format string

	err := c.runner().QueryRowContext(ctx, rowFormatQuery, stored).Scan(&format)
	if errors.Is(err, sql.ErrNoRows) {
		return tableFacts{}, nil
	}

	if err != nil {
		return tableFacts{}, assessFailure(table, "reading its row format", err)
	}

	facts := tableFacts{exists: true, compressed: strings.EqualFold(format, compressedFormat)}

	return facts, c.readIndexedColumns(ctx, table, stored, &facts)
}

// readIndexedColumns records which columns the table's indexes cover, and whether one of them is FULLTEXT.
func (c *mysqlConn) readIndexedColumns(ctx context.Context, table, stored string, facts *tableFacts) error {
	rows, err := c.runner().QueryContext(ctx, indexQuery, stored)
	if err != nil {
		return assessFailure(table, "reading its indexes", err)
	}

	defer rows.Close()

	facts.indexed = make(map[string]bool)

	for rows.Next() {
		var kind, column string
		if err := rows.Scan(&kind, &column); err != nil {
			return assessFailure(table, "reading its indexes", err)
		}

		facts.fulltext = facts.fulltext || strings.EqualFold(kind, fulltextIndex)
		facts.indexed[strings.ToLower(column)] = true
	}

	if err := rows.Err(); err != nil {
		return assessFailure(table, "reading its indexes", err)
	}

	return nil
}

// belowRowVersionCeiling reports a table with room for one more instant change, and no without PROCESS.
func (c *mysqlConn) belowRowVersionCeiling(ctx context.Context, table string) (bool, error) {
	var versions int

	err := c.runner().QueryRowContext(ctx, rowVersionQuery,
		c.database+"/"+storedName(table, c.folding)).Scan(&versions)

	switch {
	case hasNumber(err, privilegeRefused), errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, assessFailure(table, "reading its instant row versions", err)
	default:
		return versions < maxRowVersions, nil
	}
}

// assessFailure reports a catalog read that failed, leaving the verdict at its pessimistic cost.
func assessFailure(table, doing string, err error) error {
	return fmt.Errorf("%w: table %q: %s: %w", ErrAssess, table, doing, err)
}
