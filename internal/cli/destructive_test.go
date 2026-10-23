package cli

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// permitting rewrites a staged project's allowance, since the scaffold it derives from permits raw.
func permitting(t *testing.T, dir string, kinds ...ir.Kind) {
	t.Helper()

	named := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		named = append(named, string(kind))
	}

	write(t, filepath.Join(dir, config.DefaultFilename),
		strings.Replace(projectConfig, allowingRaw,
			"allow_destructive: ["+strings.Join(named, ", ")+"]", 1))
}

// allowingRaw is what the scaffold writes, and what a test rewrites to permit something else.
const allowingRaw = "allow_destructive: [" + string(ir.KindRaw) + "]"

// refusing stages a project that permits nothing, which is every migr.yaml written by hand.
func refusing(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	permitting(t, dir)

	return dir, f, drivers
}

func TestARunIsRefusedOverAMigrationNothingPermits(t *testing.T) {
	dir, _, drivers := refusing(t)

	_, _, err := runWith(t, dir, noon, drivers, "up")
	if got := ExitCode(err); got != ExitValidation {
		t.Fatalf("migr up exited %d, want %d: %v", got, ExitValidation, err)
	}

	message := said(err)

	for _, want := range []string{
		fileAt(0, "add_users"),
		"migr cannot read what this migration does",
		"operation 1",
		string(ir.KindRaw),
		"CREATE TABLE users (id bigint);",
		"--allow-destructive",
		string(ir.KindRaw) + " to validation.allow_destructive",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, message)
			t.Log("The done-when asks for the migration, the operation, and the affected object --")
			t.Log("and a refusal an operator cannot act on is worse than one that never fired.")
			t.Log("The headline is raw's own: a .sql migration compiles to nothing else, so saying")
			t.Log("that a CREATE TABLE destroys data would assert what migr cannot read.")
		}
	}
}

func TestTheFlagAndTheConfigEachPermitTheSameRun(t *testing.T) {
	dir, _, drivers := refusing(t)

	if _, _, err := runWith(t, dir, noon, drivers, "up", "--allow-destructive"); err != nil {
		t.Fatalf("migr up --allow-destructive: %v", err)
	}

	again, _, driversAgain := project(t, twoTables()...)

	if _, _, err := runWith(t, again, noon, driversAgain, "up"); err != nil {
		t.Log("The scaffold permits raw, so every project migr init writes runs its own first")
		t.Log("migration. A flag is for one run; the config is the project's standing decision.")
		t.Fatalf("migr up over a project that permits raw: %v", err)
	}
}

func TestAnAllowanceThatNamesAnotherKindPermitsNothingHere(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)
	permitting(t, dir, ir.KindDropTable)

	_, _, err := runWith(t, dir, noon, drivers, "up")
	if got := ExitCode(err); got != ExitValidation {
		t.Fatalf("migr up exited %d, want %d: %v", got, ExitValidation, err)
	}

	if !strings.Contains(err.Error(), "This project permits drop_table") {
		t.Errorf("the refusal does not say what this project does permit:\n%v", err)
		t.Log("A bool could not express this: permitting drop_table is not permitting raw, and an")
		t.Log("allowance that covers some of a migration reads as none unless the refusal says so.")
	}
}

func TestARollbackIsNotGatedByTheAllowance(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("migr up: %v", err)
	}

	permitting(t, dir)

	if _, _, err := runWith(t, dir, noon, drivers, "down"); err != nil {
		t.Log("The inverse of create_table is drop_table, so gating a rollback would make the")
		t.Log("allowance mandatory for migr down outright. A rollback is what the operator asked")
		t.Log("for, and migr down registers no --allow-destructive to pass either.")
		t.Fatalf("migr down over a project that permits nothing: %v", err)
	}
}

func TestAReportFindsTheProblemWithoutFailing(t *testing.T) {
	dir, _, drivers := refusing(t)

	out, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("migr plan over a project that permits nothing: %v", err)
	}

	for _, want := range []string{"? migr cannot read", "marked ? above", "--allow-destructive"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not mark what migr up would refuse (%q):\n%s", want, out)
		}
	}

	t.Log("A report exits 0 having found a problem, as migr status does over a drifted database")
	t.Log("while migr up exits 4. This is the same rule: the plan says what up will refuse, and")
	t.Log("up is the gate.")
}

func TestADryRunSaysWhatTheRunItDescribesWouldRefuse(t *testing.T) {
	dir, _, drivers := refusing(t)

	out, _, err := runWith(t, dir, noon, drivers, "up", "--dry-run")
	if err != nil {
		t.Fatalf("migr up --dry-run over a project that permits nothing: %v", err)
	}

	for _, want := range []string{"3 operations that nothing has permitted", "--allow-destructive"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not say what migr up would refuse (%q):\n%s", want, out)
			t.Log("A dry run counts rather than listing, so without this line it would report two")
			t.Log("migrations it would apply and then migr up would refuse the first of them.")
		}
	}
}

func TestAPermittedOperationIsMarkedWithoutBeingMarkedRefused(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	out, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("migr plan: %v", err)
	}

	if strings.Contains(out, "? migr cannot read") {
		t.Errorf("a permitted operation is marked as one nothing permits:\n%s", out)
	}

	if strings.Contains(out, "marked ? above") {
		t.Errorf("the plan says migr up would refuse a permitted run:\n%s", out)
	}
}

func TestTheRefusalIsOneJSONObjectNamingWhatItWouldDestroy(t *testing.T) {
	dir, _, drivers := refusing(t)

	_, stderr, err := runWith(t, dir, noon, drivers, "--json", "up")
	if got := ExitCode(err); got != ExitValidation {
		t.Fatalf("migr --json up exited %d, want %d: %v", got, ExitValidation, err)
	}

	var document failure
	if decodeErr := json.Unmarshal([]byte(stderr), &document); decodeErr != nil {
		t.Fatalf("the failure is not one JSON object: %v\n%s", decodeErr, stderr)
	}

	if document.Destructive == nil {
		t.Fatalf("the document carries no destructive sub-document:\n%s", stderr)
	}

	if got := document.Code; got != ExitValidation {
		t.Errorf("the document reports code %d, want %d", got, ExitValidation)
	}

	if got := document.Migration; got != fileAt(0, "add_users") {
		t.Errorf("the document names migration %q", got)
	}

	checkDestroyedDocument(t, document.Destructive)
}

// checkDestroyedDocument holds the sub-document to the fields a consumer would branch on.
func checkDestroyedDocument(t *testing.T, found *destroys) {
	t.Helper()

	if len(found.Allowed) != 0 {
		t.Errorf("the document reports %v permitted, want none", found.Allowed)
	}

	if !slices.Equal(found.Needed, []ir.Kind{ir.KindRaw}) {
		t.Errorf("the document reports %v needed, want [raw]", found.Needed)
	}

	if len(found.Operations) != 2 {
		t.Fatalf("the document reports %d operations, want the two the migration emitted",
			len(found.Operations))
	}

	first := found.Operations[0]
	if first.Operation != 1 || first.Kind != ir.KindRaw || first.Subject == "" ||
		first.Detail == "" {
		t.Errorf("the first operation is %+v, want the first statement named in full", first)
	}
}

func TestValidateReportsEveryDestructiveOperationAndHowToPermitIt(t *testing.T) {
	dir, _, drivers := refusing(t)

	out, _, err := runWith(t, dir, noon, drivers, "validate")
	if got := ExitCode(err); got != ExitValidation {
		t.Fatalf("migr validate exited %d, want %d: %v", got, ExitValidation, err)
	}

	for _, want := range []string{
		"destructive",
		"up operation 1 (raw)",
		"allow_destructive: [raw]",
		"reaches the database as a raw operation",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not carry %q:\n%s", want, out)
		}
	}
}

func TestValidatePassesTheSameProjectOnceItPermitsWhatItDoes(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "validate"); err != nil {
		t.Fatalf("migr validate over a project that permits raw: %v", err)
	}
}

func TestOnlyTheCommandsThatReadThePermissionRegisterTheFlag(t *testing.T) {
	honors := []string{"up", "validate"}

	var registered []string

	visitFlags(func(cmd *cobra.Command, f *pflag.Flag) {
		if f.Name == "allow-destructive" {
			registered = append(registered, cmd.Name())
		}
	})

	slices.Sort(registered)

	if !slices.Equal(registered, honors) {
		t.Errorf("--allow-destructive is registered on %v, want %v", registered, honors)
		t.Log("A rollback is not gated and a report refuses nothing, so on every other command")
		t.Log("this flag would be accepted and ignored -- which is worse than one that is absent.")
	}
}
