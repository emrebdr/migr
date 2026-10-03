// Package schema defines the logical type system and the schema snapshot model.
//
// Migrations name logical types and never native ones; each driver maps them to its
// own. Snapshots are driver-neutral and comparable, so an equivalent schema on
// PostgreSQL and MySQL compares equal despite differing native names.
//
// Status: types complete (M1-T1); snapshot model complete (M11-T1). Per-driver
// introspection, and Conn.Introspect itself, are M11-T2; the differ is M11-T3. See
// docs/adr/0065-the-schema-snapshot.md.
//
// # Grammar
//
//	type := ident [ "(" arg { "," arg } ")" ]
//
// Fourteen kinds stand alone — text, bool, int16, int32, int64, float32, float64,
// uuid, json, timestamp, timestamptz, date, time, binary. Four take parameters:
// string(n), decimal(p,s), enum(values...), and array(of), whose argument is itself
// a full type.
//
// Enum values are letters, digits, underscore, and hyphen, and their order is part of
// the type: PostgreSQL compares enum labels by declaration order, so enum(a,b) and
// enum(b,a) are different types.
//
// # The canonical string is the wire format
//
// A Type marshals to exactly the string it parses from, so the IR carries
// "decimal(10,2)" rather than a nested object. One representation means golden files
// stay readable, plan output needs no translation, and there is a single code path
// that can be wrong. See docs/adr/0005-logical-type-encoding.md.
//
// That places real weight on uniqueness: every type has exactly one spelling. Two
// SDKs emitting different bytes for the same schema would break the byte-identical-IR
// conformance claim that makes language independence testable, so parsing normalizes
// — whitespace around parameters is trimmed — while String always emits the tight
// form. Keywords are lowercase; an uppercase name is rejected with the canonical
// spelling as a hint rather than quietly accepted as a second way to say the same
// thing.
//
// # Invalid states are unrepresentable
//
// Type's fields are unexported and there are no setters, so every value comes from a
// constructor that has already validated it. A zero Type is not a type: it renders as
// <invalid> and refuses to marshal, so it cannot reach an IR document as an empty
// string. Because Type holds a slice, == does not compile; Equal compares canonical
// strings, which is exact precisely because that form is unique.
//
// # What is deliberately not checked here
//
// Upper bounds. PostgreSQL caps decimal precision at 1000 and MySQL at 65; varchar
// limits differ again. Those are capability questions, and CLAUDE.md puts engine
// differences in the capability model rather than in a type system that has to hold
// for every engine at once. Likewise array(array(int32)) parses: whether an engine
// supports it is for that engine's driver to declare.
//
// Array nesting is bounded only to keep hostile input from recursing away — IR
// arrives over a pipe from a subprocess, so a corrupt array(array(array(...))) must
// produce an error rather than a stack overflow.
//
// # A Snapshot is one namespace, in the operation set's own vocabulary
//
// A Snapshot holds every table and every view of exactly one namespace, and names that
// namespace nowhere. That is what the operations describe: ir.CreateTable carries a bare
// table name and nothing qualifies it. A PostgreSQL schema, a MySQL database and a SQLite
// file are each one snapshot, and which one is the caller's question.
//
// Table, Column and the five constraint types mirror the IR's own payloads -- ir.TableDef,
// ir.ColumnsDef, ir.ForeignKeyDef, ir.CheckDef -- deliberately, because that is what makes
// the differ total over the twenty operations and a projection of pending operations onto a
// schema mechanical. What the model holds is what those operations can create, plus an honest
// marker for everything else.
//
// This package cannot import internal/ir, and never will: ir imports it for schema.Type, so
// the dependency runs one way and the reverse is a cycle. Two consequences are visible here.
// The neutral vocabularies both halves need -- Function and ReferentialAction -- live in this
// package and ir aliases them, so there is one definition of each name. And ColumnDefault is
// *not* ir.Default: that type carries a literal or a neutral function and nothing else,
// because the IR never carries engine SQL, while a catalog holds nextval('users_id_seq').
// Widening ir.Default would have weakened an IR invariant to serve introspection.
//
// # A column's type is not always knowable, so the model says which
//
// No driver's native type map is injective, so the reverse direction cannot always answer
// exactly. Measured against all four servers in test/docker-compose.yml:
//
//	SQLite     int16, int32, int64 all declare as integer; float32 and float64 as real;
//	           text, json and enum all as text
//	MariaDB    json declares as longtext, which is also what text declares as
//	MySQL      timestamp and timestamptz both declare as datetime(6)
//	PostgreSQL all eighteen are distinct, and only because it refuses enum outright
//
// So ColumnType carries three states. CertaintyExact names one logical type and no other;
// CertaintyAmbiguous names the best of several; CertaintyUnknown names none at all, which is
// what a tsvector, a SET, a POINT or SQLite's untyped column gets. Every state records the
// engine's own spelling, because the catalog does not echo the driver's: varchar(255) reads
// back as character varying(255) on PostgreSQL, text as TEXT on SQLite, and int as int(11) on
// MariaDB, so a reverse map keyed on what the compiler emitted is wrong on every engine.
//
// Equal is where that honesty is enforced. Two exact types compare on the logical type alone,
// which is what lets a PostgreSQL integer equal a MySQL int. Anything inexact compares on the
// certainty, the guess and the native spelling together -- so a guess never equals a fact. A
// caller that must act asks Exact first and refuses rather than guessing, which is the answer
// capability.LockUnknown and validate's Unchecked already give one subsystem over.
//
// # What a catalog holds that the operation set cannot express
//
// A real database holds more than twenty operations can create. Measured: a partial index, an
// expression index, a prefix length, a descending index, a generated column, an identity or
// AUTO_INCREMENT column, a per-column collation, a deferrable foreign key, WITHOUT ROWID.
// Each is accepted by at least one server and spellable by none of the operations.
//
// Every element can carry an Unrepresented marker, which holds text and nothing a caller could
// mistake for a payload to act on. Omitting such an object instead would make M11-T6's empty
// diff partly a statement about what the snapshot declined to look at, and would have a differ
// propose dropping and recreating a partial index as a plain one. The marker counts in
// equality, so a round trip notices one appearing or vanishing.
//
// Unrepresentable lists every one of them in canonical order, so the rule is reachable rather
// than remembered: M11-T3 refuses off that list and migr diff prints off it, the way
// internal/engine's Destructive and Unsupported are read by a report and a run alike.
//
// The sharpest case is a generated column, which is invisible unless it is marked. SQLite's
// pragma_table_info omits one entirely -- it shows only in table_xinfo, hidden = 2 -- and
// PostgreSQL reports it as an ordinary integer column whose default is (nullable_one * 2). An
// unmarked snapshot would therefore have a differ either re-add a column that exists or try to
// alter one no engine will let it change.
//
// # What compares, and what a name cannot decide
//
// Canonical is what comparison is defined over. Tables, views, columns, indexes and
// constraints are sets, sorted by name: what a schema holds, never the order it holds it in.
// Within a key, an index or a foreign key, column order is kept, because that order is part of
// the object. The line between the two is the IR's own: M9-T7 measured that a reversed
// drop_column appends the column and no v1 operation can put it back where it was, MySQL's
// AFTER clause having no field in the operation set. A column's position is a fact nothing
// could act on, so representing it would invite a differ to emit the impossible.
//
// Two names are recorded and are not part of equality, both because no two engines agree on
// one:
//
//	primary key   PostgreSQL names it after the table, MySQL always calls it PRIMARY and
//	              discards a name given to it, and SQLite gives an integer primary key no
//	              name at all -- it is the rowid, with no index to name
//	foreign key   SQLite's pragma_foreign_key_list answers the referenced table, the columns
//	              and both actions, and no name whatsoever
//
// So a primary key compares by its columns and a foreign key by its shape. A differ still
// reads both names, because drop_primary_key and drop_foreign_key need one. ir.AddPrimaryKey
// had already made Name optional for the MySQL half of this.
//
// Three things are engine text and compare as the text they are, not as anything portable:
//
//	a view's body        asked for SELECT * FROM t, PostgreSQL stores an expanded column list
//	                     with a trailing semicolon, MySQL stores one line qualified with the
//	                     database's own name, SQLite stores what the author wrote
//	a check expression   nullable_one > 0 reads back as (`nullable_one` > 0) from MySQL,
//	                     `nullable_one` > 0 from MariaDB, (nullable_one > 0) from PostgreSQL,
//	                     and verbatim from SQLite
//	a default expression the literal x reads back as 'x'::character varying, x, 'x' and 'x'
//	                     from the four servers respectively
//
// ir.CreateView already says its query is engine SQL that does not port; the other two are the
// same fact found in two more places. Nothing here can know that curdate() and CURRENT_DATE
// agree, and claiming otherwise is what a snapshot must not do.
//
// One difference is normalized rather than reported, because it is a spelling and not a fact:
// an unnamed referential action resolves to no_action. PostgreSQL and MySQL omit the clause
// entirely where SQLite answers NO ACTION outright, so two spellings of one default would make
// every foreign key compare unequal across those engines.
//
// A table with no columns is valid here, where ir.TableDef requires at least one. PostgreSQL
// permits a relation with zero columns and drop_column can reach that state -- which is why
// pgtest lists relations as well as describing them. A snapshot reports what the engine holds,
// so refusing it would make a real database impossible to introspect.
//
// # What is deliberately not represented
//
// Column position, for the reason above. Storage parameters, tablespaces and fillfactor. A
// table's character set and collation. An index's method. Triggers, sequences and routines,
// which are outside the five object classes this model covers at all. Each is either beyond
// the v1 operation set or beyond the task, and the distinction matters to whoever extends this.
//
// # Serializable, and unversioned
//
// Marshal and Unmarshal round-trip a snapshot through JSON in canonical order, refusing a
// member this build does not know -- the same strictness ir.Unmarshal has, for the same reason.
//
// There is no version field, which is deliberate rather than deferred. The IR and the protocol
// carry one because they cross a boundary, between two processes or two languages or into a
// checksum, and invariant #5 governs exactly those. A snapshot is produced by a driver and read
// by the engine in one process, is hashed by nothing, and is persisted nowhere. What would
// force a version: a snapshot sent over the runtime protocol, or one stored in the history
// table as a baseline for a database whose earlier migrations migr did not run.
//
// # What M11-T2 inherits from here
//
// Three measurements that are a driver's problem rather than this model's. Two engines report a
// unique constraint and a unique index as one object -- MySQL implements the former as a UNIQUE
// KEY, and this project's SQLite driver compiles add_unique to CREATE UNIQUE INDEX -- so
// introspection there must choose which element to report, and the model cannot choose for it.
// Two engines attach a constraint nobody asked for, which a naive snapshot would report as real
// and a differ would then propose dropping: MariaDB adds json_valid() to a json column, and
// this project's SQLite driver renders an enum as an unnamed CHECK ... IN (...). And MySQL
// creates an index for a foreign key over an unindexed column, named after the constraint.
package schema
