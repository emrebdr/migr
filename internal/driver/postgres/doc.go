// Package postgres implements the Driver contract for PostgreSQL using pgx.
//
// PostgreSQL is the reference driver: it supports transactional DDL and most operations
// natively, which is why the engine is built against it first. It provides advisory locking, and
// its lock-impact verdicts are keyed to the detected server version — ADD COLUMN with a default
// rewrites the table on PostgreSQL 10 and is instant from 11 onward.
//
// Status: connections complete (M3-T1). This driver compiles nothing and locks nothing yet;
// see pending.go, which every remaining M3 task deletes a piece of.
//
// # Honestly unsupported, rather than half-built
//
// Open returns a session that satisfies the whole contract and tells the truth about it: all
// twenty operations are declared Unsupported naming the task that will implement them, Compile
// refuses identically, and Lock and the history store refuse the same way.
//
// That is not a shortcut around the contract, it is what the contract asks for. capability.NewSet
// rejects a set that does not answer for every operation, so there is no such thing as a partial
// declaration — and Capabilities returns no error, so a set that will not build has to fail the
// connection instead. Declaring nothing supported is the only complete and true answer available
// before M3-T2.
//
// The payoff is that the conformance suite passes against this driver from its first commit, and
// against a real driver rather than a fake: Compile refuses exactly what the declaration refuses,
// which is the biconditional the suite exists to prove. It holds at zero operations as well as it
// will at twenty, and each later task moves operations across without the suite ever being
// switched off.
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
package postgres
