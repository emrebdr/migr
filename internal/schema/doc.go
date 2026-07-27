// Package schema defines the logical type system and the schema snapshot model.
//
// Migrations name logical types (string(n), int64, timestamptz, decimal(p,s),
// uuid, json, enum, array, ...) and never native ones; each driver maps them to
// its own types. Snapshots are driver-neutral and comparable, so an equivalent
// schema on PostgreSQL and MySQL compares equal despite differing native names.
//
// Status: scaffolding. Types in M1-T1, snapshot model in M11-T1 (see TASKS.md).
package schema
