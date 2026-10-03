package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/validate"
)

// refusedStatement is the one statement the staged server compiles and declares it cannot perform.
const refusedStatement = "CREATE TABLE teams (id bigint);"

// declining stages a project whose server refuses one migration's operation, having compiled it.
func declining(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	f.serverRefuses = refusedStatement

	return dir, f, drivers
}

func TestValidateChecksNothingAgainstAServerUntilItIsAskedTo(t *testing.T) {
	dir, f, drivers := declining(t)

	out, _, err := runWith(t, dir, noon, drivers, "validate")
	if err != nil {
		t.Fatalf("migr validate: %v", err)
	}

	if f.asked("open") {
		t.Error("migr validate opened a database with no --connect")
	}

	for _, want := range []string{"no problems found", noDatabase, "--connect"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not say %q:\n%s", want, out)
			t.Log("Reporting no problems without saying that the one check needing a server was")
			t.Log("never made is the pass ADR 0028 B2 rejected, one milestone later.")
		}
	}
}

func TestValidateConnectReportsWhatThisServerWillNotPerform(t *testing.T) {
	dir, f, drivers := declining(t)

	out, _, err := runWith(t, dir, noon, drivers, "validate", "--connect")
	if got := ExitCode(err); got != ExitValidation {
		t.Fatalf("migr validate --connect exited %d, want %d: %v", got, ExitValidation, err)
	}

	if !f.asked("open") {
		t.Fatalf("migr validate --connect asked the database for %v", f.calls)
	}

	for _, want := range []string{
		fileAt(1, "add_teams"),
		string(validate.CheckCapability),
		"operation 1",
		string(ir.KindRaw),
		serverRefusalReason,
		"every operation was checked against server",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not name %q:\n%s", want, out)
		}
	}
}

func TestValidateConnectFailsRatherThanSkippingWhenNothingAnswers(t *testing.T) {
	dir, f, drivers := declining(t)
	f.openErr = errors.New("connection refused")

	_, _, err := runWith(t, dir, noon, drivers, "validate", "--connect")
	if got := ExitCode(err); got != ExitRuntime {
		t.Fatalf("migr validate --connect exited %d, want %d: %v", got, ExitRuntime, err)
	}

	if !strings.Contains(err.Error(), `database "default"`) {
		t.Errorf("the failure does not name the database it could not reach: %v", err)
	}

	if _, _, offline := runWith(t, dir, noon, drivers, "validate"); offline != nil {
		t.Errorf("migr validate without the flag failed over the same unreachable database: %v",
			offline)
		t.Log("The flag is what asks for a database, so the no-secret run ADR 0028 B1 built this")
		t.Log("command for is unchanged by a DSN that points nowhere.")
	}
}

func TestValidateConnectTakesNoMigrationLock(t *testing.T) {
	dir, f, drivers := declining(t)
	f.lockErr = driver.ErrLockTimeout
	f.serverRefuses = ""

	if _, _, err := runWith(t, dir, noon, drivers, "validate", "--connect"); err != nil {
		t.Fatalf("migr validate --connect failed against a database whose lock is held: %v", err)
	}

	if f.asked("lock") {
		t.Error("migr validate --connect took the migration lock, which no report may")
		t.Log("It answers what these migrations are, not what will run, and it is the command")
		t.Log("an operator reaches for during a deploy (ADR 0027).")
	}
}

func TestARunIsRefusedBeforeSendingAnythingThisServerCannotPerform(t *testing.T) {
	dir, f, drivers := declining(t)

	_, _, err := runWith(t, dir, noon, drivers, "up")
	if got := ExitCode(err); got != ExitRuntime {
		t.Fatalf("migr up exited %d, want %d: %v", got, ExitRuntime, err)
	}

	if !errors.Is(err, engine.ErrUnsupported) {
		t.Fatalf("migr up returned %v, want the server's refusal", err)
	}

	message := err.Error()

	for _, want := range []string{
		fileAt(1, "add_teams"),
		`driver "fake" against server 0.0-fake`,
		"operation 1",
		string(ir.KindRaw),
		serverRefusalReason,
		"nothing in it ran",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, message)
			t.Log("Invariant #9: every user-facing error names the migration and the offending")
			t.Log("operation, and the done-when asks for the driver's stated reason as well.")
		}
	}

	if ran := f.statements; len(ran) != 2 || strings.Contains(strings.Join(ran, "\n"), "teams") {
		t.Errorf("the run executed %v, want the two statements of the migration before it", ran)
		t.Log("The refusal is resolved before anything compiles, so the migration it reached sent")
		t.Log("nothing at all -- and the ones before it are what the run's own summary reports.")
	}
}

func TestTheRefusalCarriesItsOwnDocumentUnderJSON(t *testing.T) {
	dir, _, drivers := declining(t)

	_, stderr, err := runWith(t, dir, noon, drivers, "--json", "up")
	if err == nil {
		t.Fatal("migr --json up succeeded over an operation this server refuses")
	}

	got := decodeFailure(t, strings.TrimSpace(stderr))

	if got.Code != ExitRuntime || got.Capability == nil {
		t.Fatalf("the document is %+v, want a capability sub-document at code %d", got, ExitRuntime)
	}

	if got.Operation != ir.KindRaw || got.Migration != fileAt(1, "add_teams") {
		t.Errorf("the document names %q in %q", got.Operation, got.Migration)
	}

	if got.Capability.Driver != fakeName || got.Capability.Server != "0.0-fake" ||
		got.Capability.Direction != ir.Up || got.Capability.Version != versionAt(1) {
		t.Errorf("the sub-document is %+v, want the migration, the direction and the server",
			got.Capability)
	}

	if len(got.Capability.Operations) != 1 {
		t.Fatalf("the sub-document holds %d operations, want the one refused",
			len(got.Capability.Operations))
	}

	refused := got.Capability.Operations[0]
	if refused.Operation != 1 || refused.Kind != ir.KindRaw || refused.Reason != serverRefusalReason ||
		refused.Subject != refusedStatement {
		t.Errorf("the refused operation is %+v, want the index, kind, subject and reason", refused)
	}

	if strings.Contains(got.Error, "\n") {
		t.Errorf("the one-line summary spans several lines: %q", got.Error)
	}
}

func TestARollbackIsRefusedForTheSameReason(t *testing.T) {
	dir, f, drivers := declining(t)
	f.serverRefuses = ""

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("migr up: %v", err)
	}

	f.serverRefuses = "DROP TABLE teams;"

	_, _, err := runWith(t, dir, noon, drivers, "down")
	if !errors.Is(err, engine.ErrUnsupported) {
		t.Fatalf("migr down returned %v, want the server's refusal", err)
	}

	if !strings.Contains(err.Error(), "down") {
		t.Errorf("the refusal does not say which half it is about:\n%v", err)
	}
}

func TestAPreviewMarksWhatARunRefusesAndSucceeds(t *testing.T) {
	dir, _, drivers := declining(t)

	out, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("migr plan: %v", err)
	}

	for _, want := range []string{
		"unsupported",
		"× " + serverRefusalReason,
		refusedStatement,
		"marked × above: 1 operation this server declares it cannot perform",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not print %q:\n%s", want, out)
			t.Log("A report finds a problem without failing (CLAUDE.md §5), and this is the")
			t.Log("command that tells an operator what migr up will refuse -- with the reason,")
			t.Log("which before M10-T2 it printed nowhere at all.")
		}
	}
}

func TestADryRunCountsWhatThisServerCannotPerform(t *testing.T) {
	dir, _, drivers := declining(t)

	out, _, err := runWith(t, dir, noon, drivers, "up", "--dry-run")
	if err != nil {
		t.Fatalf("migr up --dry-run: %v", err)
	}

	if !strings.Contains(out, "1 operation this server declares it cannot perform") {
		t.Errorf("the dry run says nothing about the refusal:\n%s", out)
		t.Log("A dry run that reported two migrations it would apply and then watched migr up")
		t.Log("refuse the second would be describing a run that cannot happen.")
	}
}

func TestThePlanCarriesTheReasonUnderJSON(t *testing.T) {
	dir, _, drivers := declining(t)

	out, _, err := runWith(t, dir, noon, drivers, "--json", "plan")
	if err != nil {
		t.Fatalf("migr --json plan: %v", err)
	}

	var got compiledPlan
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding the plan: %v\n%s", err, out)
	}

	if len(got.Migrations) != 2 {
		t.Fatalf("the plan holds %d migrations, want both", len(got.Migrations))
	}

	refused := got.Migrations[1].Operations[0]
	if refused.Reason != serverRefusalReason {
		t.Errorf("the operation carries reason %q", refused.Reason)
	}

	if performed := got.Migrations[0].Operations[0]; performed.Reason != "" {
		t.Errorf("an operation this server performs carries a reason: %q", performed.Reason)
	}
}

func TestTheConnectFlagIsRegisteredOnlyWhereItIsRead(t *testing.T) {
	var carried []string

	for _, cmd := range newRootCmd().Commands() {
		if cmd.Flags().Lookup("connect") != nil {
			carried = append(carried, cmd.Name())
		}
	}

	if len(carried) != 1 || carried[0] != "validate" {
		t.Errorf("--connect is registered on %v, want validate alone", carried)
		t.Log("Every other command either opens a database already or is a report that must not,")
		t.Log("so anywhere else this is a flag accepted and ignored (ADR 0063's rule).")
	}

	if found := newRootCmd().PersistentFlags().Lookup("connect"); found != nil {
		t.Error("--connect is a persistent flag, so every command accepts it")
	}
}

func TestARefusedMigrationIsNeverCompiledAgainstTheDatabase(t *testing.T) {
	dir, f, drivers := project(t, stagedMigration{
		name: "add_users",
		up:   "CREATE TABLE users (id bigint);",
		down: "DROP TABLE users;",
	})

	f.expands = true
	f.serverRefuses = "CREATE TABLE users (id bigint);"

	_, _, err := runWith(t, dir, noon, drivers, "up")
	if !errors.Is(err, engine.ErrUnsupported) {
		t.Fatalf("migr up returned %v, want the server's refusal", err)
	}

	if f.asked("expand") {
		t.Errorf("the refused migration was compiled against the database: %v", f.calls)
		t.Log("Compiling is pure for most operations, so the gate's place before prepare looks")
		t.Log("free -- until an emulated one, which is compiled against the live catalog (ADR")
		t.Log("0053). A migration this server will not perform must not read a schema first.")
	}
}
