package host

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

func TestEveryCallbackIsServed(t *testing.T) {
	conn := newSession(t, capability.Native(capability.LockFree))
	conn.rows = driver.Rows{Columns: []string{"n"}, Values: [][]*string{{text("4711")}}}
	conn.affected = 7

	server := newServer(t, conn)

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	answered := make(map[string]bool, len(protocol.HostMethods()))

	for _, method := range protocol.HostMethods() {
		params := paramsFor(t, method)

		if method == protocol.HostLog {
			if reply := serve(t, server, protocol.NewNotification(method, params)); reply != nil {
				t.Errorf("%s is a notification, and this answered it", method)
			}

			answered[method] = true

			continue
		}

		reply := serve(t, server, protocol.NewRequest(1, method, params))
		if reply == nil {
			t.Errorf("%s went unanswered", method)

			continue
		}

		if reply.Error != nil {
			t.Errorf("%s was refused: %v", method, reply.Error)

			continue
		}

		answered[method] = true
	}

	for _, method := range protocol.HostMethods() {
		if !answered[method] {
			t.Errorf("%s is a callback this host does not serve", method)
			t.Log("The set comes from protocol.HostMethods(), so a seventh callback fails this")
			t.Log("suite the day it is added rather than the day something calls it.")
		}
	}

	call.Close()
}

func TestSchemaApplyRecordsInOrderAcrossCalls(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	first := `{"operations":[{"op":"drop_table","table":"a"}]}`
	second := `{"operations":[{"op":"drop_table","table":"b"},{"op":"drop_table","table":"c"}]}`

	if recorded := applied(t, server, first); recorded != 1 {
		t.Errorf("the first call recorded %d", recorded)
	}

	if recorded := applied(t, server, second); recorded != 2 {
		t.Errorf("the second call recorded %d", recorded)
	}

	ops, err := call.Operations()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	if got := tables(t, ops); got != "a,b,c" {
		t.Errorf("the operations sealed as %q, and several calls accumulate in the order made", got)
	}
}

func TestAnOperationThatIsNotOneIsRefusedAsOne(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostSchemaApply,
		json.RawMessage(`{"operations":[{"op":"drop_table"}]}`)))

	assertCode(t, reply, protocol.CodeInvalidOperation)
}

func TestAReadReachesTheDriverAndComesBackAsText(t *testing.T) {
	conn := newSession(t, capability.Native(capability.LockFree))
	conn.rows = driver.Rows{
		Columns: []string{"n", "note"},
		Values:  [][]*string{{text("9007199254740993"), nil}},
	}

	server := newServer(t, conn)

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostDBQuery,
		json.RawMessage(`{"sql":"SELECT n, note FROM t WHERE id = $1","args":["7"]}`)))

	if reply.Error != nil {
		t.Fatalf("the read was refused: %v", reply.Error)
	}

	if len(conn.reads) != 1 || conn.reads[0].SQL != "SELECT n, note FROM t WHERE id = $1" {
		t.Fatalf("the driver was asked %#v", conn.reads)
	}

	if len(conn.reads[0].Args) != 1 || conn.reads[0].Args[0] != "7" {
		t.Errorf("the bound parameters arrived as %#v", conn.reads[0].Args)
	}

	want := `{"columns":["n","note"],"rows":[["9007199254740993",null]]}`
	if string(reply.Result) != want {
		t.Errorf("the read answered\n  %s\nwant\n  %s", reply.Result, want)
		t.Log("Every value is a string or null: a bigint past 2^53 does not survive a JSON")
		t.Log("number in JavaScript, and a null is not an empty string.")
	}
}

func TestAReadThatReturnedNothingIsEmptyRatherThanNull(t *testing.T) {
	conn := newSession(t, capability.Native(capability.LockFree))
	conn.rows = driver.Rows{}

	server := newServer(t, conn)

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostDBQuery,
		json.RawMessage(`{"sql":"CALL nothing()","args":[]}`)))

	if reply.Error != nil {
		t.Fatalf("refused: %v", reply.Error)
	}

	want := `{"columns":[],"rows":[]}`
	if string(reply.Result) != want {
		t.Errorf("answered %s, want %s", reply.Result, want)
		t.Log("A null where an array belongs is a TypeScript SDK reading .length off nothing,")
		t.Log("which fails in the migration rather than here.")
	}
}

func TestAResultTheHostCannotRenderIsTheHostsOwnBug(t *testing.T) {
	conn := newSession(t, capability.Native(capability.LockFree))
	conn.rows = driver.Rows{Columns: []string{"a", "b"}, Values: [][]*string{{text("1")}}}

	server := newServer(t, conn)

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostDBQuery,
		json.RawMessage(`{"sql":"SELECT a, b FROM t","args":[]}`)))

	assertCode(t, reply, protocol.CodeInternal)

	if !strings.Contains(reply.Error.Message, "postgres") {
		t.Errorf("the refusal says %q and never names the driver that produced it", reply.Error.Message)
		t.Log("A row narrower than its columns is not the database's fault and not the")
		t.Log("migration's. Naming the driver is what makes it findable.")
	}
}

func TestAWriteTheServerRefusedIsReportedAsARefusalAndNotAsAFailure(t *testing.T) {
	conn := newSession(t, capability.Native(capability.LockFree))
	conn.readErr = driver.ErrNotReadOnly

	server := newServer(t, conn)

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostDBQuery,
		json.RawMessage(`{"sql":"WITH d AS (DELETE FROM users RETURNING *) SELECT count(*) FROM d","args":[]}`)))

	assertCode(t, reply, protocol.CodeNotReadOnly)

	conn.readErr = errors.New("relation \"nope\" does not exist")

	other := serve(t, server, protocol.NewRequest(2, protocol.HostDBQuery,
		json.RawMessage(`{"sql":"SELECT 1 FROM nope","args":[]}`)))

	assertCode(t, other, protocol.CodeDatabase)
}

func TestTheEscapeHatchRunsWhenApplyingAndIsHeldWhenPlanning(t *testing.T) {
	for _, c := range []struct {
		mode     protocol.Mode
		want     string
		performs int
		held     int
	}{
		{protocol.ModeApply, `{"rowsAffected":"7","executed":true}`, 1, 0},
		{protocol.ModePlan, `{"rowsAffected":"0","executed":false}`, 0, 1},
	} {
		t.Run(string(c.mode), func(t *testing.T) {
			conn := newSession(t, capability.Native(capability.LockFree))
			conn.affected = 7

			call := execute(t, newServer(t, conn), c.mode)
			defer call.Close()

			reply := serve(t, call.server, protocol.NewRequest(1, protocol.HostDBExec,
				json.RawMessage(`{"sql":"ANALYZE users","args":[]}`)))

			if reply.Error != nil {
				t.Fatalf("refused: %v", reply.Error)
			}

			if string(reply.Result) != c.want {
				t.Errorf("answered %s, want %s", reply.Result, c.want)
			}

			if len(conn.writes) != c.performs {
				t.Errorf("the driver ran %d statements, want %d", len(conn.writes), c.performs)
			}

			if len(call.Statements()) != c.held {
				t.Errorf("the call held %d statements, want %d", len(call.Statements()), c.held)
			}
		})
	}
}

func TestCapabilityAnswersForAWholeOperationOnBothAxes(t *testing.T) {
	conn := newSession(t, capability.Emulated("table rebuild", capability.TableRewrite))
	server := newServer(t, conn)

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostCapabilityGet,
		json.RawMessage(`{"operation":{"op":"drop_column","table":"users","column":"legacy"}}`)))

	if reply.Error != nil {
		t.Fatalf("refused: %v", reply.Error)
	}

	want := `{"support":"emulated","strategy":"table rebuild","lock":"table_rewrite"}`
	if string(reply.Result) != want {
		t.Errorf("answered %s, want %s", reply.Result, want)
	}
}

func TestInfoReportsWhatAMigrationMayBranchOn(t *testing.T) {
	conn := newSession(t, capability.Native(capability.LockFree))
	server := newServer(t, conn)

	call := execute(t, server, protocol.ModePlan)
	defer call.Close()

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostInfo, json.RawMessage(`{}`)))

	if reply.Error != nil {
		t.Fatalf("refused: %v", reply.Error)
	}

	want := `{"database":"default","driver":"postgres","server":"17.2","transactionalDDL":true,"mode":"plan"}`
	if string(reply.Result) != want {
		t.Errorf("answered %s, want %s", reply.Result, want)
	}
}

func TestALogLineReachesTheEnginesOwnLogger(t *testing.T) {
	kept := &logged{}
	conn := newSession(t, capability.Native(capability.LockFree))

	server, err := New(Options{
		Conn: conn, Database: "default", Log: slog.New(kept),
	})
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}

	body := `{"level":"warn","message":"backfilling in chunks","fields":{"chunk":"5000","table":"users"}}`
	if reply := serve(t, server, protocol.NewNotification(protocol.HostLog, json.RawMessage(body))); reply != nil {
		t.Error("host.log is a notification, and a notification is never answered")
	}

	if len(kept.records) != 1 {
		t.Fatalf("the logger kept %d records", len(kept.records))
	}

	record := kept.records[0]
	if record.Level != slog.LevelWarn || record.Message != "backfilling in chunks" {
		t.Errorf("the line was filed as %v %q", record.Level, record.Message)
	}

	if got := attrs(record); got["table"] != "users" || got["chunk"] != "5000" {
		t.Errorf("the fields arrived as %v", got)
	}
}

func TestALogLineThisVersionDoesNotDefineIsKeptRatherThanDropped(t *testing.T) {
	kept := &logged{}
	conn := newSession(t, capability.Native(capability.LockFree))

	server, err := New(Options{
		Conn: conn, Database: "default", Log: slog.New(kept),
	})
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}

	body := `{"level":"trace","message":"something happened"}`
	serve(t, server, protocol.NewNotification(protocol.HostLog, json.RawMessage(body)))

	if len(kept.records) != 1 {
		t.Fatalf("the logger kept %d records, and a notification cannot be refused", len(kept.records))
	}

	if kept.records[0].Level != slog.LevelWarn {
		t.Errorf("the complaint was filed at %v", kept.records[0].Level)
	}
}

func TestAMethodThisHostDoesNotServeIsMethodNotFound(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	reply := serve(t, server, protocol.NewRequest(1, "host.progress", json.RawMessage(`{}`)))
	assertCode(t, reply, protocol.CodeMethodNotFound)

	call.Close()

	outside := serve(t, server, protocol.NewRequest(2, "host.progress", json.RawMessage(`{}`)))
	assertCode(t, outside, protocol.CodeMethodNotFound)
	t.Log("A method that does not exist is a stronger fact than a window that is shut, so it")
	t.Log("is answered before the window is consulted. Otherwise an SDK calling a method from")
	t.Log("a later version at the wrong moment is told to try again, which will never work.")
}

func TestFieldsAreOrderedByKeyRatherThanByMapIteration(t *testing.T) {
	got := fields(map[string]string{"table": "users", "chunk": "5000", "batch": "3"})

	want := []any{"batch", "3", "chunk", "5000", "table", "users"}
	if len(got) != len(want) {
		t.Fatalf("fields returned %v", got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Log("Invariant #8: sort keys before iterating. Map order is nobody's to depend on,")
			t.Log("and a log line whose attributes shuffle between runs is one nothing can diff.")
			t.Fatalf("fields returned %v, want %v", got, want)
		}
	}

	if fields(nil) == nil {
		t.Error("a line with no fields still has an empty attribute list to log")
	}
}

func TestParametersOfTheWrongShapeAreInvalidParams(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	refusals := []struct {
		name   string
		method string
		params string
	}{
		{"a read with no statement", protocol.HostDBQuery, `{"args":[]}`},
		{"a statement with no text", protocol.HostDBExec, `{"sql":""}`},
		{"a read whose sql is not a string", protocol.HostDBQuery, `{"sql":7}`},
	}

	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			reply := serve(t, server, protocol.NewRequest(1, c.method, json.RawMessage(c.params)))
			assertCode(t, reply, protocol.CodeInvalidParams)
		})
	}
}

func TestHostLogCarryingAnIDIsNotARequestThisVersionDefines(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostLog,
		json.RawMessage(`{"level":"info","message":"x"}`)))

	assertCode(t, reply, protocol.CodeInvalidRequest)
}

func TestAResponseHandedToTheServerIsNotItsToAnswer(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	if reply := server.Serve(t.Context(), protocol.NewResponse(1, json.RawMessage(`{}`))); reply != nil {
		t.Error("a response travels the other way, and the router never hands one here")
	}

	if reply := server.Serve(t.Context(), nil); reply != nil {
		t.Error("there is nothing to answer")
	}
}

func TestAServerRefusesWhatItCouldNotAnswerWith(t *testing.T) {
	conn := newSession(t, capability.Native(capability.LockFree))
	log := slog.New(slog.NewTextHandler(nil, nil))

	refusals := []struct {
		name string
		opts Options
		want error
	}{
		{"no session", Options{Database: "d", Log: log}, ErrNoConn},
		{"no database", Options{Conn: conn, Log: log}, ErrNoDatabase},
		{"no logger", Options{Conn: conn, Database: "d"}, ErrNoLog},
	}

	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.opts); !errors.Is(err, c.want) {
				t.Errorf("New returned %v, want %v", err, c.want)
			}
		})
	}
}

// serve answers one message the way the router will, through the codec rather than through Go.
func serve(t *testing.T, server *Server, m *protocol.Message) *protocol.Message {
	t.Helper()

	line, err := protocol.Encode(m)
	if err != nil {
		t.Fatalf("encoding the call: %v", err)
	}

	arrived, err := protocol.Decode(line)
	if err != nil {
		t.Fatalf("decoding the call: %v", err)
	}

	reply := server.Serve(t.Context(), arrived)
	if reply == nil {
		return nil
	}

	back, err := protocol.Encode(reply)
	if err != nil {
		t.Fatalf("encoding the answer: %v", err)
	}

	returned, err := protocol.Decode(back)
	if err != nil {
		t.Fatalf("the answer is not a message this protocol defines: %v", err)
	}

	return returned
}

// applied sends one host.schema.apply and reports how many operations it recorded.
func applied(t *testing.T, server *Server, params string) int {
	t.Helper()

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostSchemaApply, json.RawMessage(params)))
	if reply.Error != nil {
		t.Fatalf("refused: %v", reply.Error)
	}

	var result protocol.SchemaApplyResult
	if err := protocol.UnmarshalPayload(reply.Result, &result); err != nil {
		t.Fatalf("reading the result: %v", err)
	}

	return result.Recorded
}

// tables renders the tables a list of drop_table operations names, in order.
func tables(t *testing.T, ops ir.Operations) string {
	t.Helper()

	names := make([]string, len(ops))

	for i, op := range ops {
		drop, ok := op.(ir.DropTable)
		if !ok {
			t.Fatalf("operation %d is a %T", i, op)
		}

		names[i] = drop.Table
	}

	return strings.Join(names, ",")
}

// paramsFor builds the smallest parameters each callback accepts.
func paramsFor(t *testing.T, method string) json.RawMessage {
	t.Helper()

	switch method {
	case protocol.HostSchemaApply:
		return json.RawMessage(`{"operations":[{"op":"drop_table","table":"users"}]}`)
	case protocol.HostDBQuery:
		return json.RawMessage(`{"sql":"SELECT 1","args":[]}`)
	case protocol.HostDBExec:
		return json.RawMessage(`{"sql":"ANALYZE users","args":[]}`)
	case protocol.HostCapabilityGet:
		return json.RawMessage(`{"operation":{"op":"drop_table","table":"users"}}`)
	case protocol.HostInfo:
		return json.RawMessage(`{}`)
	case protocol.HostLog:
		return json.RawMessage(`{"level":"info","message":"x"}`)
	default:
		t.Fatalf("no parameters are known for %q, which is a callback this suite does not cover", method)

		return nil
	}
}

// assertCode reports whether a refusal carries the code §7 assigns it, and a slug beside it.
func assertCode(t *testing.T, reply *protocol.Message, want protocol.Code) {
	t.Helper()

	if reply == nil {
		t.Fatal("the callback went unanswered")
	}

	if reply.Error == nil {
		t.Fatalf("the callback answered %s rather than refusing", reply.Result)
	}

	if reply.Error.Code != want {
		t.Errorf("refused with %d (%s), want %d (%s): %v",
			reply.Error.Code, reply.Error.Code, want, want, reply.Error)
	}

	if reply.Error.Slug() == "" {
		t.Errorf("the refusal carries no data.slug, which §7.1 requires: %v", reply.Error)
	}
}

// text returns a pointer to a value, which is how a column that is not null crosses the wire.
func text(s string) *string {
	return &s
}
