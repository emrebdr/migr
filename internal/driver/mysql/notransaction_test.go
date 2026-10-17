package mysql

import (
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestARawStatementCarriesItsTransactionOptOutAndNoOther(t *testing.T) {
	for _, declared := range []bool{false, true} {
		stmts, err := compile(ir.Raw{Driver: Name, SQL: "SELECT 1", NoTransaction: declared})
		if err != nil {
			t.Fatalf("compiling a raw statement: %v", err)
		}

		if len(stmts) != 1 || stmts[0].NoTransaction != declared {
			t.Errorf("a raw declaring no_transaction %v compiled to %+v", declared, stmts)
		}
	}

	t.Log("The flag is a fact about the statement the author wrote, so the driver carries it as")
	t.Log("given: grouping it into runs is internal/engine's, never a driver's (ADR 0084).")
}
