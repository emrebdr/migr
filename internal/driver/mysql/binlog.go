package mysql

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/go-mysql-org/go-mysql/replication"

	"github.com/emrebdr/migr/internal/capture"
)

// A transaction is handed over only once its commit has been read, and the position moves only then.
//
// A row-format transaction arrives as a GTID event, a BEGIN, a table map before each table's rows, the
// rows, and an XID; a DDL statement as a GTID event and the statement, which is its own commit. Changes
// are held until the XID or the statement, so a batch never ends inside a transaction and a recorded
// position never names one -- a replay after a crash starts at a boundary and repeats whole
// transactions, which an idempotent apply makes harmless. An event outside any transaction (a rotate,
// a format description, the previous-GTIDs list) moves the file and offset and nothing else; a
// heartbeat moves nothing, since it is not in the log. The copy's own inserts into the shadow arrive
// too, and are read and dropped by their table map.

// heartbeats are the event types a server sends an idle reader, which carry no place in the log.
var heartbeats = []replication.EventType{replication.HEARTBEAT_EVENT, replication.HEARTBEAT_LOG_EVENT_V2}

// The row event types, in the three kinds a change can be.
var (
	writeEvents  = []replication.EventType{replication.WRITE_ROWS_EVENTv0, replication.WRITE_ROWS_EVENTv1, replication.WRITE_ROWS_EVENTv2}
	updateEvents = []replication.EventType{replication.UPDATE_ROWS_EVENTv0, replication.UPDATE_ROWS_EVENTv1, replication.UPDATE_ROWS_EVENTv2, replication.PARTIAL_UPDATE_ROWS_EVENT}
	deleteEvents = []replication.EventType{replication.DELETE_ROWS_EVENTv0, replication.DELETE_ROWS_EVENTv1, replication.DELETE_ROWS_EVENTv2}
)

// consume folds one event into the stream, returning the changes of a transaction it committed.
func (s *binlogStream) consume(event *replication.BinlogEvent) ([]capture.Change, error) {
	if slices.Contains(heartbeats, event.Header.EventType) {
		return nil, nil
	}

	if event.Header.LogPos > 0 {
		s.at.offset = event.Header.LogPos
	}

	return s.fold(event)
}

// fold folds one event that is in the log, a compressed transaction's inner events included.
func (s *binlogStream) fold(event *replication.BinlogEvent) ([]capture.Change, error) {
	if event.Header.Timestamp > 0 {
		s.seen = event.Header.Timestamp
	}

	switch e := event.Event.(type) {
	case *replication.RotateEvent:
		s.at = logPoint{file: string(e.NextLogName), offset: offsetOf(e.Position)}
	case *replication.GTIDEvent:
		return nil, s.beginGTID(e)
	case *replication.QueryEvent:
		return s.query(e)
	case *replication.XIDEvent:
		return s.commit(), nil
	case *replication.RowsEvent:
		return nil, s.rows(event.Header.EventType, e)
	case *replication.TransactionPayloadEvent:
		return s.payload(e)
	}

	if !s.open {
		s.boundary = s.at
	}

	return nil, nil
}

// offsetOf narrows a rotation's offset to a file offset, which a binary log file never exceeds.
func offsetOf(at uint64) uint32 {
	if at > math.MaxUint32 {
		return math.MaxUint32
	}

	return uint32(at)
}

// beginGTID notes the GTID of the transaction that follows, where the server records them.
func (s *binlogStream) beginGTID(e *replication.GTIDEvent) error {
	s.open = true

	if !s.gtid {
		return nil
	}

	next, err := e.GTIDNext()
	if err != nil {
		return fmt.Errorf("reading a GTID event in the binary log: %w", err)
	}

	s.next = next.String()

	return nil
}

// query handles a statement event: a transaction's BEGIN or COMMIT, or a statement that commits itself.
func (s *binlogStream) query(e *replication.QueryEvent) ([]capture.Change, error) {
	statement := strings.TrimSpace(string(e.Query))

	switch strings.ToUpper(statement) {
	case "BEGIN":
		s.open = true

		return nil, nil
	case "COMMIT":
		return s.commit(), nil
	}

	if touched := s.changesTable(string(e.Schema), statement); touched {
		return nil, fmt.Errorf("the table %s was changed under this shadow-table migration by %q, so "+
			"its copy can no longer be kept current -- abandon the migration with migr resolve "+
			"--rolled-back", s.table, statement)
	}

	return s.commit(), nil
}

// commit ends the transaction being read, handing over its changes and moving the position past it.
func (s *binlogStream) commit() []capture.Change {
	if s.next != "" && s.committed != nil {
		_ = s.committed.Update(s.next)
	}

	s.stamp = s.seen
	changes := s.pending
	s.pending, s.next, s.open, s.boundary = nil, "", false, s.at

	return changes
}

// payload folds the events a compressed transaction carries, in order.
func (s *binlogStream) payload(e *replication.TransactionPayloadEvent) ([]capture.Change, error) {
	var committed []capture.Change

	for _, inner := range e.Events {
		changes, err := s.fold(inner)
		if err != nil {
			return nil, err
		}

		committed = append(committed, changes...)
	}

	if !s.open {
		s.boundary = s.at
	}

	return committed, nil
}

// rows turns one row event on the stream's table into changes held until the transaction commits.
func (s *binlogStream) rows(kind replication.EventType, e *replication.RowsEvent) error {
	if e.Table == nil || !s.isTable(string(e.Table.Schema), string(e.Table.Table)) {
		return nil
	}

	switch {
	case slices.Contains(writeEvents, kind):
		return s.each(e.Rows, s.upsert(capture.Insert))
	case slices.Contains(deleteEvents, kind):
		return s.each(e.Rows, s.remove)
	case slices.Contains(updateEvents, kind):
		return s.updates(e.Rows)
	default:
		return nil
	}
}

// each applies one conversion to every row an event carries.
func (s *binlogStream) each(rows [][]any, convert func([]any) error) error {
	for _, row := range rows {
		if err := convert(row); err != nil {
			return err
		}
	}

	return nil
}

// updates turns an update event's before-and-after pairs into changes, splitting one that moved the key.
func (s *binlogStream) updates(rows [][]any) error {
	if len(rows)%2 != 0 {
		return fmt.Errorf("an update of %s in the binary log carries %d rows, and an update is pairs",
			s.table, len(rows))
	}

	for i := 0; i < len(rows); i += 2 {
		moved, err := s.keyMoved(rows[i], rows[i+1])
		if err != nil {
			return err
		}

		if moved {
			if err := s.remove(rows[i]); err != nil {
				return err
			}

			if err := s.upsert(capture.Insert)(rows[i+1]); err != nil {
				return err
			}

			continue
		}

		if err := s.upsert(capture.Update)(rows[i+1]); err != nil {
			return err
		}
	}

	return nil
}

// keyMoved reports an update that changed the row's primary key, which leaves the old key behind.
func (s *binlogStream) keyMoved(before, after []any) (bool, error) {
	old, err := s.keyOf(before)
	if err != nil {
		return false, err
	}

	updated, err := s.keyOf(after)
	if err != nil {
		return false, err
	}

	return !slices.EqualFunc(old, updated, func(a, b *string) bool {
		return a != nil && b != nil && *a == *b
	}), nil
}

// upsert returns the conversion of a row an insert or update left into a change carrying every written column.
func (s *binlogStream) upsert(kind capture.Kind) func([]any) error {
	return func(row []any) error {
		if err := s.checkWidth(row); err != nil {
			return err
		}

		change := capture.Change{Kind: kind, Table: s.table}

		for i, column := range s.columns {
			if column.generated {
				continue
			}

			value, err := render(column, row[i])
			if err != nil {
				return fmt.Errorf("replaying a change to %s: %w", s.table, err)
			}

			change.Columns = append(change.Columns, column.name)
			change.Row = append(change.Row, value)
		}

		s.pending = append(s.pending, change)

		return nil
	}
}

// remove converts a deleted row into a change carrying its key alone.
func (s *binlogStream) remove(row []any) error {
	key, err := s.keyOf(row)
	if err != nil {
		return err
	}

	change := capture.Change{Kind: capture.Delete, Table: s.table, Row: key}
	for _, i := range s.key {
		change.Columns = append(change.Columns, s.columns[i].name)
	}

	s.pending = append(s.pending, change)

	return nil
}

// keyOf renders a row's primary key.
func (s *binlogStream) keyOf(row []any) ([]*string, error) {
	if err := s.checkWidth(row); err != nil {
		return nil, err
	}

	key := make([]*string, 0, len(s.key))

	for _, i := range s.key {
		value, err := render(s.columns[i], row[i])
		if err != nil {
			return nil, fmt.Errorf("replaying a change to %s: %w", s.table, err)
		}

		key = append(key, value)
	}

	return key, nil
}

// checkWidth refuses a row whose column count is not the table's, which means the table changed underneath.
func (s *binlogStream) checkWidth(row []any) error {
	if len(row) != len(s.columns) {
		return fmt.Errorf("a change to %s in the binary log carries %d columns where the table had %d "+
			"when this stream opened, so the table was changed under this shadow-table migration -- "+
			"abandon it with migr resolve --rolled-back", s.table, len(row), len(s.columns))
	}

	return nil
}

// isTable reports whether a schema and table an event names are the stream's own.
func (s *binlogStream) isTable(schema, table string) bool {
	if s.folding != 0 {
		return strings.EqualFold(schema, s.database) && strings.EqualFold(table, s.table)
	}

	return schema == s.database && table == s.table
}

// changesTable reports a statement that alters, renames, drops or truncates the stream's table.
func (s *binlogStream) changesTable(schema, statement string) bool {
	for _, target := range targetsOf(statement) {
		qualifier, name := splitQualified(target)
		if qualifier == "" {
			qualifier = schema
		}

		if s.isTable(qualifier, name) {
			return true
		}
	}

	return false
}

// The statements that change an existing table, and the words that come before the tables they name.
var tableStatements = [][]string{
	{"ALTER", "TABLE"}, {"ALTER", "ONLINE", "TABLE"}, {"RENAME", "TABLE"}, {"DROP", "TABLE"},
	{"DROP", "TEMPORARY", "TABLE"}, {"TRUNCATE", "TABLE"}, {"TRUNCATE"},
}

// targetsOf lists the tables a table-changing statement changes, and none for any other statement.
func targetsOf(statement string) []string {
	words := tokens(statement)

	for _, head := range tableStatements {
		if len(words) <= len(head) || !leads(words, head) {
			continue
		}

		rest := words[len(head):]
		if len(rest) > 2 && strings.EqualFold(rest[0], "IF") && strings.EqualFold(rest[1], "EXISTS") {
			rest = rest[2:]
		}

		if head[0] == "ALTER" || head[0] == "TRUNCATE" {
			return rest[:1]
		}

		return listed(rest)
	}

	return nil
}

// leads reports whether a statement's words begin with a sequence, ignoring case.
func leads(words, head []string) bool {
	for i, word := range head {
		if !strings.EqualFold(words[i], word) {
			return false
		}
	}

	return true
}

// listed reads the first name of every comma-separated item a DROP or RENAME lists.
func listed(words []string) []string {
	names := []string{words[0]}

	for i, word := range words[:len(words)-1] {
		if word == "," {
			names = append(names, words[i+1])
		}
	}

	return names
}

// tokens splits a statement into words, keeping a backquoted name whole and a comma as a word of its own.
func tokens(statement string) []string {
	var (
		words   []string
		current strings.Builder
		quoted  bool
	)

	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}

	for i := range len(statement) {
		switch ch := statement[i]; {
		case ch == '`':
			quoted = !quoted
			current.WriteByte(ch)
		case quoted:
			current.WriteByte(ch)
		case ch == ',':
			flush()
			words = append(words, ",")
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == ';' || ch == '(':
			flush()
		default:
			current.WriteByte(ch)
		}
	}

	flush()

	return words
}

// splitQualified divides a possibly qualified, possibly backquoted name into its schema and its table.
func splitQualified(name string) (string, string) {
	parts := splitOutsideQuotes(name)
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(strings.Trim(part, "`"), "``", "`")
	}

	if len(parts) == 2 {
		return parts[0], parts[1]
	}

	return "", parts[len(parts)-1]
}

// splitOutsideQuotes splits a name at every dot that is not inside backquotes.
func splitOutsideQuotes(name string) []string {
	var (
		parts  []string
		start  int
		quoted bool
	)

	for i := range len(name) {
		switch name[i] {
		case '`':
			quoted = !quoted
		case '.':
			if !quoted {
				parts = append(parts, name[start:i])
				start = i + 1
			}
		}
	}

	return append(parts, name[start:])
}
