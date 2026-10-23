// Package source turns IR operations back into the migration source a language
// writes them as, which is what lets migr diff write a migration file.
//
// Status: complete (M11-T5).
//
// # One chain, a writer per language
//
// Chains turns operations into Chains of Calls with typed Args -- a name, a column
// list, a logical type, a default, a referential action, a recorded payload. Each
// language's writer spells them: internal/source/golang and
// internal/source/typescript. The two SDKs are the same shape with different
// spelling, so what is not shared is only ever how something is written down:
//
//	s.Table("users").AddColumn("plan", migr.String(32)).Null().Apply(ctx)
//	await s.table("users").addColumn("plan", t.string(32)).null().apply();
//
// A third language is a writer plus an eighteen-row table of type names, a prefix
// and a quoter -- not a second renderer. Four of the eighteen logical types take
// parameters and the rest stand alone, which is the whole of the shape a writer
// has to know, and Type here is what knows it.
//
// Each writer produces a whole file rather than a body, because that is what a
// command writing a migration needs. The file carries an up half and no down: every
// operation a differ emits records what it replaced, so migr derives the inverse.
//
// # It writes the recorded payloads, and that is the point
//
// The differ's stated payoff is that a migration migr diff writes is reversible by
// derivation. The differ declines to emit a drop whose prior definition it cannot
// record precisely so that the derived down is exact -- so a writer that dropped
// .Was(...) would spend that for nothing and write migrations this project's own
// migr validate rejects.
//
// # What is not here
//
// There is no sql writer. Every .sql migration reaches the database as ir.Raw, so a
// project in that format has no foldable desired schema and migr diff refuses it
// before anything reaches a renderer -- a backend for it would be unreachable.
// raw is the one operation Chains refuses outright, for the same reason: a schema
// builder has no call for engine SQL.
//
// # Two per-language details that are not arbitrary
//
// The Go writer's output is held to being byte-exactly what gofmt writes, because a
// generated file people read and edit has to arrive formatted. The TypeScript
// writer's is deliberately not held to prettier, whose line breaking is
// width-driven: matching it means reproducing it, and a generated file lands in
// somebody else's project where their own formatter is the one that reformats it.
// What is asserted there instead is that it typechecks against the published
// package, by self-reference through the same exports map a consumer resolves.
//
// And a generated Go file's identifier carries the version rather than the name.
// Every Go migration in a project shares one package and the default name is the
// same for every diff, so two generated migrations named from the name are a
// project that does not build. TypeScript needs none of this, each migration being
// a module with a default export -- "the module system replaces the registry",
// reached from a new direction.
package source
