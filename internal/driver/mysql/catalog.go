package mysql

import (
	"context"
	"strconv"
	"strings"
)

// foldedNames is the lower_case_table_names setting under which the server stores names lowercased.
//
// 0 stores and compares a table name exactly as written, which is the default on Linux; 1 lowercases
// it on the way in, which is the default on Windows and what the MySQL images use when told to; 2
// stores it as written and compares it case-insensitively, which is macOS's. Only 1 changes what the
// catalog holds, so only 1 changes what a lookup must ask for -- and asking for the wrong spelling
// means Ensure reports the table absent and tries to create it, forever, on every run. SQLite's
// COLLATE NOCASE finding in a third dialect, arrived at from the opposite direction.
const foldedNames = 1

// caseQuery reads how this server stores an identifier, which decides what its catalog is asked for.
const caseQuery = `SELECT @@global.lower_case_table_names`

// detectStoredName reports the history table's name as this server's catalog holds it.
func detectStoredName(ctx context.Context, c *mysqlConn, table string) (string, error) {
	folding, err := readCaseFolding(ctx, c)
	if err != nil {
		return "", err
	}

	return storedName(table, folding), nil
}

// readCaseFolding asks the server how it stores an identifier, refusing an answer it cannot read.
func readCaseFolding(ctx context.Context, c *mysqlConn) (int, error) {
	var reported string

	if err := c.runner().QueryRowContext(ctx, caseQuery).Scan(&reported); err != nil {
		return 0, wrapConnect("reading lower_case_table_names", err)
	}

	folding, err := strconv.Atoi(strings.TrimSpace(reported))
	if err != nil {
		return 0, wrapConnect("reading lower_case_table_names", err)
	}

	return folding, nil
}

// storedName renders a table name the way the catalog will hold it under one folding setting.
func storedName(table string, folding int) string {
	if folding == foldedNames {
		return strings.ToLower(table)
	}

	return table
}
