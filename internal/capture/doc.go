// Package capture is the contract a shadow-table migration reads a table's writes through: the
// engine's own replication log, never a trigger (docs/adr/0014-online-change-capture.md).
//
// # Why a package of its own
//
// ADR 0014 B2 put the stream on driver.Driver rather than on driver.Conn: a Conn is one pinned
// session whose pinning exists for a session-scoped lock, and a replication stream is a second
// connection of a different kind with a lifetime of its own. The types it speaks live here so that
// this package can never import internal/driver -- the driver imports it, so the compiler refuses
// the cycle, which is the same one-way rule ADR 0011 recorded for internal/history.
//
// # A position is a value
//
// Position is opaque text in the driver's spelling -- a GTID set or a file and offset on MySQL, an
// LSN on PostgreSQL -- and the engine only stores it and hands it back. Resuming is reading from a
// recorded value rather than reconstructing where a copy got to, which is what makes every stage of
// the lifecycle resumable across a process restart (ADR 0089).
//
// # Close keeps, Drop releases
//
// The two ways to stop are the two outcomes a migration can have. Close detaches and keeps the
// server-side resource, so the next run reads on from the recorded position; Drop releases it, and
// is what an abort and a finished cutover call. A resource left behind is an outage rather than
// litter -- an abandoned PostgreSQL slot retains WAL until the disk fills -- so Request.Name is
// derived from the migration rather than chosen by the server, and an abort can find it even when a
// crash came before anything recorded it. Drop of a resource that is already gone is nothing, so an
// abort can always be run again.
//
// # Values are the server's own text
//
// A Change carries every column as the text the server reported, nil for NULL, as driver.Rows does
// for a migration's own read: a value that crosses from the log to the shadow is never reinterpreted
// on the way. Insert and update carry the whole row after the write, which is what lets the shadow
// apply one as an upsert in any order against the copy (ADR 0089, B).
//
// Nothing here is implemented by a real driver yet: MySQL's binlog reader is M13-T10 and
// PostgreSQL's logical decoding is M13-T15. Every driver's Capture refuses with ErrUnsupported and a
// reason until then, and SQLite's always will.
package capture
