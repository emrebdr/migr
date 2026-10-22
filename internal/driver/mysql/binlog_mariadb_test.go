package mysql

import (
	"strings"
	"testing"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
)

// The events here are what MariaDB 11.4.13 was measured writing: a GTID event opening every group with no
// BEGIN after it, an annotate event before the rows, the rows, an XID, and a standalone group for a DDL.

// mariadbStreamOver is streamOver on a MariaDB recording GTIDs, from a position.
func mariadbStreamOver(t *testing.T, from string) *binlogStream {
	t.Helper()

	start, err := gomysql.ParseMariadbGTIDSet(from)
	if err != nil {
		t.Fatal(err)
	}

	s := streamOver()
	s.mariadb, s.gtid, s.committed = true, true, start

	return s
}

// mariadbGTID builds the event that opens a MariaDB group, standalone where it is one self-committing statement.
func mariadbGTID(at uint32, sequence uint64, standalone bool) *replication.BinlogEvent {
	e := &replication.MariadbGTIDEvent{GTID: gomysql.MariadbGTID{DomainID: 0, ServerID: 4, SequenceNumber: sequence}}
	if standalone {
		e.Flags = replication.BINLOG_MARIADB_FL_STANDALONE
	}

	return event(replication.MARIADB_GTID_EVENT, at, e)
}

func TestAMariaDBPositionReadsBackAsDomainServerSequenceAndRefusesMySQLs(t *testing.T) {
	for _, text := range []capture.Position{"gtid:0-4-10", "gtid:0-4-10,1-7-3", "file:mariadb-bin.000002:344"} {
		got, err := parsePosition(text, true)
		if err != nil {
			t.Fatalf("parsePosition(%q): %v", text, err)
		}

		if spelled := got.spell(); spelled != text {
			t.Errorf("%q read back as %q", text, spelled)
		}
	}

	for mariadb, text := range map[bool]capture.Position{
		true:  "gtid:4ba4da4e-ccba-11f1-8f15-d23d22d3c78b:1-15",
		false: "gtid:0-4-10",
	} {
		if _, err := parsePosition(text, mariadb); err == nil || !strings.Contains(err.Error(), "other flavor") {
			t.Errorf("parsePosition(%q, mariadb %v) returned %v, want the other flavor's set refused", text, mariadb, err)
		}
	}
}

func TestAMariaDBTransactionIsHandedOverAtItsXIDAndMovesTheGTID(t *testing.T) {
	s := mariadbStreamOver(t, "0-4-10")

	feed := []*replication.BinlogEvent{
		mariadbGTID(150, 11, false),
		event(replication.MARIADB_ANNOTATE_ROWS_EVENT, 200, &replication.MariadbAnnotateRowsEvent{Query: []byte("INSERT ...")}),
		event(replication.WRITE_ROWS_EVENTv1, 250, rowsOf("people", []any{int32(1), "ada", int32(2)})),
	}

	for _, e := range feed {
		if changes, err := s.consume(e); err != nil || len(changes) != 0 {
			t.Fatalf("an event before the commit handed over %v (%v)", changes, err)
		}
	}

	if s.through() != "gtid:0-4-10" {
		t.Fatalf("the position is %s before the group committed, want it unmoved", s.through())
	}

	changes, err := s.consume(event(replication.XID_EVENT, 300, &replication.XIDEvent{}))
	if err != nil || len(changes) != 1 {
		t.Fatalf("the XID handed over %v (%v), want the one insert", changes, err)
	}

	end, _ := gomysql.ParseMariadbGTIDSet("0-4-11")
	if s.through() != "gtid:0-4-11" || !s.reached(logEnd{gtid: end}) {
		t.Errorf("after the commit the position is %s, want gtid:0-4-11, reached", s.through())
	}
}

func TestAMariaDBStandaloneGroupCommitsAtItsStatementAndAnAlterOfTheTableStopsTheStream(t *testing.T) {
	s := mariadbStreamOver(t, "0-4-10")

	for _, e := range []*replication.BinlogEvent{
		mariadbGTID(150, 11, true),
		event(replication.QUERY_EVENT, 200, &replication.QueryEvent{Schema: []byte("migr"),
			Query: []byte("CREATE TABLE other (id int)")}),
		event(replication.MARIADB_BINLOG_CHECKPOINT_EVENT, 250, &replication.MariadbBinlogCheckPointEvent{}),
		event(replication.MARIADB_GTID_LIST_EVENT, 300, &replication.MariadbGTIDListEvent{}),
	} {
		if _, err := s.consume(e); err != nil {
			t.Fatal(err)
		}
	}

	if s.through() != "gtid:0-4-11" || s.open {
		t.Errorf("after another table's DDL the position is %s (open %v), want gtid:0-4-11 at a boundary",
			s.through(), s.open)
	}

	if _, err := s.consume(mariadbGTID(350, 12, true)); err != nil {
		t.Fatal(err)
	}

	_, err := s.consume(event(replication.QUERY_EVENT, 400, &replication.QueryEvent{Schema: []byte("migr"),
		Query: []byte("ALTER TABLE people ADD COLUMN x int")}))
	if err == nil || !strings.Contains(err.Error(), "changed under") {
		t.Errorf("an ALTER of the stream's table returned %v, want the stream stopped naming it", err)
	}
}

func TestOnMariaDBTheCopyReadsUnderLockInShareModeAndChecksAreNotRefused(t *testing.T) {
	if got := (&mysqlConn{version: serverVersion{variant: MariaDB, major: 11, minor: 4}}).shareLock(); got != " LOCK IN SHARE MODE" {
		t.Errorf("MariaDB's copy reads %q, and MariaDB refuses FOR SHARE in an INSERT ... SELECT", got)
	}

	if got := (&mysqlConn{version: serverVersion{variant: MySQL, major: 8, minor: 0}}).shareLock(); got != " FOR SHARE" {
		t.Errorf("MySQL's copy reads %q", got)
	}

	c := &mysqlConn{version: serverVersion{variant: MariaDB, major: 11, minor: 4}}
	if got, err := c.namedChecksOf(t.Context(), "people"); got != "" || err != nil {
		t.Errorf("a MariaDB table's checks were refused (%q, %v), though LIKE keeps their names there", got, err)
	}
}

func TestTheRenameMustHoldEveryNameThatSortsBeforeTheTable(t *testing.T) {
	c := &mysqlConn{}

	for table, want := range map[string][]string{
		"people": {"_people_migr_old", "_people_migr_new"},
		"People": {},
		"Ab":     {},
	} {
		got := c.namesBefore(shadowFor(table))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("before %s the rename takes %v first, want %v", table, got, want)
		}
	}
}

// shadowFor names a table's shadow and retired copy as the engine derives them, less the version.
func shadowFor(table string) driver.Shadow {
	return driver.Shadow{Table: table, Shadow: "_" + table + "_migr_new", Retired: "_" + table + "_migr_old"}
}
