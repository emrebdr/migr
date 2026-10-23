package host

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

// sdk is a fixture runtime: it makes one callback at a time, through the codec, and reads the answer.
type sdk struct {
	t      *testing.T
	server *Server
	id     int64
}

// call makes one callback and returns what it answered, failing the test on a refusal.
func (s *sdk) call(method, params string) json.RawMessage {
	s.t.Helper()

	s.id++

	reply := serve(s.t, s.server, protocol.NewRequest(s.id, method, json.RawMessage(params)))
	if reply.Error != nil {
		s.t.Fatalf("%s was refused: %v", method, reply.Error)
	}

	return reply.Result
}

// count reads the first value of the first row a query answered, which fixtures branch on.
func (s *sdk) count(query string) string {
	s.t.Helper()

	var result protocol.DBQueryResult
	if err := protocol.UnmarshalPayload(s.call(protocol.HostDBQuery, query), &result); err != nil {
		s.t.Fatalf("reading what the query answered: %v", err)
	}

	if len(result.Rows) == 0 || len(result.Rows[0]) == 0 || result.Rows[0][0] == nil {
		s.t.Fatalf("the query answered %+v, and this fixture branches on a count", result)
	}

	return *result.Rows[0][0]
}

// fixture is one migration's own code: the callbacks it makes, in the order it makes them.
type fixture func(s *sdk)

const (
	// createUsers is the operation the §12 conversation emits, spelled as that conversation spells it.
	createUsers = `{"operations":[{"op":"create_table","table":"users","columns":` +
		`[{"name":"id","type":"int64","nullable":false}]}]}`
	// addEmail is what the branching fixture emits instead, when the table is already there.
	addEmail = `{"operations":[{"op":"add_column","table":"users","column":` +
		`{"name":"email","type":"string(255)","nullable":true}}]}`
	// countUsers is the question both fixtures ask before deciding anything.
	countUsers = `{"sql":"SELECT count(*) AS n FROM users","args":[]}`
	// analyze is the escape hatch: a statement the Operation IR cannot express.
	analyze = `{"sql":"ANALYZE users","args":[]}`
)

// straightLine reads, emits, and reaches for the escape hatch, branching on none of it.
func straightLine(s *sdk) {
	s.call(protocol.HostDBQuery, countUsers)
	s.call(protocol.HostSchemaApply, createUsers)
	s.call(protocol.HostDBExec, analyze)
	s.call(protocol.HostSchemaApply, `{"operations":[{"op":"drop_table","table":"legacy"}]}`)
}

// branching emits one thing or another depending on what the database answered.
func branching(s *sdk) {
	if s.count(countUsers) == "0" {
		s.call(protocol.HostSchemaApply, createUsers)

		return
	}

	s.call(protocol.HostSchemaApply, addEmail)
}

// counted is a result one row wide holding a count, which is what the fixtures read.
func counted(n string) driver.Rows {
	return driver.Rows{Columns: []string{"n"}, Values: [][]*string{{&n}}}
}

// ran executes one fixture in one mode and returns the IR it emitted, canonically encoded.
func ran(t *testing.T, conn driver.Conn, mode protocol.Mode, f fixture) ([]byte, *Call) {
	t.Helper()

	server := newServer(t, conn)
	call := execute(t, server, mode)

	f(&sdk{t: t, server: server})

	ops, err := call.Operations()
	if err != nil {
		t.Fatalf("sealing what the %s run emitted: %v", mode, err)
	}

	document, err := ir.Marshal(ops)
	if err != nil {
		t.Fatalf("marshaling what the %s run emitted: %v", mode, err)
	}

	return document, call
}

func TestPlanAndApplyEmitIdenticalIRForAMigrationThatDoesNotBranchOnData(t *testing.T) {
	emitted := make(map[protocol.Mode][]byte, len(protocol.Modes()))

	for _, mode := range protocol.Modes() {
		conn := newSession(t, capability.Native(capability.LockFree))
		conn.rows = counted("0")
		conn.affected = 7

		document, call := ran(t, conn, mode, straightLine)
		emitted[mode] = document

		if len(conn.reads) != 1 {
			t.Errorf("in %s the migration's read reached the driver %d times, want once",
				mode, len(conn.reads))
			t.Log("§6: host.db.query executes in both modes. A preview that answered it from")
			t.Log("nothing would take a branch the apply will not, which is the one way a plan")
			t.Log("can be wrong that a user cannot detect.")
		}

		assertEscapeHatch(t, conn, call, mode)
	}

	if !bytes.Equal(emitted[protocol.ModeApply], emitted[protocol.ModePlan]) {
		t.Errorf("applying emitted %s and planning emitted %s",
			emitted[protocol.ModeApply], emitted[protocol.ModePlan])
		t.Log("This is what makes migr plan worth reading: the operations a preview reports are")
		t.Log("the operations the apply will run, byte for byte, and the checksum covers that")
		t.Log("same encoding. host.schema.apply records in both modes and always did.")
	}
}

// assertEscapeHatch checks that host.db.exec reached the database when applying and was held when not.
func assertEscapeHatch(t *testing.T, conn *session, call *Call, mode protocol.Mode) {
	t.Helper()

	performed, held := 1, 0
	if mode == protocol.ModePlan {
		performed, held = 0, 1
	}

	if len(conn.writes) != performed {
		t.Errorf("in %s the escape hatch reached the driver %d times, want %d",
			mode, len(conn.writes), performed)
	}

	if len(call.Statements()) != held {
		t.Errorf("in %s the call held %d statements, want %d", mode, len(call.Statements()), held)
	}
}

func TestAMigrationThatBranchesOnDataPlansAsItAppliesWhileTheDataIsUnchanged(t *testing.T) {
	emitted := make(map[protocol.Mode][]byte, len(protocol.Modes()))

	for _, mode := range protocol.Modes() {
		conn := newSession(t, capability.Native(capability.LockFree))
		conn.rows = counted("0")

		emitted[mode], _ = ran(t, conn, mode, branching)
	}

	if !bytes.Equal(emitted[protocol.ModeApply], emitted[protocol.ModePlan]) {
		t.Errorf("applying emitted %s and planning emitted %s",
			emitted[protocol.ModeApply], emitted[protocol.ModePlan])
		t.Log("The branch is data-dependent, but the data did not change: the read executes")
		t.Log("live in both modes, so both take the same one. That is what the done-when's")
		t.Log("\"no data-dependent branching\" is worth once the read is honest.")
	}
}

func TestAMigrationThatBranchesOnDataMayPlanDifferentlyThanItApplies(t *testing.T) {
	planning := newSession(t, capability.Native(capability.LockFree))
	planning.rows = counted("0")

	previewed, _ := ran(t, planning, protocol.ModePlan, branching)

	applying := newSession(t, capability.Native(capability.LockFree))
	applying.rows = counted("41")

	applied, _ := ran(t, applying, protocol.ModeApply, branching)

	if bytes.Equal(previewed, applied) {
		t.Errorf("the preview and the apply both emitted %s across a change to the data", applied)
		t.Log("§6 documents this rather than hiding it: a migration may branch on live results,")
		t.Log("so a plan reflects the state at plan time and the apply the state at apply time.")
		t.Log("A fixture that could not tell the two apart would make the test above vacuous.")
	}
}
