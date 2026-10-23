// Package rule holds the lint rules migr validate runs, and what a project decides each is worth.
//
// Status: the framework and two rules (M10-T4), then the three that complete the set (M10-T5). Each
// rule's own doc comment states what it reports and what it declines to. migr config prints the five with their
// resolved severities, which is how an operator discovers them without reading this.
//
// # A rule is data with one function in it
//
// Rule carries an ID, a Default severity, a one-line Summary and a Judge. Three of the four are
// facts about the rule rather than behavior, which is why this is a struct with a func field and
// not an interface: adding a rule is appending a value to builtIn and writing one file beside the
// others, with no switch to extend and no init to remember. capability.Rule is the same shape one
// subsystem over.
//
// Nothing outside this binary can add one. M15 gives a plugin protocol to drivers (M15-T1) and to
// nothing else, so "pluggable" here means a value a later task appends, and a registry with an
// error return would be ceremony around a slice literal.
//
// NewSet refuses a set that cannot mean anything: a blank or malformed id, a duplicate, a nil
// Judge, a blank Summary, an invalid default, and a configured id no rule answers to. The last of
// those is load-bearing rather than tidy -- see "off is not a permission" below.
//
// # What a rule judges, and what it cannot see
//
// Subject is one migration as internal/validate has already read it: both halves, each carrying
// its operations and the statements the migration sends itself (the escape hatch, the other half of
// what a migration does), plus an Undo saying where the down came from. Those five Undo answers are what
// validate.undo already distinguishes, and the distinction is what makes a rule about an *unwritten*
// down expressible at all: CheckReverse reports a migration nothing can undo, which is a different
// statement from one whose author wrote no down and got an inverse derived.
//
// Two things a rule deliberately cannot see:
//
// A database. not-null-without-default is about adding a NOT NULL column with no default to a
// *non-empty* table, and a row count is not something migr validate has: --connect
// buys the capability set and nothing else, and that migrations are asked over
// driver.Offline. So the rule reports what it can see and says in its own detail that it could not
// count the table's rows, which is what a warning is for. There is exactly one case where it does
// know: a table *this migration creates* holds nothing, so a column added to one is never reported.
// That is the only inference about data anything here makes, and it is structural rather than a
// guess.
//
// Another migration. A report names one migration, so a rule judges one -- which means
// missing-primary-key reports a table whose key a later migration adds, and says in its own detail
// that it judged this migration alone. A project doing that sets the rule to warn.
//
// # No rule can fire on a .sql project, and that is now three facts rather than one
//
// Every statement in that format reaches the database as ir.Raw, which carries no columns and no
// key -- so missing-primary-key, unbounded-string and not-null-without-default have nothing to read.
// mixed-data-migration is silent because Direct comes from host.db.exec, which only a runtime SDK
// can call. And missing-down is silent because such a project never reaches UndoDerived at all: a
// raw operation has no inverse, so deriving one always fails and CheckReverse is what reports it.
// A test constructs all three shapes a .sql migration can take rather than asserting the sentence.
//
// # The three severities
//
// error makes a finding a problem, so migr validate exits 2. warn makes it a warning, which is
// printed and changes no exit code. off stops the rule from running at all -- Judge is never
// called, which a test holds with a rule that records being asked.
//
// Judgements come back sorted by rule id rather than in declaration order, so two runs over one
// migration report in one order (invariant #8).
//
// # error is a weaker word here than elsewhere in migr
//
// Everything else that exits 2 is wrong: a filename the grammar refuses, an operation no server
// will perform, a migration nothing can undo. A rule at error is something this project decided it
// will not ship. That is deliberate: migr up never reads a rule at all, because the
// run's gates -- drift, destruction, capability -- are each about whether an operation is correct or
// permitted, and refusing a deploy over a missing primary key is how a project learns to switch
// everything off.
//
// # off is not a permission
//
// M10-T4's note is that off must not become a second way to say what
// validation.allow_destructive says. Three things keep it from becoming one, and the middle one is
// the mechanism that matters:
//
// Destruction is not a rule. It stays validate.CheckDestructive, read from engine.Destructive by
// the validator and by the run, so no severity can reach it.
//
// An unknown rule id fails the config load, naming every id that exists. The failure mode was never
// a rule *about* destruction -- it was a line in migr.yaml that reads like permission and silently
// does nothing, and `rules: {drop_table: off}` is now a refusal.
//
// A test holds the sentence: every rule off, an unpermitted drop_table, and migr validate still
// reports destructive while migr up still exits 2 having run nothing.
package rule
