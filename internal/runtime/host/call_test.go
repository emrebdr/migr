package host

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

func TestEveryCallbackOutsideACallIsSessionState(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	for _, method := range protocol.HostMethods() {
		if method == protocol.HostLog {
			continue
		}

		t.Run(method, func(t *testing.T) {
			reply := serve(t, server, protocol.NewRequest(1, method, paramsFor(t, method)))
			assertCode(t, reply, protocol.CodeSessionState)
		})
	}
}

func TestEveryCallbackAfterTheCallIsSessionStateAgain(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	if _, err := call.Operations(); err != nil {
		t.Fatalf("sealing: %v", err)
	}

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostInfo, json.RawMessage(`{}`)))
	assertCode(t, reply, protocol.CodeSessionState)
}

func TestAValidationWindowChangesNothing(t *testing.T) {
	conn := newSession(t, capability.Native(capability.LockFree))
	server := newServer(t, conn)

	call, err := server.Validate("20260727T153000")
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	for _, method := range []string{protocol.HostSchemaApply, protocol.HostDBExec} {
		t.Run(method, func(t *testing.T) {
			reply := serve(t, server, protocol.NewRequest(1, method, paramsFor(t, method)))
			assertCode(t, reply, protocol.CodeSessionState)
		})
	}

	if len(conn.writes) != 0 {
		t.Errorf("a validation ran %d statements, and it runs none", len(conn.writes))
	}

	for _, method := range []string{protocol.HostDBQuery, protocol.HostCapabilityGet, protocol.HostInfo} {
		t.Run(method, func(t *testing.T) {
			reply := serve(t, server, protocol.NewRequest(1, method, paramsFor(t, method)))
			if reply.Error != nil {
				t.Errorf("§5.1 allows the read-only callbacks during a validation: %v", reply.Error)
			}
		})
	}
}

func TestAValidationWindowSealsNothing(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	call, err := server.Validate("20260727T153000")
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	if _, err := call.Operations(); !errors.Is(err, ErrNothingRecorded) {
		t.Errorf("Operations returned %v, want %v", err, ErrNothingRecorded)
	}
}

func TestASecondCallWhileOneIsInFlightIsRefused(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	first, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the first call: %v", err)
	}

	if _, err := server.Execute("20260727T153100", ir.Up, protocol.ModeApply); !errors.Is(err, ErrCallOpen) {
		t.Errorf("the second Execute returned %v, want %v", err, ErrCallOpen)
		t.Log("§2.5: the engine has at most one request in flight, so a runtime never has to")
		t.Log("interleave two migrations and this never has to record for two at once.")
	}

	if _, err := server.Validate("20260727T153100"); !errors.Is(err, ErrCallOpen) {
		t.Errorf("Validate returned %v, want %v", err, ErrCallOpen)
	}

	first.Close()

	second, err := server.Execute("20260727T153100", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("a closed call leaves room for the next: %v", err)
	}

	second.Close()
}

func TestClosingTwiceIsSafeAndDoesNotCloseTheNextCall(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	first, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	first.Close()

	second, err := server.Execute("20260727T153100", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening the next: %v", err)
	}

	first.Close()

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostInfo, json.RawMessage(`{}`)))
	if reply.Error != nil {
		t.Errorf("closing a spent call ended the one in flight: %v", reply.Error)
	}

	second.Close()
}

func TestOperationsSealsSoThatNothingCanBeAddedAfterwards(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	if recorded := applied(t, server, `{"operations":[{"op":"drop_table","table":"a"}]}`); recorded != 1 {
		t.Fatalf("recorded %d", recorded)
	}

	ops, err := call.Operations()
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	if len(ops) != 1 {
		t.Fatalf("sealed %d operations", len(ops))
	}

	if err := call.record(ir.Operations{ir.DropTable{Table: "b"}}); !errors.Is(err, ir.ErrSealed) {
		t.Errorf("recording after the seal returned %v, want %v", err, ir.ErrSealed)
		t.Log("What the engine hashes is what it compiles, so a list that can still grow after")
		t.Log("it was read is a checksum for something other than what ran.")
	}
}

func TestACallNamesTheMigrationItIsInFlightFor(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	call, err := server.Execute("20260727T153000", ir.Up, protocol.ModeApply)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	defer call.Close()

	if call.Migration() != "20260727T153000" {
		t.Errorf("the call names %q", call.Migration())
	}
}

func TestACallIsRefusedWhenItNamesNoMigration(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	if _, err := server.Validate("  "); !errors.Is(err, ErrNoMigration) {
		t.Errorf("Validate returned %v, want %v", err, ErrNoMigration)
	}

	if _, err := server.Execute("", ir.Up, protocol.ModeApply); err == nil {
		t.Error("a recorder needs the migration whose operations it collects")
	}

	if _, err := server.Execute("20260727T153000", "sideways", protocol.ModeApply); err == nil {
		t.Error("a direction is up or down, and a recorder is opened for one of them")
	}
}

func TestTheModeIsTheCallsRatherThanTheSessions(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	for _, mode := range protocol.Modes() {
		call := execute(t, server, mode)

		if call.Mode() != mode {
			t.Errorf("a %s call reports mode %q", mode, call.Mode())
		}

		reply := serve(t, server, protocol.NewRequest(1, protocol.HostInfo, json.RawMessage(`{}`)))
		if !strings.Contains(string(reply.Result), `"mode":"`+string(mode)+`"`) {
			t.Errorf("host.info answered %s during a %s call", reply.Result, mode)
			t.Log("§4: the mode belongs to the question being asked, not to the session. One")
			t.Log("rollback issues both -- the down as a run, then the up as the question it is")
			t.Log("inverted from -- so a server carrying one mode could not serve §12's second")
			t.Log("conversation at all.")
		}

		call.Close()
	}
}

func TestAValidationIsAskedAsAQuestion(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	call, err := server.Validate("20260727T153000")
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	defer call.Close()

	if call.Mode() != protocol.ModePlan {
		t.Errorf("a validation window is in mode %q", call.Mode())
	}

	reply := serve(t, server, protocol.NewRequest(1, protocol.HostInfo, json.RawMessage(`{}`)))
	if !strings.Contains(string(reply.Result), `"mode":"plan"`) {
		t.Errorf("host.info answered %s during a validation", reply.Result)
		t.Log("migration.validate carries no mode of its own (§4) and §5.1 lets host.info be")
		t.Log("called inside one, so the host has to answer something: a self-check is a")
		t.Log("question, and nothing it does reaches the database.")
	}
}

func TestACallIsRefusedWhenItStatesNoModeThisVersionDefines(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)))

	for _, mode := range []protocol.Mode{"", "dry-run", "APPLY"} {
		if _, err := server.Execute("20260727T153000", ir.Up, mode); !errors.Is(err, ErrNoMode) {
			t.Errorf("Execute in mode %q returned %v, want %v", mode, err, ErrNoMode)
		}
	}
}
