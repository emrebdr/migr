// Package visual renders a project's migrations as a picture: the dependency DAG they declare, and
// the schema they build, one migration at a time.
//
// Status: complete (M15-T7).
//
// # What it is a function of
//
// Both views are pure functions of what a run already reads. DAG takes the migrations in the order
// migration.Order settled, their declarations, and the gates migration.Gates resolved; Evolve takes
// each migration's up operations and folds them forward through internal/fold, which is the
// projection migr diff makes. Nothing here opens a database, starts a runtime, or reads a clock, so
// the same project renders the same bytes every time (invariant #8). Every map is read through a
// sorted slice of its keys or through the ordered migrations, never iterated.
//
// # Where the schema stops being known
//
// The evolution says where it cannot see rather than guessing past it, for the reasons migr diff
// refuses a project whole: a raw operation is engine SQL whose effect nothing can read, a migration
// that reaches the database has no operations a run without one can trust, and an operation the fold
// refuses leaves a schema no engine produces. The step that ends the projection carries the reason,
// every later step still lists its operations, and no schema is drawn for a moment past it.
//
// # The three renderings
//
// Text is for a terminal. Mermaid is what GitHub and most markdown sites draw from a fenced block,
// and DOT is Graphviz's. The DAG keeps the run order in both diagram languages with an invisible
// link between consecutive migrations, so a migration nothing constrains still sits where it runs
// rather than floating beside the graph. An ER diagram draws the schema at one moment: tables, their
// columns with the logical type, the key, and every foreign key as an edge.
//
// Mermaid's ER grammar accepts a type made of letters, digits, hyphens, underscores, parentheses and
// square brackets alone, so decimal(10,2) and enum(a,b) cannot be written as types. Such a column is
// drawn under its type's kind, with its full spelling in the attribute's comment, rather than under
// an invented spelling that reads like a type migr has.
package visual
