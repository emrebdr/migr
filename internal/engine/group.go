package engine

import "github.com/emrebdr/migr/internal/driver"

// Group is a run of consecutive statements that agree on whether they can share a transaction.
type Group struct {
	// Statements are the statements this run executes, in the order the driver compiled them.
	Statements []driver.Statement
	// Transactional reports whether this run executes inside one transaction.
	Transactional bool
}

// Groups splits statements into consecutive runs that agree on their transaction requirement.
func Groups(stmts []driver.Statement) []Group {
	var groups []Group

	for _, stmt := range stmts {
		if opensGroup(groups, stmt) {
			groups = append(groups, Group{Transactional: transactional(stmt)})
		}

		last := len(groups) - 1
		groups[last].Statements = append(groups[last].Statements, stmt)
	}

	return groups
}

// opensGroup reports whether a statement starts a run rather than continuing the current one.
func opensGroup(groups []Group, stmt driver.Statement) bool {
	if len(groups) == 0 {
		return true
	}

	return groups[len(groups)-1].Transactional != transactional(stmt)
}

// transactional reports whether a statement may run inside a transaction.
func transactional(stmt driver.Statement) bool {
	return !stmt.NoTransaction
}
