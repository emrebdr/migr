package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/capture"
)

// batchCap is how many changes one read hands over before it stops at the next commit.
const batchCap = 10000

// peekRows is how many messages one read asks the slot for: a change's own, and its transaction's begin and commit.
const peekRows = 4 * batchCap

// slotBusy is object_in_use, which dropping a slot a connection is still reading through answers.
const slotBusy = "55006"

// busyWait is how long a drop waits for a slot a dead reader's backend still holds, and how often it asks.
const (
	busyWait = 30 * time.Second
	busyPoll = 250 * time.Millisecond
)

// peekQuery reads, without consuming, up to a number of the slot's pgoutput messages before a position, ending at a commit.
const peekQuery = `SELECT lsn::text, data FROM pg_logical_slot_peek_binary_changes($1, $2::pg_lsn, $3::int,
  'proto_version', '1', 'publication_names', $4)`

// slotQuery reads where a slot stands, and whether some connection is reading through it now.
const slotQuery = `SELECT confirmed_flush_lsn::text, active FROM pg_replication_slots
 WHERE slot_name = $1 AND database = current_database()`

// tableColumnsQuery lists a table's columns as pgoutput sends them: no dropped and no generated column.
const tableColumnsQuery = `SELECT a.attname FROM pg_attribute a
 WHERE a.attrelid = to_regclass(quote_ident($1)) AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = ''
 ORDER BY a.attnum`

// Capture opens a stream of one table's committed row changes, decoded from the WAL through a slot of its own.
func (d *pgDriver) Capture(ctx context.Context, dsn string, req capture.Request) (capture.Stream, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	if len(req.Tables) != 1 {
		return nil, fmt.Errorf("the postgres driver reads one table's changes per stream, and %q names %d",
			req.Name, len(req.Tables))
	}

	conn, err := d.streamConn(ctx, dsn)
	if err != nil {
		return nil, err
	}

	s := &slotStream{conn: conn, name: req.Name, table: req.Tables[0]}

	if err := s.open(ctx, req.From); err != nil {
		return nil, errors.Join(err, conn.Close(context.WithoutCancel(ctx)))
	}

	return s, nil
}

// streamConn opens the connection a stream reads through, never the migration's own session.
func (d *pgDriver) streamConn(ctx context.Context, dsn string) (*pgx.Conn, error) {
	cfg, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}

	pinApplicationName(cfg)
	pinKeepalives(cfg)
	pinCancellation(cfg)

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: opening a change stream: %w", ErrConnect, err)
	}

	if err := applySearchPath(ctx, conn, d.schema); err != nil {
		return nil, errors.Join(err, conn.Close(context.WithoutCancel(ctx)))
	}

	return conn, nil
}

// slotStream reads one table's changes through a logical replication slot and a publication of the same name.
type slotStream struct {
	// conn is the stream's own connection.
	conn *pgx.Conn
	// name is the slot and the publication.
	name string
	// table is the table the publication carries.
	table string
	// start is where the stream began.
	start pglogrepl.LSN
	// acknowledged is how far the slot has been advanced, which an advance never moves back from.
	acknowledged pglogrepl.LSN
	// consumed is how many messages past the slot's position the cursor already stands beyond, which a read asks for on top.
	consumed int
	// decoder carries the relations read and the cursor every read skips to.
	decoder *decoder
	// gone reports a resumed stream whose slot no longer exists, which can still be dropped and read nothing.
	gone bool
}

// open creates the slot for a fresh stream, dropping one a start that recorded nothing left, or finds the recorded one.
func (s *slotStream) open(ctx context.Context, from capture.Position) error {
	columns, err := s.columns(ctx)
	if err != nil {
		return err
	}

	if from == "" {
		return s.create(ctx, columns)
	}

	at, err := pglogrepl.ParseLSN(string(from))
	if err != nil {
		return fmt.Errorf("the recorded log position %q of %s is not a WAL position: %w", from, s.name, err)
	}

	confirmed, found, err := s.slot(ctx)
	if err != nil {
		return err
	}

	s.start, s.acknowledged, s.gone = at, confirmed, !found
	s.decoder = newDecoder(s.table, columns, at)

	return nil
}

// columns reads the table's columns in the order a pgoutput row carries them.
func (s *slotStream) columns(ctx context.Context) ([]string, error) {
	rows, err := s.conn.Query(ctx, tableColumnsQuery, s.table)
	if err != nil {
		return nil, fmt.Errorf("reading the columns of %s for a change stream: %w", s.table, err)
	}

	columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("reading the columns of %s for a change stream: %w", s.table, err)
	}

	if len(columns) == 0 {
		return nil, fmt.Errorf("%s has no columns this server can read, or is not a table here", s.table)
	}

	return columns, nil
}

// create drops what a start that recorded nothing left, then makes the publication and the slot.
func (s *slotStream) create(ctx context.Context, columns []string) error {
	if err := release(ctx, s.conn, s.name); err != nil {
		return err
	}

	if _, err := s.conn.Exec(ctx, "CREATE PUBLICATION "+quote(s.name)+" FOR TABLE "+quote(s.table)); err != nil {
		return fmt.Errorf("publishing %s's changes as %s: %w", s.table, s.name, err)
	}

	var made string
	if err := s.conn.QueryRow(ctx, "SELECT lsn::text FROM pg_create_logical_replication_slot($1, 'pgoutput')",
		s.name).Scan(&made); err != nil {
		return errors.Join(fmt.Errorf("creating the replication slot %s: %w", s.name, err),
			release(context.WithoutCancel(ctx), s.conn, s.name))
	}

	at, err := pglogrepl.ParseLSN(made)
	if err != nil {
		return fmt.Errorf("the replication slot %s began at %q, which is not a WAL position: %w", s.name, made, err)
	}

	s.start, s.acknowledged = at, at
	s.decoder = newDecoder(s.table, columns, at)

	return nil
}

// slot reads where the stream's slot stands, and whether it exists at all.
func (s *slotStream) slot(ctx context.Context) (pglogrepl.LSN, bool, error) {
	var (
		confirmed *string
		active    bool
	)

	err := s.conn.QueryRow(ctx, slotQuery, s.name).Scan(&confirmed, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, fmt.Errorf("reading the replication slot %s: %w", s.name, err)
	}

	if confirmed == nil {
		return 0, true, nil
	}

	at, err := pglogrepl.ParseLSN(*confirmed)
	if err != nil {
		return 0, false, fmt.Errorf("the replication slot %s stands at %q, which is not a WAL position: %w",
			s.name, *confirmed, err)
	}

	return at, true, nil
}

// Start returns the position the stream began reading at.
func (s *slotStream) Start() capture.Position {
	return capture.Position(s.start.String())
}

// Next returns the changes committed since the last call, up to where the WAL ended as it began.
func (s *slotStream) Next(ctx context.Context) (capture.Batch, error) {
	if s.gone {
		return capture.Batch{}, fmt.Errorf("the replication slot %s no longer exists, so the WAL since this "+
			"shadow-table migration's recorded position is gone with it -- abandon it with migr resolve "+
			"--rolled-back and run it again", s.name)
	}

	var (
		upto pglogrepl.LSN
		now  time.Time
		end  string
	)

	if err := s.conn.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text, clock_timestamp()").Scan(&end, &now); err != nil {
		return capture.Batch{}, fmt.Errorf("reading where the WAL ends for %s: %w", s.name, err)
	}

	upto, err := pglogrepl.ParseLSN(end)
	if err != nil {
		return capture.Batch{}, fmt.Errorf("the WAL ends at %q, which is not a WAL position: %w", end, err)
	}

	got, err := s.read(ctx, upto)
	if err != nil {
		return capture.Batch{}, err
	}

	return s.batch(got, upto, now), nil
}

// batch turns what a read decoded into the batch handed over: idle past every other table's WAL, and behind while full.
func (s *slotStream) batch(got read, upto pglogrepl.LSN, now time.Time) capture.Batch {
	batch := capture.Batch{Changes: got.changes, Through: capture.Position(got.through.String())}

	if got.full {
		if !got.committed.IsZero() {
			batch.Behind = max(now.Sub(got.committed), 0)
		}

		return batch
	}

	if upto > s.decoder.after {
		s.decoder.after = upto
	}

	batch.Through = capture.Position(s.decoder.after.String())

	return batch
}

// read decodes the next batch past the cursor, asking for more of the slot until a capped peek reaches past it.
func (s *slotStream) read(ctx context.Context, upto pglogrepl.LSN) (read, error) {
	limit := s.consumed + peekRows

	for {
		messages, err := s.peek(ctx, upto, limit)
		if err != nil {
			return read{}, err
		}

		before := s.decoder.after

		got, err := s.decoder.decode(messages, batchCap)
		if err != nil {
			return read{}, err
		}

		capped := len(messages) >= limit
		if !capped || s.decoder.after > before {
			s.consumed = got.consumed
			got.full = got.full || capped

			return got, nil
		}

		limit *= 2
	}
}

// peek reads up to a number of the messages the slot holds before a position, leaving the slot where it is.
func (s *slotStream) peek(ctx context.Context, upto pglogrepl.LSN, limit int) ([]decoded, error) {
	rows, err := s.conn.Query(ctx, peekQuery, s.name, upto.String(), limit, s.name)
	if err != nil {
		return nil, fmt.Errorf("reading the replication slot %s: %w", s.name, err)
	}

	defer rows.Close()

	var messages []decoded

	for rows.Next() {
		var (
			at   string
			data []byte
		)

		if err := rows.Scan(&at, &data); err != nil {
			return nil, fmt.Errorf("reading the replication slot %s: %w", s.name, err)
		}

		lsn, err := pglogrepl.ParseLSN(at)
		if err != nil {
			return nil, fmt.Errorf("the replication slot %s reported %q, which is not a WAL position: %w",
				s.name, at, err)
		}

		messages = append(messages, decoded{lsn: lsn, data: data})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the replication slot %s: %w", s.name, err)
	}

	return messages, nil
}

// Acknowledge advances the slot past every change before a position the engine has recorded, so the server can let its WAL go.
func (s *slotStream) Acknowledge(ctx context.Context, through capture.Position) error {
	if s.gone {
		return nil
	}

	at, err := pglogrepl.ParseLSN(string(through))
	if err != nil {
		return fmt.Errorf("the position %q of %s is not a WAL position: %w", through, s.name, err)
	}

	if at <= s.acknowledged {
		return nil
	}

	if _, err := s.conn.Exec(ctx, "SELECT pg_replication_slot_advance($1, $2::pg_lsn)", s.name, at.String()); err != nil {
		return fmt.Errorf("advancing the replication slot %s to %s: %w", s.name, at, err)
	}

	s.acknowledged = at
	if at >= s.decoder.after {
		s.consumed = 0
	}

	return nil
}

// Close stops reading and keeps the slot and the publication, so a later stream resumes from a recorded position.
func (s *slotStream) Close(ctx context.Context) error {
	return s.conn.Close(ctx)
}

// Drop stops reading and drops the slot and the publication, which is nothing where they are already gone.
func (s *slotStream) Drop(ctx context.Context) error {
	return errors.Join(release(ctx, s.conn, s.name), s.conn.Close(ctx))
}

// release drops a slot and the publication of the same name, waiting a bounded while for a dead reader to let go.
func release(ctx context.Context, conn *pgx.Conn, name string) error {
	if err := dropSlot(ctx, conn, name); err != nil {
		return err
	}

	if _, err := conn.Exec(ctx, "DROP PUBLICATION IF EXISTS "+quote(name)); err != nil {
		return fmt.Errorf("dropping the publication %s: %w", name, err)
	}

	return nil
}

// dropSlot drops a replication slot where it exists, retrying while a reader that is gone still holds it.
func dropSlot(ctx context.Context, conn *pgx.Conn, name string) error {
	for tries := int(busyWait / busyPoll); ; tries-- {
		_, err := conn.Exec(ctx, "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots "+
			"WHERE slot_name = $1 AND database = current_database()", name)

		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || pgErr.Code != slotBusy || tries <= 0 {
			return wrapDrop(name, err)
		}

		select {
		case <-ctx.Done():
			return wrapDrop(name, errors.Join(err, ctx.Err()))
		case <-time.After(busyPoll):
		}
	}
}

// wrapDrop names the slot a failed drop was of, and is nothing where the drop succeeded.
func wrapDrop(name string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("dropping the replication slot %s, which keeps every byte of WAL since it was last "+
		"advanced until it is dropped: %w", name, err)
}

// compile-time proof that a slot reader is a change stream.
var _ capture.Stream = (*slotStream)(nil)
