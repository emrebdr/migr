// Package capability models what a driver can do, along two independent axes.
//
// Support answers whether an operation can run at all: Native, Emulated with a
// named strategy, or Unsupported with a human-readable reason. A capability is
// never silently degraded — emulation is surfaced in migr plan output.
//
// Lock impact answers what the operation costs a live database: LockFree,
// BriefExclusive, LongExclusive, or TableRewrite. It is version-dependent and
// must be resolved from the connected server, never hard-coded; an
// undeterminable case reports the pessimistic verdict (CLAUDE.md invariant #13).
//
// This package is where engine-specific knowledge belongs. An
// `if driver == "mysql"` branch in the engine means this model is missing
// something and should be extended instead.
//
// Status: scaffolding. Support axis in M2-T2, lock impact in M13-T1 (see TASKS.md).
package capability
