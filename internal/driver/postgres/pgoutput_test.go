package postgres

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pglogrepl"

	"github.com/emrebdr/migr/internal/capture"
)

// The messages here are pgoutput's protocol version 1 as the PostgreSQL documentation lays it out,
// encoded by hand, and the TOAST marker is the 'u' measured on 17 for an update leaving a large column
// alone.

// relationID is the one relation every message here names.
const relationID = 16401

// startOfAfternoon is the instant an idle read is taken at, since nothing here may read a clock.
var startOfAfternoon = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// wire builds one pgoutput message.
type wire []byte

// byte1 appends one byte.
func (w wire) byte1(b byte) wire { return append(w, b) }

// int16 appends a big-endian 16-bit integer.
func (w wire) int16(n uint16) wire { return binary.BigEndian.AppendUint16(w, n) }

// int32 appends a big-endian 32-bit integer.
func (w wire) int32(n uint32) wire { return binary.BigEndian.AppendUint32(w, n) }

// int64 appends a big-endian 64-bit integer.
func (w wire) int64(n uint64) wire { return binary.BigEndian.AppendUint64(w, n) }

// text appends a NUL-terminated string.
func (w wire) text(s string) wire { return append(append(w, s...), 0) }

// relationMessage describes people(id key, label, note).
func relationMessage(columns ...string) []byte {
	w := wire{}.byte1('R').int32(relationID).text("public").text("people").byte1('d').int16(uint16(len(columns)))
	for i, name := range columns {
		flags := byte(0)
		if i == 0 {
			flags = 1
		}

		w = w.byte1(flags).text(name).int32(25).int32(0xffffffff)
	}

	return w
}

// tuple encodes values, nil for NULL and "\x00u" for an unchanged TOAST value.
func tuple(w wire, values ...*string) wire {
	w = w.int16(uint16(len(values)))

	for _, v := range values {
		switch {
		case v == nil:
			w = w.byte1('n')
		case *v == "\x00u":
			w = w.byte1('u')
		default:
			w = w.byte1('t').int32(uint32(len(*v)))
			w = append(w, *v...)
		}
	}

	return w
}

// s returns a pointer to a value.
func s(v string) *string { return &v }

// toast is the marker for a column an update left unchanged.
var toast = s("\x00u")

// begin, commit, insert, update, del and truncate build the messages a transaction is made of.
func begin() []byte { return wire{}.byte1('B').int64(0).int64(0).int32(7) }

func commit(lsn uint64) []byte { return wire{}.byte1('C').byte1(0).int64(lsn).int64(lsn).int64(0) }

func insert(values ...*string) []byte {
	return tuple(wire{}.byte1('I').int32(relationID).byte1('N'), values...)
}

func update(values ...*string) []byte {
	return tuple(wire{}.byte1('U').int32(relationID).byte1('N'), values...)
}

func moved(old, values []*string) []byte {
	return tuple(tuple(wire{}.byte1('U').int32(relationID).byte1('K'), old...).byte1('N'), values...)
}

func del(key *string) []byte {
	return tuple(wire{}.byte1('D').int32(relationID).byte1('K'), key, nil, nil)
}

func truncate() []byte { return wire{}.byte1('T').int32(1).byte1(0).int32(relationID) }

// transaction places messages at WAL positions, each message one past the last, the commit at the position given.
func transaction(lsn uint64, messages ...[]byte) []decoded {
	out := make([]decoded, 0, len(messages)+2)
	out = append(out, decoded{lsn: pglogrepl.LSN(lsn - 1), data: begin()})

	for _, m := range messages {
		out = append(out, decoded{lsn: pglogrepl.LSN(lsn - 1), data: m})
	}

	return append(out, decoded{lsn: pglogrepl.LSN(lsn), data: commit(lsn)})
}

// peopleColumns are the columns the stream opened with.
var peopleColumns = []string{"id", "label", "note"}

func TestChangesAreHandedOverAtTheirCommitAndAlreadyAppliedOnesAreSkipped(t *testing.T) {
	d := newDecoder("people", peopleColumns, 100)
	messages := append(transaction(100, relationMessage(peopleColumns...), insert(s("1"), s("old"), nil)),
		transaction(200, relationMessage(peopleColumns...), insert(s("2"), s("a"), nil), del(s("1")))...)

	got, err := d.decode(messages, batchCap)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(got.changes) != 2 || got.changes[0].Kind != capture.Insert || got.changes[1].Kind != capture.Delete {
		t.Fatalf("decoded %+v, want the second transaction's insert and delete alone", got.changes)
	}

	if got.through != 200 || got.full {
		t.Errorf("the read ends at %s (full %v), want at the last commit", got.through, got.full)
	}

	again, err := d.decode(messages, batchCap)
	if err != nil || len(again.changes) != 0 {
		t.Errorf("a second read of the same messages handed over %v (%v), want nothing", again.changes, err)
		t.Log("Every peek decodes from the slot's confirmed position, which trails the cursor until the")
		t.Log("engine acknowledges, so anything at or before the cursor has already been handed over.")
	}
}

func TestAReadStopsAtTheCommitThatFillsTheBatch(t *testing.T) {
	d := newDecoder("people", peopleColumns, 0)
	messages := append(transaction(100, relationMessage(peopleColumns...), insert(s("1"), s("a"), nil),
		insert(s("2"), s("b"), nil)), transaction(200, insert(s("3"), s("c"), nil))...)

	got, err := d.decode(messages, 2)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !got.full || got.through != 100 || len(got.changes) != 2 {
		t.Errorf("decoded %d changes through %s (full %v), want the first transaction and a full batch",
			len(got.changes), got.through, got.full)
	}
}

func TestAReadCountsTheMessagesBehindTheCursorSoACappedPeekAsksPastThem(t *testing.T) {
	d := newDecoder("people", peopleColumns, 100)
	first := transaction(100, relationMessage(peopleColumns...), insert(s("1"), s("old"), nil))
	second := transaction(200, insert(s("2"), s("a"), nil), insert(s("3"), s("b"), nil))
	messages := append(append(first, second...), transaction(300, insert(s("4"), s("c"), nil))...)

	got, err := d.decode(messages, 2)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.consumed != len(first)+len(second) || got.through != 200 {
		t.Errorf("the read consumed %d messages through %s, want the %d up to the commit it stopped at",
			got.consumed, got.through, len(first)+len(second))
		t.Log("A capped peek starts at the slot's position, which the cutover's catch-up never advances: a")
		t.Log("read that asked for no more than the messages it had already handed over saw nothing new and")
		t.Log("reported the stream caught up with a backlog unread, losing every row in it.")
	}
}

func TestAnUpdateLeavingAToastedColumnAloneIsPartialAndAKeyMoveCarriesItsOldKey(t *testing.T) {
	d := newDecoder("people", peopleColumns, 0)
	messages := transaction(100, relationMessage(peopleColumns...),
		update(s("1"), s("b"), toast),
		moved([]*string{s("2"), nil, nil}, []*string{s("9"), s("c"), toast}),
		moved([]*string{s("3"), nil, nil}, []*string{s("8"), s("d"), s("small")}))

	got, err := d.decode(messages, batchCap)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(got.changes) != 4 {
		t.Fatalf("decoded %+v, want four changes", got.changes)
	}

	partial := got.changes[0]
	if !partial.Partial || len(partial.Columns) != 2 || partial.From != nil {
		t.Errorf("an update leaving note alone decoded as %+v, want partial without note", partial)
	}

	move := got.changes[1]
	if !move.Partial || move.From == nil || *move.From.Row[0] != "2" || *move.Row[0] != "9" {
		t.Errorf("a partial key move decoded as %+v, want one change carrying the old key", move)
		t.Log("Measured: a delete and a partial update lost every row whose key moved while its TOASTed")
		t.Log("column was left alone, the value being on the old row alone.")
	}

	if got.changes[2].Kind != capture.Delete || *got.changes[2].Row[0] != "3" || got.changes[3].Partial {
		t.Errorf("a whole key move decoded as %+v, want a delete of the old key and the new row", got.changes[2:])
	}
}

func TestATruncateOrAReshapedTableStopsTheStream(t *testing.T) {
	for name, tc := range map[string]struct {
		messages []decoded
		want     error
	}{
		"truncate": {transaction(100, relationMessage(peopleColumns...), truncate()), errTruncated},
		"reshaped": {transaction(100, relationMessage("id", "label", "note", "extra")), errReshaped},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newDecoder("people", peopleColumns, 0).decode(tc.messages, batchCap)
			if !errors.Is(err, tc.want) {
				t.Errorf("decode returned %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAnIdleReadLetsTheSlotAdvancePastEveryOtherTablesWAL(t *testing.T) {
	stream := &slotStream{decoder: newDecoder("people", peopleColumns, 100)}

	batch := stream.batch(read{through: 100}, 5000, startOfAfternoon)
	if batch.Through != capture.Position(pglogrepl.LSN(5000).String()) || len(batch.Changes) != 0 {
		t.Errorf("an idle read hands over %+v, want nothing through where the WAL ended", batch)
		t.Log("Without this a slot on an idle table under busy others never advances, and keeps every")
		t.Log("other table's WAL too.")
	}
}
