package rule

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
)

// MissingPrimaryKey is the id of the rule that reports a table created without a primary key.
const MissingPrimaryKey ID = "missing-primary-key"

// missingPrimaryKey reports a table this migration creates and declares no primary key for.
func missingPrimaryKey() Rule {
	return Rule{
		ID:      MissingPrimaryKey,
		Default: SeverityError,
		Summary: "a table is created without a primary key",
		Judge:   judgeMissingPrimaryKey,
	}
}

// judgeMissingPrimaryKey reports every table created with no key, in the order they were created.
func judgeMissingPrimaryKey(s Subject) []Finding {
	keyed := keyedIn(s.Up.Operations)

	var found []Finding

	for _, op := range s.Up.Numbered(ir.Up) {
		created, creating := op.Op.(ir.CreateTable)
		if !creating || len(created.PrimaryKey) > 0 || keyed[created.Table] {
			continue
		}

		found = append(found, op.Finding(fmt.Sprintf("table %q is created with no primary key, "+
			"and nothing in this migration adds one: rows have no stable identity, and an online "+
			"copy of the table cannot be kept in sync without a key", created.Table)))
	}

	return found
}

// keyedIn names every table one half gives a primary key to as an operation of its own.
func keyedIn(ops ir.Operations) map[string]bool {
	keyed := make(map[string]bool)

	for _, op := range ops {
		if added, adding := op.(ir.AddPrimaryKey); adding {
			keyed[added.Table] = true
		}
	}

	return keyed
}
