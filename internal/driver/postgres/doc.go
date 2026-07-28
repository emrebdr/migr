// Package postgres implements the Driver contract for PostgreSQL using pgx.
//
// PostgreSQL is the reference driver: it supports transactional DDL and most operations
// natively, which is why the engine is built against it first. It provides advisory locking, and
// its lock-impact verdicts are keyed to the detected server version — ADD COLUMN with a default
// rewrites the table on PostgreSQL 10 and is instant from 11 onward.
//
// Status: connections and compilation complete (M3-T1, M3-T2). This driver locks nothing and
// keeps no history yet; see pending.go, which M3-T4 and M3-T6 each delete a piece of. Lock impact
// is declared unknown until M3-T3 measures it.
//
// # The declaration is derived from the compiler
//
// A driver states what it supports twice — Compile refuses with a driver.UnsupportedError, the
// capability set refuses with a Verdict — and the conformance suite asserts the biconditional:
// Resolve(op).Allowed() is false exactly when Compile refuses. Here the rule calls the compiler,
// so the two cannot drift and the suite proves the wiring rather than a coincidence.
//
// Callers are unaffected: migr validate and migr plan read the declaration before anything
// touches a database, so the declaration is still what is consulted. Which of the two is derived
// is an implementation detail of this driver, and it does not generalize — SQLite must declare
// Emulated with a named strategy, and a Compile that succeeded says nothing about how. See
// ADR 0015.
//
// rules() needs no connection, so the conformance suite runs untagged against a session double
// carrying the same declaration, and the agreement is a make test and CI gate rather than one
// only Docker reaches. The integration run stays: it holds the declaration a live server produced
// to what that server's driver compiles, which is a different claim.
//
// # Refusals are per payload, not per kind
//
// PostgreSQL supports create_table. It does not support the one with an enum column. Five refusals
// have that shape, and capability.Rule takes the whole operation so the declaration can say the
// same thing:
//
//   - an enum type, which needs a CREATE TYPE object no v1 operation owns — nothing would drop it,
//     so apply, roll back, and apply again fails on a type that already exists (M3-T3)
//   - a nested array, because PostgreSQL declares array dimensions and does not enforce them:
//     integer[][] and integer[] are one type, so two logical types would become one native one
//   - a drop_primary_key naming no constraint, because PostgreSQL has no name-free form; the v1 IR
//     leaves the name optional because MySQL has no name for a primary key
//   - an identifier over 63 bytes, which PostgreSQL truncates with a notice rather than an error,
//     so two long names silently become one object
//   - an identifier, literal, or embedded fragment holding a NUL, which ends the statement where it
//     appears, because a query reaches the server as a C string
//
// Every one is a silent-wrong rather than a loud failure, which is why each is refused rather than
// left to the server. A raw operation addressed to another engine is the sixth, and the one the
// conformance suite already required of every driver.
//
// # Identifiers are always quoted
//
// Double quotes, with an embedded quote doubled, unconditionally. One rule covers both halves of
// what M3-T2 asked for: "order" is not a keyword and "userID" does not fold to userid. A
// reserved-word list would need updating every release and would be silently wrong in between.
// Nothing is schema-qualified — Options.Schema sets search_path at Open, and qualifying as well
// would make one migration mean different things depending on which mechanism an operator used.
//
// # Logical types
//
//	string(n)   varchar(n)     int16/32/64   smallint/integer/bigint   uuid        uuid
//	text        text           float32/64    real/double precision     json        jsonb
//	bool        boolean        decimal(p,s)  numeric(p,s)              binary      bytea
//	date        date           timestamp     timestamp                 array(of)   <of>[]
//	time        time           timestamptz   timestamptz               enum(...)   refused
//
// json maps to jsonb rather than json: PostgreSQL's json type stores the source text, preserving
// whitespace, key order, and duplicate keys, so two equal documents are different values and
// nobody indexes it. A schema type should be the canonical one.
//
// Default functions map to CURRENT_TIMESTAMP, CURRENT_DATE, CURRENT_TIME, and gen_random_uuid().
// That last one is core only from PostgreSQL 13, and this driver's minimum is 10 — Compile is pure
// by contract and cannot know which server a session reached, so the version-dependent refusal can
// only live in the declaration, which does. M3-T3 inherits the choice; ADR 0015 records it.
//
// # What this driver does not emit
//
// No USING clause on ALTER COLUMN ... TYPE: a cast that does not exist should fail loudly rather
// than be coerced into one that silently loses data. No CONCURRENTLY on index creation, which is
// M13-T11's, built on the executor M4-T5 designs to split a migration around a statement that
// cannot run in a transaction. So nothing here sets Statement.NoTransaction, and the golden
// format's no-transaction header stays unexercised until then.
//
// alter_column emits one statement per requested change, in a fixed order: type, then nullability,
// then default. That is not cosmetic — SET NOT NULL against a column being retyped has to run after
// the retype — and it is what makes the golden file reproducible.
//
// Every statement this driver composes ends in a semicolon. A raw operation does not: run verbatim
// is the whole contract, and the author may already have written one. A view query is trimmed of
// its own terminator before being embedded, because pgx sends through the extended protocol, which
// rejects more than one command.
//
// # One pinned session, and no pool
//
// Open returns a single pgx connection. The task that specified this asked for connection
// pooling, and it does not do that, because nothing would draw from the pool: the engine migrates
// serially under one lock, and a capture stream is a replication connection opened from the
// Driver (ADR 0014) which would not share it either. pgxpool defaults to four connections, and
// holding four backends to use one is a cost operators notice — max_connections is a budget.
//
// The contract's rule survives regardless: an advisory lock belongs to a backend, so the session
// M3-T4 locks on must be the session that releases it. Pooling arrives when a second consumer
// does, and pinning is what it will have to preserve.
//
// # A connection string is never rendered
//
// pgx redacts passwords from its own error text on a best-effort basis, and its source says so:
// above the redaction, ParseConfigError.Error carries a comment that returning a static string
// "would ensure that the error message cannot leak a password". It is right. The keyword/value
// fallback matches password=[^ ]*, so a password containing a space keeps its tail —
//
//	host=localhost port=notanumber password=my secret
//	  -> cannot parse `host=localhost port=notanumber password=xxxxx secret`
//
// — and the URL branch that would have handled it only runs when url.Parse succeeds, which is
// exactly what fails when the string is unparsable. So parseDSN wraps the failure in a DSNError
// that renders none of it, and unwraps to the pgx cause for errors.Is and errors.As. The
// diagnosis survives; the text does not.
//
// # The health check is the version detection
//
// One query for server_version_num proves the session works and returns the fact M3-T3 and
// M13-T1 need, so there is no separate ping. The display string is derived from that number
// rather than read from server_version, which reports "17.2 (Debian 17.2-1.pgdg120+1)" — text
// nobody can compare. Deriving both from one detection is what keeps ServerVersion and
// capability.Facts.Server from disagreeing, which the driver contract requires.
//
// Servers older than PostgreSQL 10 are refused, because the major*10000 + minor encoding the
// version type reads does not hold below it. That refusal prints the raw number rather than a
// derived release, since the formula is the thing that does not apply.
//
// # search_path needs more than a bound parameter
//
// The schema from migr.yaml is set through set_config with a bound parameter — and that alone is
// not enough. A bound parameter stops SQL injection, but search_path is a list-valued setting, so
// PostgreSQL parses whatever value it receives as comma-separated entries: a schema of
// "public, pg_catalog" silently becomes two of them, which is a privilege surface rather than a
// typo. quote_ident makes it one identifier, so an injected value names a schema nobody has.
//
// This matters because internal/config validates a schema for nothing at all — not identifier
// legality, not length, not commas — and it reaches the session before any migration runs. An
// integration test asserts both halves: an ordinary schema is set plainly, and an injected one
// stays whole.
//
// # A string literal is written in, not bound
//
// DDL takes no parameters, so a column default's literal value is concatenated into the statement
// and doubling ' is the whole defense. That defense is complete only while
// standard_conforming_strings is on: with it off, a backslash starts an escape sequence and the
// doubling can be escaped past. It has been the default since PostgreSQL 9.1 and is deprecated,
// but Compile is pure and cannot read a session setting — so Open pins it rather than trusting it,
// alongside search_path.
package postgres
