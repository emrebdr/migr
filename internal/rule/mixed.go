package rule

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
)

// MixedDataMigration is the id of the rule that reports one migration changing schema and data at once.
const MixedDataMigration ID = "mixed-data-migration"

// mixedDataMigration reports a migration that sends statements of its own beside schema operations.
func mixedDataMigration() Rule {
	return Rule{
		ID:      MixedDataMigration,
		Default: SeverityWarn,
		Summary: "a migration sends statements of its own beside the schema changes it declares",
		Judge:   judgeMixedDataMigration,
	}
}

// judgeMixedDataMigration reports the one shape where a schema change and a data change share a run.
func judgeMixedDataMigration(s Subject) []Finding {
	if len(s.Up.Operations) == 0 || len(s.Up.Direct) == 0 {
		return nil
	}

	return []Finding{{Direction: ir.Up, Detail: fmt.Sprintf("this migration sends %s of its own "+
		"beside %s: those commit on their own before the operations' transaction opens, so a "+
		"failure afterwards leaves them behind and no rollback reaches them -- move the data "+
		"change into a migration of its own", counted(len(s.Up.Direct), "statement"),
		counted(len(s.Up.Operations), "schema operation"))}}
}

// counted renders a count with its noun, since a detail is read as a sentence rather than a field.
func counted(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}

	return fmt.Sprintf("%d %ss", n, noun)
}
