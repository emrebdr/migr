// Package config loads and validates migr.yaml: migration source settings,
// database definitions with ${VAR} environment expansion, history and lock
// configuration, and the safety thresholds that gate zero-downtime checks.
//
// Unknown keys are an error rather than a silent no-op, and missing required
// fields are reported with the offending YAML path.
//
// # Pipeline
//
//	read -> parse to AST -> expand ${VAR} -> decode (strict) -> defaults -> validate
//
// Expansion runs against the parsed AST, never the raw bytes. Substituting text
// before the parser runs would let an environment value change the document's
// structure: a value containing a newline or an unquoted colon can inject or
// truncate keys, and these values routinely come from CI secrets. Rewriting
// string scalars in an already-parsed tree cannot alter structure.
//
// Validation accumulates every problem rather than stopping at the first, so a
// misconfigured file is fixed in one pass instead of one error at a time.
//
// # Paths follow their config file
//
// A relative migrations.dir is anchored to the directory migr.yaml sits in,
// never to the working directory, so a config names its own migrations from
// anywhere. Read the other way, `migr --config ../other/migr.yaml up` would
// apply whatever ./migrations happened to be beside the operator to the other
// project's database. The default is unchanged — filepath.Dir("migr.yaml") is
// "." — and an absolute path is left where it points. Nothing read the field
// until M4-T1, which is why nothing had noticed.
//
// Duration implements the func form of UnmarshalYAML rather than the []byte
// form deliberately: the []byte form receives the node's raw source, trailing
// comment and all, so "30s  # wait for the lock" would reach time.ParseDuration
// intact.
//
// # SQL is a language, not a special case
//
// migrations.language admits sql alongside go and typescript, and M5-T1 added it
// rather than teaching the CLI to write a value this loader refuses. M5's own
// goal is SQL-file migrations as the interim source format until M6 lands the
// SDKs, and engine.Options.Runtime is documented as the language "spelled as
// migr.yaml spells it" -- so a format with no spelling here is one nothing can
// be asked to run.
//
// It is a real value and not a placeholder: a .sql file reaches the database as
// ir.Raw, which carries a driver name, so such a migration is engine-specific
// and has no inverse the framework can derive. That is a property of the format
// worth stating where it is selected, not a reason to hide the selection. See
// docs/adr/0025-sql-source-language.md.
//
// # Lock impact
//
// internal/capability owns the LockImpact verdicts from M2-T2 onward; the four
// spellings this package froze in its testdata are the ones it adopted.
// safety.max_lock_impact validates against capability.Ceilings rather than a
// list kept here, so a verdict renamed on either side fails a test instead of
// leaving migr.yaml accepting a word no driver reports.
//
// Ceilings is deliberately narrower than the full verdict set: a driver may
// report an unresolved lock impact, but "block anything worse than unknown" is
// not a threshold with a meaning, so max_lock_impact: unknown is refused by the
// same path as a typo.
//
// capability.LockImpact deliberately implements no encoding.TextUnmarshaler.
// goccy honors one during decode, which runs before the validator below, so an
// unrecognized value would abort the parse rather than joining the accumulated
// report — costing the "every problem at once" property that
// TestMissingRequiredFieldsAreAllReportedWithPaths and
// TestValidationErrorReportsEveryProblemAtOnce exist to hold.
//
// # Secrets
//
// Connection strings are held in a Secret, whose String, MarshalYAML, and
// MarshalJSON all redact. Printing, logging, or serializing a config is
// therefore safe by default, and obtaining the usable value requires a
// deliberate call to Reveal.
//
// Redaction checks for "://" before treating a DSN as a URL. That test is not
// redundant: url.Parse reads a driver-native DSN such as "user:pass@tcp(host)/db"
// as scheme "user" with an opaque remainder, so the URL branch would find no
// credentials and return the password intact.
package config
