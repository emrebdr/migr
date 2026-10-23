package migr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

func TestTheHandshakePicksOneOfTheVersionsTheEngineOffered(t *testing.T) {
	staged(t, nil)

	e := dial(t)

	var answered wire.InitializeResult

	decode(t, e.greet(), &answered)

	if answered.ProtocolVersion != wire.Version {
		t.Errorf("the runtime picked version %d, want %d", answered.ProtocolVersion, wire.Version)
	}

	if answered.Runtime.Name != Language || answered.Runtime.Version == "" {
		t.Errorf("the runtime named itself %+v, want the go toolchain", answered.Runtime)
	}

	if answered.SDK.Name != Name || answered.SDK.Version == "" {
		t.Errorf("the SDK named itself %+v", answered.SDK)
	}

	if e.finish() != exitOK {
		t.Error("a session that was shut down exited non-zero")
	}
}

func TestAProtocolVersionThisSDKDoesNotSpeakIsRefusedAsItself(t *testing.T) {
	staged(t, nil)

	e := dial(t)

	id := e.send(wire.Initialize, wire.InitializeParams{
		ProtocolVersions: []int{7, 9}, IRVersion: wire.IRVersion,
	})

	t.Log("§3.2: the engine offers a set and the runtime picks one. A runtime that answered with")
	t.Log("a version outside the set would be a protocol error, so it says so instead.")

	if slug := slugOf(t, e.answer(id, nil)); slug != "unsupported_protocol" {
		t.Errorf("the refusal is %q, want unsupported_protocol", slug)
	}
}

func TestAnyIRVersionAnEngineStatesIsAcceptedAndOnlyNoneIsRefused(t *testing.T) {
	staged(t, nil)

	for _, stated := range []int{1, wire.IRVersion, wire.IRVersion + 1} {
		e := dial(t)

		id := e.send(wire.Initialize, wire.InitializeParams{
			ProtocolVersions: []int{wire.Version}, IRVersion: stated,
		})

		if refused := e.answer(id, nil); refused.Error != nil {
			t.Errorf("an engine stating irVersion %d was refused: %v", stated, refused.Error)
			t.Log("Each version is a superset of the one before, so an engine that reads")
			t.Log("an older one still reads every operation this SDK emits but the newer ones, and a")
			t.Log("newer engine reads all of them. What is refused is an operation, not a session.")
		}
	}

	e := dial(t)

	id := e.send(wire.Initialize, wire.InitializeParams{
		ProtocolVersions: []int{wire.Version}, IRVersion: 0,
	})

	if slug := slugOf(t, e.answer(id, nil)); slug != "unsupported_ir" {
		t.Errorf("the refusal is %q, want unsupported_ir for an engine stating no version", slug)
	}
}

func TestARequestBeforeInitializeIsRefusedAsSessionState(t *testing.T) {
	staged(t, nil)

	e := dial(t)

	id := e.send(wire.MigrationDiscover, struct{}{})

	if slug := slugOf(t, e.answer(id, nil)); slug != "session_state" {
		t.Errorf("the refusal is %q, want session_state", slug)
	}
}

func TestASecondInitializeIsRefusedAsSessionState(t *testing.T) {
	staged(t, nil)

	e := dial(t)
	e.greet()

	id := e.send(wire.Initialize, wire.InitializeParams{
		ProtocolVersions: []int{wire.Version}, IRVersion: wire.IRVersion,
	})

	if slug := slugOf(t, e.answer(id, nil)); slug != "session_state" {
		t.Errorf("the refusal is %q, want session_state", slug)
	}
}

func TestARequestAfterShutdownIsRefusedAsSessionState(t *testing.T) {
	s := newSession(strings.NewReader(""), io.Discard)
	s.open(wire.IRVersion)

	if refused := s.admit(wire.Shutdown); refused != nil {
		t.Fatalf("shutdown was refused: %v", refused)
	}

	t.Log("This is asserted here rather than over the wire because §9 requires the process exit")
	t.Log("as soon as it has answered shutdown -- the engine waits for exactly that and kills")
	t.Log("what does not. So the window this rule covers is a race, and unreachable on purpose.")

	for _, method := range []string{wire.MigrationDiscover, wire.Initialize, wire.Shutdown} {
		refused := s.admit(method)
		if refused == nil {
			t.Errorf("%q was admitted after shutdown was answered", method)

			continue
		}

		if refused.Slug() != "session_state" {
			t.Errorf("%q was refused with %q, want session_state", method, refused.Slug())
		}
	}
}

func TestEndOfFileOnStdinEndsTheProcess(t *testing.T) {
	staged(t, nil)

	e := dial(t)
	e.greet()
	e.close()

	t.Log("§11: it means the engine is gone, and it is the one case the engine's own")
	t.Log("process-group kill cannot reach. A runtime that read on would run forever.")

	if code := <-e.done; code != exitOK {
		t.Errorf("the session exited %d after losing its engine, want %d", code, exitOK)
	}
}

func TestALineThatCannotBeFramedIsRefusedOnceAndEndsTheSession(t *testing.T) {
	staged(t, nil)

	written, code := replay(t, handshake(t)+"this is not a message\n")

	t.Log("§2.6: one best-effort error carrying id null is written first, and it is the only")
	t.Log("place null is an id. Recovering and reading on would leave whoever was waiting on")
	t.Log("the line that could not be read waiting forever.")

	if len(written) != 2 {
		t.Fatalf("the session wrote %d messages, want the handshake and one refusal", len(written))
	}

	refusal := written[1]
	if !refusal.ID.Null() {
		t.Errorf("the refusal is answered under id %s, want null", refusal.ID)
	}

	if slug := slugOf(t, refusal); slug != "parse_error" {
		t.Errorf("the refusal is %q, want parse_error", slug)
	}

	if code != exitFailed {
		t.Errorf("the session exited %d after a line it could not frame, want %d",
			code, exitFailed)
	}
}

func TestTheReaderIsSpentAndNeverReadsPastTheLineItCouldNotFrame(t *testing.T) {
	staged(t, map[string]Migration{file: plain{}})

	discover, err := wire.MarshalPayload(struct{}{})
	if err != nil {
		t.Fatalf("encoding a staged request: %v", err)
	}

	written, _ := replay(t, handshake(t)+"{\n"+
		line(t, wire.NewRequest(2, wire.MigrationDiscover, discover)))

	t.Log("The reader is spent. A session that skipped the bad line and read on")
	t.Log("would answer request 2, and whoever was waiting on the line it could not read waits")
	t.Log("forever with the migration lock held.")

	if len(written) != 2 {
		t.Fatalf("the session wrote %d messages, want the handshake and one refusal: %v",
			len(written), written)
	}

	if written[1].ID.Null() != true {
		t.Errorf("the second message is %+v, want the one null-id refusal", written[1])
	}
}

func TestDiscoverReportsEveryMigrationRegisteredInVersionOrder(t *testing.T) {
	staged(t, map[string]Migration{
		"20260727T153100_add_teams.go": plain{},
		file:                           plain{},
	})

	e := dial(t)
	e.greet()

	var found wire.DiscoverResult

	decode(t, e.answer(e.send(wire.MigrationDiscover, struct{}{}), nil), &found)

	want := []string{version, "20260727T153100"}

	got := make([]string, 0, len(found.Migrations))
	for _, m := range found.Migrations {
		got = append(got, m.Version)
	}

	if !slices.Equal(got, want) {
		t.Errorf("discover reported %v, want %v", got, want)
	}

	if found.Migrations[0].File != file {
		t.Errorf("discover reported file %q, want %q", found.Migrations[0].File, file)
	}
}

func TestDiscoverReportsDeclaredDependenciesAndNeverComputesThem(t *testing.T) {
	staged(t, map[string]Migration{
		file: dependent{after: []string{"20260727T152900"}},
	})

	e := dial(t)
	e.greet()

	var found wire.DiscoverResult

	decode(t, e.answer(e.send(wire.MigrationDiscover, struct{}{}), nil), &found)

	if !slices.Equal(found.Migrations[0].Dependencies, []string{"20260727T152900"}) {
		t.Errorf("discover reported dependencies %v", found.Migrations[0].Dependencies)
	}
}

func TestAMigrationDeclaringNoDependenciesReportsAnEmptyListRatherThanNull(t *testing.T) {
	for name, m := range map[string]Migration{"declares none": plain{}, "declares nil": silent{}} {
		t.Run(name, func(t *testing.T) {
			staged(t, map[string]Migration{file: m})

			e := dial(t)
			e.greet()

			answer := e.answer(e.send(wire.MigrationDiscover, struct{}{}), nil)

			if !strings.Contains(string(answer.Result), `"dependencies":[]`) {
				t.Errorf("discover rendered %s, want an empty list rather than null", answer.Result)
			}
		})
	}
}

func TestDiscoverReportsWhatIsWrongWithTheRegistryRatherThanHidingIt(t *testing.T) {
	staged(t, map[string]Migration{"add_users.go": plain{}})

	e := dial(t)
	e.greet()

	answer := e.answer(e.send(wire.MigrationDiscover, struct{}{}), nil)

	t.Log("Nothing is reported from init, so this is the request that answers for every")
	t.Log("registration problem. Hidden here, a migration nobody can run reads as one nobody")
	t.Log("wrote -- and the engine reports a version it discovered that this runtime did not.")

	if answer.Error == nil {
		t.Fatalf("discover reported %s for a registry that could not be built", answer.Result)
	}

	if !strings.Contains(answer.Error.Message, "add_users.go") {
		t.Errorf("the failure does not name the file: %v", answer.Error)
	}
}

func TestExecuteEmitsThroughTheHostAndReportsTheCountItEmitted(t *testing.T) {
	staged(t, map[string]Migration{file: plain{}})

	e := dial(t)
	e.greet()

	applied := 0
	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	answer := e.answer(id, func(m *wire.Message) *wire.Message {
		if m.Method == wire.HostSchemaApply {
			applied++
		}

		return recorded(t, m)
	})

	var emitted wire.ExecuteResult

	decode(t, answer, &emitted)

	if applied != 1 || emitted.Operations != 1 {
		t.Errorf("the migration made %d apply calls and reported %d operations",
			applied, emitted.Operations)
	}
}

func TestAMigrationWithNoDownAnswersNoDownAuthored(t *testing.T) {
	staged(t, map[string]Migration{file: plain{}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Down, Mode: wire.ModeApply,
	})

	t.Log("§11: not migration_failed, and not an empty operation list. Reported as either, the")
	t.Log("engine never asks for the up it would have inverted, so a derivable rollback fails.")

	if slug := slugOf(t, e.answer(id, nil)); slug != "no_down_authored" {
		t.Errorf("the refusal is %q, want no_down_authored", slug)
	}
}

func TestAMigrationDeclaringItselfOneWayAnswersWithTheAuthorsReason(t *testing.T) {
	staged(t, map[string]Migration{file: oneWay{}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Down, Mode: wire.ModeApply,
	})

	answer := e.answer(id, nil)

	if slug := slugOf(t, answer); slug != "declared_irreversible" {
		t.Fatalf("the refusal is %q, want declared_irreversible", slug)
	}

	if reason := reasonOf(t, answer); !strings.Contains(reason, "restore from a backup") {
		t.Errorf("the refusal carries reason %q, want the author's own", reason)
	}
}

func TestAnAuthoredDownIsRunRatherThanRefused(t *testing.T) {
	var ran []string

	staged(t, map[string]Migration{file: reversible{plain{ran: &ran}}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Down, Mode: wire.ModeApply,
	})

	var emitted wire.ExecuteResult

	decode(t, e.answer(id, func(m *wire.Message) *wire.Message { return recorded(t, m) }), &emitted)

	if emitted.Operations != 1 || !slices.Contains(ran, "down apply") {
		t.Errorf("the down emitted %d operations and ran %v", emitted.Operations, ran)
	}
}

func TestTheModeIsReadFromEachCallRatherThanRememberedFromTheSession(t *testing.T) {
	var ran []string

	staged(t, map[string]Migration{file: reversible{plain{ran: &ran}}})

	e := dial(t)
	e.greet()

	for _, call := range []struct {
		dir  wire.Direction
		mode wire.Mode
	}{{wire.Down, wire.ModeApply}, {wire.Up, wire.ModePlan}} {
		id := e.send(wire.MigrationExecute, wire.ExecuteParams{
			ID: version, Direction: call.dir, Mode: call.mode,
		})
		e.answer(id, func(m *wire.Message) *wire.Message { return recorded(t, m) })
	}

	t.Log("§11: one rollback issues both -- a down as a run, then the up as the question the")
	t.Log("derived inverse comes from. An SDK that cached the first previews the wrong half.")

	if !slices.Equal(ran, []string{"down apply", "up plan"}) {
		t.Errorf("the migration saw %v, want each call's own mode", ran)
	}
}

func TestAnUnknownMigrationIsRefusedAsItself(t *testing.T) {
	staged(t, map[string]Migration{file: plain{}})

	e := dial(t)
	e.greet()

	for _, method := range []string{wire.MigrationExecute, wire.MigrationValidate} {
		id := e.send(method, wire.ExecuteParams{
			ID: "20260101T000000", Direction: wire.Up, Mode: wire.ModeApply,
		})

		if slug := slugOf(t, e.answer(id, nil)); slug != "unknown_migration" {
			t.Errorf("%s refused with %q, want unknown_migration", method, slug)
		}
	}
}

func TestAMigrationThatFailsIsTheOrdinaryFailure(t *testing.T) {
	staged(t, map[string]Migration{file: plain{fail: errMigration}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	answer := e.answer(id, nil)

	if slug := slugOf(t, answer); slug != "migration_failed" {
		t.Errorf("the failure is %q, want migration_failed", slug)
	}

	if !strings.Contains(answer.Error.Message, file) {
		t.Errorf("the failure does not name the migration: %v", answer.Error)
	}
}

func TestValidateRunsTheMigrationsOwnCheckAndEmitsNothing(t *testing.T) {
	staged(t, map[string]Migration{file: checked{}})

	e := dial(t)
	e.greet()

	callbacks := 0
	id := e.send(wire.MigrationValidate, wire.ValidateParams{ID: version})

	answer := e.answer(id, func(m *wire.Message) *wire.Message {
		callbacks++

		return nil
	})

	if answer.Error != nil {
		t.Fatalf("validate failed a migration with nothing wrong with it: %v", answer.Error)
	}

	if callbacks != 0 {
		t.Errorf("validate made %d callbacks, and §4 says it emits nothing", callbacks)
	}
}

func TestAMigrationThatFailsItsOwnCheckReportsWhy(t *testing.T) {
	staged(t, map[string]Migration{file: checked{problem: errMigration}})

	e := dial(t)
	e.greet()

	answer := e.answer(e.send(wire.MigrationValidate, wire.ValidateParams{ID: version}), nil)

	if slug := slugOf(t, answer); slug != "migration_failed" {
		t.Errorf("the failure is %q, want migration_failed", slug)
	}

	if !strings.Contains(answer.Error.Message, errMigration.Error()) {
		t.Errorf("the failure does not carry the check's own words: %v", answer.Error)
	}
}

func TestAMethodThisVersionDoesNotDefineIsMethodNotFound(t *testing.T) {
	staged(t, nil)

	e := dial(t)
	e.greet()

	answer := e.answer(e.send("migration.rehearse", struct{}{}), nil)

	if answer.Error == nil || answer.Error.Code != wire.CodeMethodNotFound {
		t.Fatalf("an unknown method answered %+v, want -32601", answer.Error)
	}

	t.Log("§10.2: adding a method is a compatible change, so an older runtime has to refuse one")
	t.Log("it does not know and carry on -- ending the session would make every addition break")
	t.Log("every runtime that predates it.")

	if e.answer(e.send(wire.MigrationDiscover, struct{}{}), nil).Error != nil {
		t.Error("the session did not survive a method it does not know")
	}
}

func TestEveryErrorCarriesTheSlugAnSDKBranchesOn(t *testing.T) {
	staged(t, map[string]Migration{file: plain{}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Down, Mode: wire.ModeApply,
	})

	answer := e.answer(id, nil)

	var data map[string]json.RawMessage

	if err := json.Unmarshal(answer.Error.Data, &data); err != nil {
		t.Fatalf("the error carries no data object: %v", err)
	}

	if _, ok := data["slug"]; !ok {
		t.Errorf("the error carries data %s, and §7.1 requires a slug", answer.Error.Data)
	}
}

// recorded answers host.schema.apply with the count the host would have recorded.
func recorded(t *testing.T, m *wire.Message) *wire.Message {
	t.Helper()

	if m.Method != wire.HostSchemaApply {
		return nil
	}

	var p wire.SchemaApplyParams

	if err := wire.UnmarshalPayload(m.Params, &p); err != nil {
		t.Fatalf("reading the operations: %v", err)
	}

	id, _ := m.ID.Number()

	payload, err := wire.MarshalPayload(wire.SchemaApplyResult{Recorded: len(p.Operations)})
	if err != nil {
		t.Fatalf("encoding the result: %v", err)
	}

	return wire.NewResponse(id, payload)
}

func TestCancelReachesAMigrationMidUpAndTheRequestAnswersCanceled(t *testing.T) {
	staged(t, map[string]Migration{file: plain{block: make(chan struct{})}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	e.notify(wire.Cancel, wire.CancelParams{ID: id})

	t.Log("§8: the read loop had to be free while the migration ran, or the notification would")
	t.Log("have sat unread behind the very work it was sent to stop.")

	if slug := slugOf(t, e.answer(id, nil)); slug != "canceled" {
		t.Errorf("the abandoned request answered %q, want canceled", slug)
	}
}

func TestACanceledMigrationsOwnCallbacksStopReturningAnswers(t *testing.T) {
	staged(t, map[string]Migration{
		file: plain{block: make(chan struct{}), exec: "UPDATE users SET x = 1"},
	})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	e.notify(wire.Cancel, wire.CancelParams{ID: id})

	reached := 0

	answer := e.answer(id, func(m *wire.Message) *wire.Message {
		reached++

		return nil
	})

	t.Log("A migration backfilling in chunks stops at its next call rather than running the")
	t.Log("rest of them into a database the engine has stopped waiting on.")

	if reached != 0 {
		t.Errorf("a canceled migration reached the database %d times", reached)
	}

	if slug := slugOf(t, answer); slug != "canceled" {
		t.Errorf("the abandoned request answered %q, want canceled", slug)
	}
}

func TestTheEscapeHatchReportsWhetherItRanAtAll(t *testing.T) {
	staged(t, map[string]Migration{file: plain{exec: "UPDATE users SET x = 1"}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModePlan,
	})

	sent := ""

	e.answer(id, func(m *wire.Message) *wire.Message {
		if m.Method != wire.HostDBExec {
			return recorded(t, m)
		}

		var p wire.DBExecParams
		if err := wire.UnmarshalPayload(m.Params, &p); err != nil {
			t.Fatalf("reading the statement: %v", err)
		}

		sent = p.SQL

		return nil
	})

	t.Log("§6: the SDK sends host.db.exec in both modes and the host decides. An SDK that")
	t.Log("skipped the call under plan would hide the statement from migr plan entirely.")

	if sent != "UPDATE users SET x = 1" {
		t.Errorf("the escape hatch sent %q under plan mode", sent)
	}
}

func TestCallbacksAreNumberedFromOneInThisSidesOwnSequence(t *testing.T) {
	staged(t, map[string]Migration{file: plain{exec: "UPDATE users SET x = 1"}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	var issued []int64

	e.answer(id, func(m *wire.Message) *wire.Message {
		number, _ := m.ID.Number()
		issued = append(issued, number)

		return recorded(t, m)
	})

	t.Log("§2.5: each side numbers its own requests. The engine's request here is id 2, and")
	t.Log("this side's first callback is its own id 1 -- the two sequences are unrelated.")

	if !slices.Equal(issued, []int64{1, 2}) {
		t.Errorf("the callbacks were numbered %v, want this side's own sequence from one", issued)
	}
}

func TestACallbackAfterItsWindowClosedIsRefused(t *testing.T) {
	s := newSchema(nil, version, Up, ModeApply)
	s.seal()

	if _, err := s.Query(t.Context(), "SELECT 1"); !errors.Is(err, ErrClosed) {
		t.Errorf("a callback after the window closed returned %v, want %v", err, ErrClosed)
		t.Log("§5.1: a callback is valid only while the engine request that opened it is in")
		t.Log("flight. One made afterwards reaches a host that has already sealed the recording.")
	}
}

// backfilling is a migration whose one operation is a data operation, which irVersion 2 added.
type backfilling struct{}

// Up fills a column's NULLs, which an engine stating irVersion 1 cannot read.
func (backfilling) Up(ctx context.Context, s *Schema) error {
	return s.Table("users").Backfill("plan", String(16), Literal("free")).Apply(ctx)
}

func TestADataOperationAgainstAnEngineThatReadsOnlyV1IsRefusedBeforeItIsSent(t *testing.T) {
	staged(t, map[string]Migration{file: backfilling{}})

	e := dial(t)

	greeted := e.send(wire.Initialize, wire.InitializeParams{
		ProtocolVersions: []int{wire.Version}, IRVersion: 1,
		Client:     wire.Client{Name: "migr", Version: "0.0.9"},
		Migrations: wire.MigrationsAt{Dir: "/srv/app/migrations", Language: "go"},
		Database:   wire.DatabaseAt{Name: "default", Driver: "postgres"},
	})
	if answer := e.answer(greeted, nil); answer.Error != nil {
		t.Fatalf("an engine stating irVersion 1 was refused: %v", answer.Error)
	}

	sent := 0
	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	answer := e.answer(id, func(m *wire.Message) *wire.Message {
		if m.Method == wire.HostSchemaApply {
			sent++
		}

		return recorded(t, m)
	})

	if slug := slugOf(t, answer); slug != "unsupported_ir" {
		t.Errorf("the failure is %q, want unsupported_ir", slug)
	}

	for _, want := range []string{"data_backfill", "irVersion 2", "stated 1"} {
		if !strings.Contains(answer.Error.Message, want) {
			t.Errorf("the failure does not name %q: %s", want, answer.Error.Message)
		}
	}

	if sent != 0 {
		t.Errorf("the operation reached the host %d times before it was refused", sent)
		t.Log("An older engine decodes an operation it has never heard of as an unknown one, which")
		t.Log("points at the migration rather than at the CLI that needs upgrading (docs/ir.md §2.3).")
	}
}
