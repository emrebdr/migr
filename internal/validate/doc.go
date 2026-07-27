// Package validate runs pre-execution checks: destructive-operation detection,
// capability preflight against the target driver, reference integrity, and
// dependency completeness.
//
// Validation reports every problem it finds rather than stopping at the first,
// and runs without a database connection wherever possible.
//
// Status: scaffolding. Implemented across M10 (see TASKS.md).
package validate
