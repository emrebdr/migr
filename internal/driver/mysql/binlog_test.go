package mysql

import (
	"slices"
	"strings"
	"testing"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

func TestARecordedPositionReadsBackAsTheOneSpelled(t *testing.T) {
	for _, text := range []capture.Position{
		"file:binlog.000002:157",
		"gtid:4ba4da4e-ccba-11f1-8f15-d23d22d3c78b:1-15",
		"gtid:",
	} {
		got, err := parsePosition(text, false)
		if err != nil {
			t.Fatalf("parsePosition(%q): %v", text, err)
		}

		if spelled := got.spell(); spelled != text {
			t.Errorf("%q read back as %q", text, spelled)
		}
	}

	for _, text := range []capture.Position{"", "binlog.000002:157", "file:binlog.000002", "file:binlog.000002:x",
		"file:binlog.000002:3", "gtid:not-a-set"} {
		if _, err := parsePosition(text, false); err == nil {
			t.Errorf("parsePosition(%q) read a position the driver never records", text)
		}
	}
}

func TestAFileAndOffsetIsReachedAcrossARotation(t *testing.T) {
	end := logPoint{file: "binlog.000010", offset: 500}

	for point, want := range map[logPoint]bool{
		{file: "binlog.000010", offset: 499}:  false,
		{file: "binlog.000010", offset: 500}:  true,
		{file: "binlog.000009", offset: 9000}: false,
		{file: "binlog.000011", offset: 4}:    true,
	} {
		if got := point.reached(end); got != want {
			t.Errorf("%v reached %v: %v, want %v", point, end, got, want)
		}
	}
}

func TestAReaderIdIsDerivedHighAndNeverOneAlreadyTaken(t *testing.T) {
	first := freeID("migr_v1_abc", nil)
	if first < replicaIDBase || freeID("migr_v1_abc", nil) != first {
		t.Fatalf("the id %d is not stable in the high range", first)
	}

	next := freeID("migr_v1_abc", map[uint32]bool{first: true})
	if next == first || next < replicaIDBase {
		t.Errorf("an id already taken (%d) was handed out again as %d", first, next)
	}
}

func TestEveryCapturedValueIsRenderedAsTheServerWouldAnswer(t *testing.T) {
	text := "people"
	for _, tc := range []struct {
		column sourceColumn
		value  any
		want   string
	}{
		{sourceColumn{data: "int", class: classInteger}, int32(-5), "-5"},
		{sourceColumn{data: "int", class: classInteger, unsigned: true}, int32(-1), "4294967295"},
		{sourceColumn{data: "tinyint", class: classInteger, unsigned: true}, int8(-1), "255"},
		{sourceColumn{data: "mediumint", class: classInteger, unsigned: true}, int32(-1), "16777215"},
		{sourceColumn{data: "bigint", class: classInteger, unsigned: true}, int64(-1), "18446744073709551615"},
		{sourceColumn{data: "float", class: classFloat}, float32(1.1), "1.100000023841858"},
		{sourceColumn{data: "double", class: classNumber}, float64(0.1), "0.1"},
		{sourceColumn{data: "decimal", class: classNumber}, "12.3400", "12.3400"},
		{sourceColumn{data: "year", class: classNumber}, 2026, "2026"},
		{sourceColumn{data: "bit", class: classBit}, int64(5), "5"},
		{sourceColumn{data: "enum", class: classEnum, labels: []string{"a", "b,c"}}, int64(2), "b,c"},
		{sourceColumn{data: "enum", class: classEnum, labels: []string{"a"}}, int64(0), ""},
		{sourceColumn{data: "set", class: classSet, labels: []string{"x", "y", "z"}}, int64(5), "x,z"},
		{sourceColumn{data: "varchar", class: classText}, text, text},
		{sourceColumn{data: "longblob", class: classBinary}, []byte{0, 255}, "\x00\xff"},
		{sourceColumn{data: "json", class: classJSON}, `{"a":1}`, `{"a":1}`},
		{sourceColumn{data: "timestamp", class: classTemporal}, "2026-10-20 12:00:00.123456", "2026-10-20 12:00:00.123456"},
	} {
		got, err := render(tc.column, tc.value)
		if err != nil || got == nil || *got != tc.want {
			t.Errorf("render(%s, %#v) = %v, %v, want %q", tc.column.data, tc.value, deref(got), err, tc.want)
		}
	}

	if got, err := render(sourceColumn{class: classInteger}, nil); got != nil || err != nil {
		t.Errorf("a NULL rendered as %v, %v", deref(got), err)
	}

	for _, tc := range []struct {
		column sourceColumn
		value  any
	}{
		{sourceColumn{data: "enum", class: classEnum, labels: []string{"a"}}, int64(3)},
		{sourceColumn{data: "set", class: classSet, labels: []string{"a"}}, int64(6)},
		{sourceColumn{data: "point", class: unreplayable}, []byte{1}},
		{sourceColumn{data: "int", class: classInteger}, "7"},
	} {
		if got, err := render(tc.column, tc.value); err == nil {
			t.Errorf("render(%s, %#v) = %v, want a refusal", tc.column.data, tc.value, deref(got))
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}

	return *s
}

func TestACapturedValueIsBoundByItsColumnsClass(t *testing.T) {
	value := "5"

	for _, tc := range []struct {
		column      sourceColumn
		placeholder string
		arg         any
	}{
		{sourceColumn{class: classText, charset: "latin1"}, "CONVERT(? USING latin1)", []byte("5")},
		{sourceColumn{class: classText, charset: "utf8mb4); DROP"}, "CONVERT(? USING utf8mb4)", []byte("5")},
		{sourceColumn{class: classJSON}, "CONVERT(? USING utf8mb4)", []byte("5")},
		{sourceColumn{class: classBinary}, "?", []byte("5")},
		{sourceColumn{class: classBit}, "?", uint64(5)},
		{sourceColumn{class: classInteger}, "?", "5"},
	} {
		placeholder, arg, err := bind(tc.column, &value)
		if err != nil || placeholder != tc.placeholder || !equalArg(arg, tc.arg) {
			t.Errorf("bind(%+v) = %q, %#v, %v, want %q, %#v", tc.column, placeholder, arg, err, tc.placeholder, tc.arg)
		}
	}
}

func equalArg(a, b any) bool {
	if x, ok := a.([]byte); ok {
		y, ok := b.([]byte)

		return ok && string(x) == string(y)
	}

	return a == b
}

func TestEnumLabelsAreReadWithTheirCommasAndQuotes(t *testing.T) {
	got := quotedLabels(`'a','b,c','it''s'`)
	if !slices.Equal(got, []string{"a", "b,c", "it's"}) {
		t.Errorf("read %q", got)
	}
}

func TestAStatementChangingTheStreamsTableIsTold(t *testing.T) {
	s := &binlogStream{database: "migr", table: "people"}

	for statement, want := range map[string]bool{
		"ALTER TABLE people ADD COLUMN x int":                      true,
		"alter table `people` drop column x":                       true,
		"ALTER TABLE `migr`.`people` ENGINE=InnoDB":                true,
		"ALTER TABLE other.people ADD COLUMN x int":                false,
		"ALTER TABLE pets ADD COLUMN people int, ADD COLUMN y int": false,
		"RENAME TABLE pets TO p2, people TO p3":                    true,
		"RENAME TABLE pets TO people":                              false,
		"DROP TABLE IF EXISTS `pets`, `people`":                    true,
		"DROP TABLE IF EXISTS `_people_migr_x_new`":                false,
		"TRUNCATE people":                                          true,
		"TRUNCATE TABLE `people`":                                  true,
		"CREATE TABLE `_people_migr_x_new` LIKE `people`":          false,
		"CREATE INDEX i ON people (x)":                             false,
	} {
		if got := s.changesTable("migr", statement); got != want {
			t.Errorf("%q changes people: %v, want %v", statement, got, want)
		}
	}
}

// streamOver returns a stream of people(id, name, total generated) in file mode, at a known point.
func streamOver() *binlogStream {
	return &binlogStream{database: "migr", table: "people", key: []int{0},
		columns: []sourceColumn{
			{name: "id", data: "int", class: classInteger},
			{name: "name", data: "varchar", class: classText},
			{name: "total", data: "int", class: classInteger, generated: true},
		},
		at: logPoint{file: "binlog.000002", offset: 100}, boundary: logPoint{file: "binlog.000002", offset: 100}}
}

// event builds one event at an offset.
func event(kind replication.EventType, at uint32, e replication.Event) *replication.BinlogEvent {
	return &replication.BinlogEvent{Header: &replication.EventHeader{EventType: kind, LogPos: at, Timestamp: at}, Event: e}
}

// rowsOf builds a row event on a table.
func rowsOf(table string, rows ...[]any) *replication.RowsEvent {
	return &replication.RowsEvent{Table: &replication.TableMapEvent{Schema: []byte("migr"), Table: []byte(table)}, Rows: rows}
}

func TestAChangeIsHandedOverOnlyOnceItsTransactionCommits(t *testing.T) {
	s := streamOver()

	feed := []*replication.BinlogEvent{
		event(replication.QUERY_EVENT, 150, &replication.QueryEvent{Query: []byte("BEGIN")}),
		event(replication.TABLE_MAP_EVENT, 175, &replication.TableMapEvent{Schema: []byte("migr"), Table: []byte("people")}),
		event(replication.WRITE_ROWS_EVENTv2, 200, rowsOf("people", []any{int32(1), "ada", int32(9)})),
		event(replication.WRITE_ROWS_EVENTv2, 250, rowsOf("_people_migr_x_new", []any{int32(1), "ada"})),
		event(replication.UPDATE_ROWS_EVENTv2, 300, rowsOf("people",
			[]any{int32(1), "ada", int32(9)}, []any{int32(2), "ada", int32(9)},
			[]any{int32(2), "ada", int32(9)}, []any{int32(2), "grace", int32(9)})),
		event(replication.DELETE_ROWS_EVENTv2, 350, rowsOf("people", []any{int32(2), "grace", int32(9)})),
	}

	for _, e := range feed {
		committed, err := s.consume(e)
		if err != nil || len(committed) != 0 {
			t.Fatalf("an event inside the transaction handed over %v, %v", committed, err)
		}

		if s.through() != "file:binlog.000002:100" {
			t.Fatalf("the position moved to %s inside a transaction", s.through())
		}
	}

	committed, err := s.consume(event(replication.XID_EVENT, 400, &replication.XIDEvent{}))
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, c := range committed {
		got = append(got, string(c.Kind)+" "+strings.Join(c.Columns, ",")+"="+joined(c.Row))
	}

	want := []string{
		"insert id,name=1,ada",
		"delete id=1", "insert id,name=2,ada",
		"update id,name=2,grace",
		"delete id=2",
	}

	if !slices.Equal(got, want) || s.through() != "file:binlog.000002:400" {
		t.Errorf("the transaction committed %q through %s, want %q through file:binlog.000002:400", got, s.through(), want)
	}
}

func joined(row []*string) string {
	parts := make([]string, len(row))
	for i, v := range row {
		parts[i] = deref(v)
	}

	return strings.Join(parts, ",")
}

func TestAPositionMovesPastEventsOutsideAnyTransactionButNotPastAHeartbeat(t *testing.T) {
	s := streamOver()

	if _, err := s.consume(event(replication.HEARTBEAT_EVENT, 999, nil)); err != nil || s.through() != "file:binlog.000002:100" {
		t.Fatalf("a heartbeat moved the position to %s (%v)", s.through(), err)
	}

	if _, err := s.consume(event(replication.ROTATE_EVENT, 0, &replication.RotateEvent{Position: 4,
		NextLogName: []byte("binlog.000003")})); err != nil {
		t.Fatal(err)
	}

	if _, err := s.consume(event(replication.FORMAT_DESCRIPTION_EVENT, 126, &replication.FormatDescriptionEvent{})); err != nil {
		t.Fatal(err)
	}

	if s.through() != "file:binlog.000003:126" {
		t.Errorf("after a rotation the position is %s, want file:binlog.000003:126", s.through())
	}
}

func TestAGTIDPositionGrowsByEachCommittedTransaction(t *testing.T) {
	start, err := gomysql.ParseMysqlGTIDSet("4ba4da4e-ccba-11f1-8f15-d23d22d3c78b:1-15")
	if err != nil {
		t.Fatal(err)
	}

	s := streamOver()
	s.gtid, s.committed = true, start.Clone()

	sid := []byte{0x4b, 0xa4, 0xda, 0x4e, 0xcc, 0xba, 0x11, 0xf1, 0x8f, 0x15, 0xd2, 0x3d, 0x22, 0xd3, 0xc7, 0x8b}

	for _, e := range []*replication.BinlogEvent{
		event(replication.GTID_EVENT, 150, &replication.GTIDEvent{SID: sid, GNO: 16}),
		event(replication.QUERY_EVENT, 200, &replication.QueryEvent{Query: []byte("BEGIN")}),
	} {
		if _, err := s.consume(e); err != nil {
			t.Fatal(err)
		}
	}

	if s.through() != "gtid:4ba4da4e-ccba-11f1-8f15-d23d22d3c78b:1-15" {
		t.Fatalf("the position is %s before the transaction committed", s.through())
	}

	if _, err := s.consume(event(replication.XID_EVENT, 300, &replication.XIDEvent{})); err != nil {
		t.Fatal(err)
	}

	end, _ := gomysql.ParseMysqlGTIDSet("4ba4da4e-ccba-11f1-8f15-d23d22d3c78b:1-16")
	if s.through() != "gtid:4ba4da4e-ccba-11f1-8f15-d23d22d3c78b:1-16" || !s.reached(logEnd{gtid: end}) {
		t.Errorf("after the commit the position is %s, want the set through 16, reached", s.through())
	}
}

func TestTheStreamStopsWhereTheTableChangesUnderIt(t *testing.T) {
	s := streamOver()

	if _, err := s.consume(event(replication.QUERY_EVENT, 200, &replication.QueryEvent{Schema: []byte("migr"),
		Query: []byte("ALTER TABLE people ADD COLUMN x int")})); err == nil || !strings.Contains(err.Error(), "changed under") {
		t.Errorf("an ALTER of the stream's table returned %v", err)
	}

	if _, err := streamOver().consume(event(replication.WRITE_ROWS_EVENTv2, 200,
		rowsOf("people", []any{int32(1), "ada"}))); err == nil || !strings.Contains(err.Error(), "2 columns") {
		t.Errorf("a row of the wrong width returned %v", err)
	}

	committed, err := streamOver().consume(event(replication.QUERY_EVENT, 200, &replication.QueryEvent{Schema: []byte("migr"),
		Query: []byte("CREATE TABLE `_people_migr_x_new` LIKE `people`")}))
	if err != nil || len(committed) != 0 {
		t.Errorf("the shadow's own CREATE stopped the stream: %v, %v", committed, err)
	}
}

func TestTheShadowerRefusesWhatACopyWouldGetWrong(t *testing.T) {
	c := &mysqlConn{}

	for _, op := range []ir.Operation{
		ir.AddColumn{Table: "people"}, ir.DropColumn{Table: "people"}, ir.AlterColumn{Table: "people"},
		ir.AddIndex{Table: "people"}, ir.DropIndex{Table: "people"}, ir.AddCheck{Table: "people"},
		ir.DropUnique{Table: "people"},
	} {
		if err := c.Shadows(op); err != nil {
			t.Errorf("%s was refused: %v", op.Kind(), err)
		}
	}

	for _, tc := range []struct {
		op   ir.Operation
		says string
	}{
		{ir.AddUnique{Table: "people"}, "silently drop"},
		{ir.RenameColumn{Table: "people"}, "by name"},
		{ir.DropCheck{Table: "people"}, "LIKE"},
		{ir.AddForeignKey{Table: "people"}, "retired name"},
		{ir.Raw{Driver: "mysql", SQL: "x"}, "not a change"},
		{ir.DataTransform{Table: "people"}, "not a change"},
		{ir.DropForeignKey{Table: "people"}, "retired name"},
	} {
		if err := c.Shadows(tc.op); err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s returned %v, want a refusal saying %q", tc.op.Kind(), err, tc.says)
		}
	}
}

func TestAServerTheReaderCannotReadIsRefusedByName(t *testing.T) {
	for v, says := range map[serverVersion]string{
		{variant: MariaDB, major: 10, minor: 11}:        "measured from 11.4",
		{variant: MySQL, major: 8, minor: 0, patch: 12}: "8.0.13",
	} {
		if got := streamRefusal(v); !strings.Contains(got, says) {
			t.Errorf("%s was refused with %q, want it to say %q", v, got, says)
		}
	}

	if got := streamRefusal(serverVersion{variant: MySQL, major: 8, minor: 0, patch: 13}); got != "" {
		t.Errorf("8.0.13 was refused: %s", got)
	}

	if got := streamRefusal(serverVersion{variant: MariaDB, major: 11, minor: 4}); got != "" {
		t.Errorf("MariaDB 11.4, where the cutover was measured, was refused: %s", got)
	}
}

func TestTheShadowIsCreatedLikeTheTableAndChangedByName(t *testing.T) {
	typed, err := schema.Parse("int64")
	if err != nil {
		t.Fatal(err)
	}

	s := driver.Shadow{Table: "people", Shadow: "_people_new", Retired: "_people_old", Key: []string{"id"},
		Ops: ir.Operations{
			ir.AddColumn{Table: "people", Column: ir.Column{Name: "age", Type: typed, Nullable: true}},
			ir.AddIndex{Table: "people", Name: "people_age", Columns: []string{"age"}},
		}}

	stmts, err := (&mysqlConn{}).CreateShadow(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, stmt := range stmts {
		got = append(got, stmt.SQL)
	}

	if len(got) != 3 || got[0] != "CREATE TABLE `_people_new` LIKE `people`;" ||
		!strings.Contains(got[1], "`_people_new`") || !strings.Contains(got[2], "`_people_new`") {
		t.Errorf("the shadow is created by %q", got)
	}
}

func TestAReplayIsAReplaceOrADeleteByKeyBoundByClass(t *testing.T) {
	c := &mysqlConn{}
	s := driver.Shadow{Table: "people", Shadow: "_people_new", Key: []string{"id"}}
	p := pair{byName: map[string]sourceColumn{
		"id":   {name: "id", class: classInteger},
		"name": {name: "name", class: classText, charset: "utf8mb4"},
		"gone": {name: "gone", class: classInteger},
	}, writes: map[string]bool{"id": true, "name": true}}

	one, two, ada := "1", "2", "ada"
	upserts := []capture.Change{
		{Kind: capture.Insert, Columns: []string{"id", "name", "gone"}, Row: []*string{&one, &ada, &one}},
		{Kind: capture.Update, Columns: []string{"id", "name", "gone"}, Row: []*string{&two, nil, &two}},
	}

	statement, args, err := c.applyStatement(s, p, upserts)
	if err != nil || statement != "REPLACE INTO `_people_new` (`id`, `name`) VALUES (?, CONVERT(? USING utf8mb4)), (?, ?)" ||
		len(args) != 4 || args[3] != nil {
		t.Errorf("an upsert renders %q with %#v (%v)", statement, args, err)
	}

	statement, args, err = c.applyStatement(s, p, []capture.Change{
		{Kind: capture.Delete, Columns: []string{"id"}, Row: []*string{&one}},
		{Kind: capture.Delete, Columns: []string{"id"}, Row: []*string{&two}},
	})
	if err != nil || statement != "DELETE FROM `_people_new` WHERE (`id` = ?) OR (`id` = ?)" || len(args) != 2 {
		t.Errorf("a delete renders %q with %#v (%v)", statement, args, err)
	}
}

func TestTheNextAutoIncrementIsReadFromTheTablesOptionsAlone(t *testing.T) {
	created := "CREATE TABLE `people` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  PRIMARY KEY (`id`)\n) " +
		"ENGINE=InnoDB AUTO_INCREMENT=42 DEFAULT CHARSET=utf8mb4"

	if got := autoIncrementOption.FindStringSubmatch(created); got == nil || got[1] != "42" {
		t.Errorf("read %v", got)
	}

	if got := autoIncrementOption.FindStringSubmatch("CREATE TABLE `p` (\n  `id` int NOT NULL AUTO_INCREMENT\n) ENGINE=InnoDB"); got != nil {
		t.Errorf("a table with no counter yet read %v", got)
	}
}
