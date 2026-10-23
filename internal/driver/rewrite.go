package driver

import (
	"slices"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

// RewriteName names one way a driver performs an operation more safely, which safety.rewrites switches.
type RewriteName string

// The rewrites a driver may answer with, each one a key under safety.rewrites.
const (
	// RewriteNotValid adds a check or foreign key NOT VALID and validates it on its own.
	RewriteNotValid RewriteName = "not_valid"
	// RewriteNotNullCheck sets NOT NULL behind a validated check, so the server need not scan.
	RewriteNotNullCheck RewriteName = "not_null_check"
	// RewriteConcurrentIndex builds an index without blocking writes.
	RewriteConcurrentIndex RewriteName = "concurrent_index"
)

// rewriteNames lists every rewrite this build knows, in the order a report lists them.
var rewriteNames = []RewriteName{RewriteNotValid, RewriteNotNullCheck, RewriteConcurrentIndex}

// RewriteNames returns every rewrite this build knows, as a copy.
func RewriteNames() []RewriteName {
	return slices.Clone(rewriteNames)
}

// Valid reports whether this build knows the rewrite.
func (n RewriteName) Valid() bool {
	return slices.Contains(rewriteNames, n)
}

// Rewrite is a safer sequence one operation compiles to on this server, and why.
type Rewrite struct {
	// Name is which rewrite this is, the key that switches it off.
	Name RewriteName
	// Statements are what the operation runs as instead, in order.
	Statements []Statement
	// Reason says in one sentence what the sequence avoids.
	Reason string
	// Lock is what the sequence costs a live database, where the plain statement costs more.
	Lock capability.LockImpact
}

// Rewriter is a session that performs an operation more safely than its plain compilation, reading nothing.
type Rewriter interface {
	// Rewrite returns a safer way to perform op, and false where this server has none.
	Rewrite(op ir.Operation) (Rewrite, bool)
}
