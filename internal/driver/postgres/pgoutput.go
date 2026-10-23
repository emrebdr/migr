package postgres

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pglogrepl"

	"github.com/emrebdr/migr/internal/capture"
)

// A change stream reads pgoutput, PostgreSQL's own logical decoding plugin, through a publication of
// the one table and a logical replication slot (ADR 0094). Each read is a peek bounded by where the
// WAL ended as it began, so it returns nothing only once it has read that far; a peek decodes from
// the slot's confirmed position, so a transaction committed at or before the stream's cursor is one
// already handed over and is skipped. A transaction's changes are handed over only at its commit, so
// a recorded position is always a commit boundary.
//
// An update that leaves a TOASTed column unchanged carries no value for it -- measured on 17, the
// column arrives as 'u' with no bytes -- so such a change omits the column and is marked Partial: it
// updates the row where the shadow has it and inserts none, since a row the copy has not reached yet
// gets the value from the copy. An update that moves the key carries the old key, and arrives as a
// delete of it before the new row -- except where it is partial too, since the column it left out is
// on the old row alone: that change carries the old key in From, and moves the shadow's row instead.

// errTruncated marks a stream that read a TRUNCATE of the table it carries, which no row change replays.
var errTruncated = errors.New("the table was truncated while a shadow table was kept current from it")

// errReshaped marks a stream whose table changed columns under it, which the shadow does not follow.
var errReshaped = errors.New("the table's columns changed while a shadow table was kept current from it")

// decoded is one message of a peek, with the WAL position the server reported it at.
type decoded struct {
	// lsn is where the server placed the message, which for a commit is the position a reader advances to.
	lsn pglogrepl.LSN
	// data is the pgoutput message.
	data []byte
}

// decoder turns pgoutput messages into committed row changes of one table, past one position.
type decoder struct {
	// table is the table the stream carries, as the catalog holds its name.
	table string
	// columns are the table's columns as the stream opened, which a relation message must keep.
	columns []string
	// after is the position every transaction committed at or before was already handed over.
	after pglogrepl.LSN
	// relations are the relation messages read, by the id each change names.
	relations map[uint32]*pglogrepl.RelationMessage
	// pending are the changes of the transaction being read.
	pending []capture.Change
	// begun is the commit time the transaction being read announced.
	begun time.Time
}

// read is what one peek decoded: the changes committed past the cursor, where they end, and when.
type read struct {
	// changes are what committed, in commit order.
	changes []capture.Change
	// through is the last commit read whole, and the cursor where none was.
	through pglogrepl.LSN
	// committed is when the last transaction read whole committed.
	committed time.Time
	// full reports a read that stopped at a commit boundary with more of the log still to read.
	full bool
	// consumed is how many of the messages read lead up to the cursor, every one of them now behind it.
	consumed int
}

// newDecoder returns a decoder for one table's changes, past a position.
func newDecoder(table string, columns []string, after pglogrepl.LSN) *decoder {
	return &decoder{table: table, columns: columns, after: after,
		relations: map[uint32]*pglogrepl.RelationMessage{}}
}

// decode reads a peek's messages in order, stopping at the first commit that brings the changes to limit.
func (d *decoder) decode(messages []decoded, limit int) (read, error) {
	out := read{through: d.after}

	for i, m := range messages {
		committed, err := d.message(m)
		if err != nil {
			return read{}, err
		}

		if !committed {
			continue
		}

		out.consumed = i + 1

		if m.lsn <= d.after {
			continue
		}

		out.changes = append(out.changes, d.pending...)
		out.through, out.committed = m.lsn, d.begun
		d.pending, d.after = nil, m.lsn

		if len(out.changes) >= limit {
			out.full = true

			return out, nil
		}
	}

	return out, nil
}

// message reads one message, reporting whether it committed a transaction.
func (d *decoder) message(m decoded) (bool, error) {
	parsed, err := pglogrepl.Parse(m.data)
	if err != nil {
		return false, fmt.Errorf("reading a pgoutput message at %s: %w", m.lsn, err)
	}

	switch msg := parsed.(type) {
	case *pglogrepl.BeginMessage:
		d.pending, d.begun = nil, msg.CommitTime
	case *pglogrepl.CommitMessage:
		return true, nil
	case *pglogrepl.RelationMessage:
		return false, d.relation(msg)
	case *pglogrepl.InsertMessage:
		return false, d.insert(msg)
	case *pglogrepl.UpdateMessage:
		return false, d.update(msg)
	case *pglogrepl.DeleteMessage:
		return false, d.delete(msg)
	case *pglogrepl.TruncateMessage:
		return false, d.truncate(msg)
	}

	return false, nil
}

// relation remembers a relation message, refusing one that says the table's columns are not the ones it began with.
func (d *decoder) relation(msg *pglogrepl.RelationMessage) error {
	d.relations[msg.RelationID] = msg

	if msg.RelationName != d.table {
		return nil
	}

	names := make([]string, len(msg.Columns))
	for i, column := range msg.Columns {
		names[i] = column.Name
	}

	if !slices.Equal(names, d.columns) {
		return fmt.Errorf("%w: %s now has the columns %v where it had %v -- the copy cannot follow an "+
			"ALTER of the table it is copying, so abandon the migration with migr resolve --rolled-back "+
			"and run it again", errReshaped, d.table, names, d.columns)
	}

	return nil
}

// ours returns the relation a change names, where it is the table the stream carries.
func (d *decoder) ours(id uint32) (*pglogrepl.RelationMessage, bool) {
	rel, found := d.relations[id]

	return rel, found && rel.RelationName == d.table
}

// insert reads an inserted row whole.
func (d *decoder) insert(msg *pglogrepl.InsertMessage) error {
	rel, ours := d.ours(msg.RelationID)
	if !ours {
		return nil
	}

	change, err := rowOf(rel, msg.Tuple, capture.Insert)
	if err != nil {
		return err
	}

	d.pending = append(d.pending, change)

	return nil
}

// update reads an updated row, deleting its old key first where the update moved it.
func (d *decoder) update(msg *pglogrepl.UpdateMessage) error {
	rel, ours := d.ours(msg.RelationID)
	if !ours {
		return nil
	}

	change, err := rowOf(rel, msg.NewTuple, capture.Update)
	if err != nil {
		return err
	}

	if msg.OldTupleType != pglogrepl.UpdateMessageTupleTypeKey && msg.OldTupleType != pglogrepl.UpdateMessageTupleTypeOld {
		d.pending = append(d.pending, change)

		return nil
	}

	removed, err := keyOf(rel, msg.OldTuple)
	if err != nil {
		return err
	}

	if change.Partial {
		change.From = &removed
		d.pending = append(d.pending, change)

		return nil
	}

	d.pending = append(d.pending, removed, change)

	return nil
}

// delete reads a removed row's key.
func (d *decoder) delete(msg *pglogrepl.DeleteMessage) error {
	rel, ours := d.ours(msg.RelationID)
	if !ours {
		return nil
	}

	removed, err := keyOf(rel, msg.OldTuple)
	if err != nil {
		return err
	}

	d.pending = append(d.pending, removed)

	return nil
}

// truncate refuses a TRUNCATE of the table, which the shadow would otherwise silently keep the rows of.
func (d *decoder) truncate(msg *pglogrepl.TruncateMessage) error {
	for _, id := range msg.RelationIDs {
		if _, ours := d.ours(id); ours {
			return fmt.Errorf("%w: %s -- the copy would keep every row the original lost, so abandon "+
				"the migration with migr resolve --rolled-back and run it again", errTruncated, d.table)
		}
	}

	return nil
}

// rowOf reads a tuple as a change carrying every column it holds a value for, marked partial where it leaves one out.
func rowOf(rel *pglogrepl.RelationMessage, tuple *pglogrepl.TupleData, kind capture.Kind) (capture.Change, error) {
	if tuple == nil || len(tuple.Columns) != len(rel.Columns) {
		return capture.Change{}, fmt.Errorf("a pgoutput %s of %s carries a row that does not match "+
			"its %d columns", kind, rel.RelationName, len(rel.Columns))
	}

	change := capture.Change{Kind: kind, Table: rel.RelationName}

	for i, column := range tuple.Columns {
		if column.DataType == pglogrepl.TupleDataTypeToast {
			change.Partial = true

			continue
		}

		value, err := valueOf(rel, i, column)
		if err != nil {
			return capture.Change{}, err
		}

		change.Columns, change.Row = append(change.Columns, rel.Columns[i].Name), append(change.Row, value)
	}

	return change, nil
}

// keyOf reads the key a delete or a key-moving update removes, from the columns the relation marks as key.
func keyOf(rel *pglogrepl.RelationMessage, tuple *pglogrepl.TupleData) (capture.Change, error) {
	if tuple == nil || len(tuple.Columns) != len(rel.Columns) {
		return capture.Change{}, fmt.Errorf("a pgoutput delete of %s carries no key", rel.RelationName)
	}

	change := capture.Change{Kind: capture.Delete, Table: rel.RelationName}

	for i, column := range tuple.Columns {
		if rel.Columns[i].Flags&1 == 0 {
			continue
		}

		value, err := valueOf(rel, i, column)
		if err != nil {
			return capture.Change{}, err
		}

		if value == nil {
			return capture.Change{}, fmt.Errorf("a pgoutput delete of %s carries no value for its key "+
				"column %s", rel.RelationName, rel.Columns[i].Name)
		}

		change.Columns, change.Row = append(change.Columns, rel.Columns[i].Name), append(change.Row, value)
	}

	if len(change.Columns) == 0 {
		return capture.Change{}, fmt.Errorf("a pgoutput delete of %s names no key column", rel.RelationName)
	}

	return change, nil
}

// valueOf reads one column's text, nil for NULL.
func valueOf(rel *pglogrepl.RelationMessage, i int, column *pglogrepl.TupleDataColumn) (*string, error) {
	switch column.DataType {
	case pglogrepl.TupleDataTypeNull:
		return nil, nil
	case pglogrepl.TupleDataTypeText:
		text := string(column.Data)

		return &text, nil
	default:
		return nil, fmt.Errorf("a pgoutput change of %s carries column %s as %q, where this reader "+
			"asks for text", rel.RelationName, rel.Columns[i].Name, column.DataType)
	}
}
