// Package capture is the contract a shadow-table migration reads a table's writes through: the
// engine's own replication log, never a trigger.
//
// # Why a package of its own
//
// The stream is on driver.Driver rather than on driver.Conn: a Conn is one pinned
// session whose pinning exists for a session-scoped lock, and a replication stream is a second
// connection of a different kind with a lifetime of its own. The types it speaks live here so that
// this package can never import internal/driver -- the driver imports it, so the compiler refuses
// the cycle, which is the same one-way rule internal/history follows.
//
// # A position is a value
//
// Position is opaque text in the driver's spelling -- a GTID set or a file and offset on MySQL, an
// LSN on PostgreSQL -- and the engine only stores it and hands it back. Resuming is reading from a
// recorded value rather than reconstructing where a copy got to, which is what makes every stage of
// the lifecycle resumable across a process restart.
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
// apply one as an upsert in any order against the copy.
//
// # An empty batch means the end of the log
//
// Stream.Next returns no change only once it has read to where the log ended as the call began, which
// is what lets a cutover's catch-up under its brief lock be complete rather than merely quick: the
// writes are stopped, so that end is the last write (M13-T10).
//
// MySQL's binlog reader implements this (M13-T10): a transaction is handed over once its
// commit is read, so a batch never ends inside one, and a position is a GTID set where the server
// records GTIDs and a file and offset where it does not. PostgreSQL's reads pgoutput through a
// replication slot (M13-T15), and SQLite's Capture always refuses with ErrUnsupported.
//
// Acknowledge is how a server lets its log go: the engine calls it once the transaction recording a
// position has committed, never inside one, because a PostgreSQL slot advanced inside a transaction
// that rolls back stays advanced -- measured -- and would forget the changes that transaction undid.
// A Change may be Partial, an update that left a TOASTed column out, which updates a row and inserts
// none; and a partial update that moved its key carries the old one in From.
package capture
