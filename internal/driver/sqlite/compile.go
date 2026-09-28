package sqlite

import (
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// pending is why this driver refuses every operation, and it names the task that stops it refusing.
const pending = "this driver does not compile any operation yet: M8-T2 is where SQLite's SQL is " +
	"written, and until then the declaration and the compiler refuse together rather than one of " +
	"them pretending"

// compile turns an IR operation into the SQLite statements that perform it, or refuses with a reason.
func compile(op ir.Operation) ([]driver.Statement, error) {
	return nil, driver.Unsupported(Name, op.Kind(), pending)
}
