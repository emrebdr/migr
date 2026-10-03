// Package rule holds the lint rules migr validate runs, and what a project decides each is worth.
//
// Status: the framework and two rules (M10-T4). The rest of the set is M10-T5. See
// docs/adr/0066-lint-rules.md.
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
// its operations and the statements the migration sends itself (ADR 0036's other half of what a
// migration does), plus an Undo saying where the down came from. Those five Undo answers are what
// validate.undo already distinguishes, and the distinction is what makes a rule about an *unwritten*
// down expressible at all: CheckReverse reports a migration nothing can undo, which is a different
// statement from one whose author wrote no down and got an inverse derived.
//
// Two things a rule deliberately cannot see:
//
// A database. M10-T5 wants a rule about adding a NOT NULL column with no default to a *non-empty*
// table, and a row count is not something migr validate has: ADR 0064 settled that --connect buys
// the capability set and nothing else, and that migrations are asked over driver.Offline. So that
// rule will report what it can see and say it cannot know whether the table has rows, which is what
// a warning is for.
//
// Another migration. A report names one migration, so a rule judges one -- which means
// missing-primary-key reports a table whose key a later migration adds, and says in its own detail
// that it judged this migration alone. A project doing that sets the rule to warn.
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
// will not ship. That is deliberate (ADR 0066, E): migr up never reads a rule at all, because the
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
