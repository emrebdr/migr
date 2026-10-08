// Package diff compares two schema snapshots and emits the ordered IR operations
// that close the gap.
//
// Status: complete (M11-T3, M11-T4). The migr diff command is M11-T5. See
// docs/adr/0070-the-snapshot-differ.md and docs/adr/0072-rename-detection.md.
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
// An unconfirmed rename therefore reaches the operations as a drop and a create, which is
// safe because invariant #7 makes both destructive: migr up refuses a generated migration
// that destroys what the project has not permitted. It is also not what the operator wanted,
// which is what the next section is for.
//
// # A rename is proposed, confirmed, and only then emitted
//
// Two snapshots differing by a rename record exactly what two differing by an unrelated drop
// and create record, so there is no reading of the pair that is not a guess -- and acting on
// a wrong one destroys the data a rename would have kept. Detection and emission are
// therefore two calls with a person between them:
//
//	found := diff.Candidates(live, desired, confirmed)   // proposes, changes nothing
//	confirmed = ask(found)                               // internal/cli, M11-T5's to call
//	ops, refused := diff.Diff(live, desired, confirmed)  // told, not asked
//
// Diff takes the answer rather than the asker because invariant #8 forbids a planning path
// whose output depends on anything but its arguments: a confirmer handed to Diff would make
// the same two snapshots diff two ways.
//
// Candidates applies a floor rather than ranking everything, because a person shown ten
// implausible pairs stops reading the plausible one. A column pair must share a type; a table
// pair must have a majority of the table being left accounted for, counting a column matched
// by name as stronger evidence than one matched by type alone. Then pairs are taken greedily,
// best first and one to one, so N losses against M gains ask at most min(N, M) questions.
// What a person is shown is prose rather than a score: "both are string(255), neither accepts
// null, and neither has a default" is checkable against their own intent, and 0.83 is not.
//
// Candidates also takes what is already confirmed, and proposes one round deeper each time it
// is asked. A column rename inside a table that may itself be renamed is a candidate
// conditional on another candidate, which is not a question anybody can answer -- so the
// confirmed renames are applied first and the columns are then matched by name.
//
// Applying one is a pre-pass and nothing else: emit the rename, rewrite the schema being
// changed as the engine would leave it, and hand the rest to a differ that knows nothing
// about any of this. Measured on all five servers, that rewrite is what each one does by
// itself -- an index follows a renamed column and keeps its own name, and a foreign key in
// another table follows a renamed table. A check expression and a view body are engine text
// and are never rewritten here, so one naming a renamed object is proposed as a drop and an
// add, or as a replacement; every server rewrites its own, so that is redundant work rather
// than a wrong result.
//
// A confirmed rename arrives from outside, so it is checked rather than trusted. A source
// this schema does not hold, a target the other does not hold, a source the target still
// holds, a name already taken, and two renames claiming one object are each a Refusal -- and
// the drop and the create are still proposed, so the diff has an answer either way.
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
// so a name moving between tables must be freed before it is claimed. The seventeen kinds
// that are not renames are exactly their own mirror, the pairs sitting at opposite ends, so
// ir.ReverseAll of a list of those is itself validly ordered.
//
// The two renames are the one asymmetry, and the asymmetry is what makes a derived down
// runnable. A rename establishes the name every operation after it speaks, so it comes first
// going forward and ir.ReverseAll puts it last coming back, which is where a rollback needs
// it. Exactly two operations may precede one, and each records something the rename would
// otherwise move out from under it: a dropped view's body names the old table, and a dropped
// check's expression names the old column -- which is also what MySQL 8.0 requires, that
// server refusing to rename a column a check names at all.
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
// And a table renamed at the same time as the columns inside it needs two rounds rather than
// one, since the second round is what the first round's answer unlocks. A caller that asks
// once gets the table rename and takes the column changes as a drop and an add.
package diff
