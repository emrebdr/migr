// Package diff compares two schema snapshots and emits the ordered IR operations
// that close the gap.
//
// Status: complete (M11-T3). Rename detection is M11-T4 and the migr diff command is
// M11-T5. See docs/adr/0070-the-snapshot-differ.md.
//
// # What it is a function of
//
// Diff takes two snapshots and returns operations plus the changes it would not propose.
// It is pure and deterministic -- both sides are canonicalized and nothing iterates a map
// -- and it returns no error, because a malformed snapshot is Snapshot.Validate's question
// and everything else it meets is a refusal rather than a failure. That is the shape
// engine.Destructive, engine.Unrestorable and schema.Unrepresentable already have: one
// policy, read by a report and by a command alike.
//
// Neither side is privileged. Nothing here knows which snapshot came from a live database
// and which describes a target, so every message says "before" and "after" rather than
// naming one of them as the truth.
//
// # What is matched, and how
//
// Tables, columns and views are matched by name, because add_column, drop_column,
// alter_column and create_view ... or_replace exist, so a matched pair can be changed
// rather than replaced. The five constraint kinds are set differences under the model's
// own Equal: there is no alter_index, so a changed index is a drop and an add. Two of
// those Equal methods ignore a name on purpose -- a primary key's and a foreign key's,
// because no two engines agree on one (ADR 0065) -- which is what stops a diff proposing
// to rebuild every key it can see.
//
// A rename therefore reaches the operations as a drop and a create, which is what M11-T4
// exists to fix. Until then invariant #7 is the net: drop_table and drop_column are
// destructive, so migr up refuses a generated migration that destroys what the project has
// not permitted.
//
// # Every operation it emits carries a complete from payload
//
// Nine operations can record what they replace (ADR 0008), and a differ is the one producer
// that knows both states exactly -- so it fills every one of them, and a migration migr diff
// writes is reversible by derivation. That is also why a drop of something the operations
// cannot express is refused rather than emitted bare: a drop_index carrying the columns of a
// partial index recreates it as a plain one at the next migr down, which is the degradation
// the marker exists to prevent, arriving a milestone later.
//
// # The two things that are refused on
//
// An Unrepresented marker and a column type's certainty, read in express.go and nowhere
// else. The marker is read as a lookup rather than as a report: an element that carries one
// and is identical in both schemas produces no operation, so it produces no refusal either,
// and diffing a database against itself is empty however much of it migr could not have
// created. A caller that wants the whole picture calls schema.Unrepresentable, which is
// exported for exactly that.
//
// A type is compared on the logical answer, which is the only part anything can act on. Two
// guesses that agree propose nothing, the best answer available being that the column is
// unchanged; two that disagree are refused, because the operation would carry a target type
// nothing verified and record a prior type that is itself a guess. Creating is not changing:
// a new column is emitted off the logical answer however certain it is, there being nothing
// to compare it against and no data at risk. A type that names no logical type at all is the
// floor in both directions.
//
// The other refusals are each a field the IR does not have: a default that is engine SQL, a
// check or foreign key the engine names nothing, an index carrying Unique, and a table with
// no columns -- which schema.Table permits and ir.CreateTable does not, PostgreSQL being
// able to reach that state.
//
// A refusal never leaves a dependent operation behind. The differ keeps a names-only model
// of the target state and consults it before every constraint, so a refused create_table
// takes that table's indexes and keys with it. Names and nothing else: that is deliberately
// not the IR-onto-snapshot projection M10-T3 needs, and must not grow into one here.
//
// # The order
//
// One fixed order in order.go rather than a topological sort, because no operation carries a
// foreign key inline: every real dependency between two operations is a boundary between two
// kinds. Drops come before creates because an index name is namespace-scoped on PostgreSQL,
// so a name moving between tables must be freed before it is claimed. And the order is its
// own mirror, the pairs sitting at opposite ends, so ir.ReverseAll of an emitted list is
// itself validly ordered.
//
// A dropped table's own indexes, uniques, checks and foreign keys are dropped by name first,
// even though every engine takes them with the table. A recorded table definition holds
// columns and a primary key and nothing else, so a bare drop_table is a migration whose
// inverse rebuilds a different table -- which internal/engine's implied gate refuses, and
// rightly.
//
// # Two limitations, measured rather than assumed
//
// A view's body and a check's expression are engine text (ADR 0065), so diffing two
// different engines proposes replacing every view and every check. Nothing here can know
// that an expanded column list and a SELECT * are the same query.
//
// And a list this differ emits is not always a list migr up can run today. plan.Planner
// compiles every operation before any of them executes, and an emulated one is compiled
// against the database (ADR 0053) -- so on SQLite an add_foreign_key over a table an earlier
// create_table in the same list creates is expanded against a catalog that has no such
// table. Measured: "cannot rebuild this table: this database has no such table". The
// operations and their order are right; M11-T7 is what makes them runnable.
package diff
