package host

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

func TestEveryCallbackOutsideACallIsSessionState(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)), protocol.ModeApply)

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
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)), protocol.ModeApply)

	call, err := server.Execute("20260727T153000", ir.Up)
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
	server := newServer(t, conn, protocol.ModeApply)

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
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)), protocol.ModeApply)

	call, err := server.Validate("20260727T153000")
	if err != nil {
		t.Fatalf("opening the call: %v", err)
	}

	if _, err := call.Operations(); !errors.Is(err, ErrNothingRecorded) {
		t.Errorf("Operations returned %v, want %v", err, ErrNothingRecorded)
	}
}

func TestASecondCallWhileOneIsInFlightIsRefused(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)), protocol.ModeApply)

	first, err := server.Execute("20260727T153000", ir.Up)
	if err != nil {
		t.Fatalf("opening the first call: %v", err)
	}

	if _, err := server.Execute("20260727T153100", ir.Up); !errors.Is(err, ErrCallOpen) {
		t.Errorf("the second Execute returned %v, want %v", err, ErrCallOpen)
		t.Log("§2.5: the engine has at most one request in flight, so a runtime never has to")
		t.Log("interleave two migrations and this never has to record for two at once.")
	}

	if _, err := server.Validate("20260727T153100"); !errors.Is(err, ErrCallOpen) {
		t.Errorf("Validate returned %v, want %v", err, ErrCallOpen)
	}

	first.Close()

	second, err := server.Execute("20260727T153100", ir.Up)
	if err != nil {
		t.Fatalf("a closed call leaves room for the next: %v", err)
	}

	second.Close()
}

func TestClosingTwiceIsSafeAndDoesNotCloseTheNextCall(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)), protocol.ModeApply)

	first, err := server.Execute("20260727T153000", ir.Up)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	first.Close()

	second, err := server.Execute("20260727T153100", ir.Up)
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
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)), protocol.ModeApply)

	call, err := server.Execute("20260727T153000", ir.Up)
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
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)), protocol.ModeApply)

	call, err := server.Execute("20260727T153000", ir.Up)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	defer call.Close()

	if call.Migration() != "20260727T153000" {
		t.Errorf("the call names %q", call.Migration())
	}
}

func TestACallIsRefusedWhenItNamesNoMigration(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)), protocol.ModeApply)

	if _, err := server.Validate("  "); !errors.Is(err, ErrNoMigration) {
		t.Errorf("Validate returned %v, want %v", err, ErrNoMigration)
	}

	if _, err := server.Execute("", ir.Up); err == nil {
		t.Error("a recorder needs the migration whose operations it collects")
	}

	if _, err := server.Execute("20260727T153000", "sideways"); err == nil {
		t.Error("a direction is up or down, and a recorder is opened for one of them")
	}
}

func TestModeIsReportedAsItWasBuilt(t *testing.T) {
	server := newServer(t, newSession(t, capability.Native(capability.LockFree)), protocol.ModePlan)

	if server.Mode() != protocol.ModePlan {
		t.Errorf("the server reports mode %q", server.Mode())
	}
}
