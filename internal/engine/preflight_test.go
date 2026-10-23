package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

// serverReason is a refusal only a connected server could have reached, which Compile cannot.
const serverReason = "this release accepts the statement and discards it, so refusing is the only " +
	"answer that is not a lie"

// declaring replaces a session's declaration with one refusing the kinds named, natively otherwise.
func declaring(t *testing.T, conn *recorder, refused ...ir.Kind) {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))

	for _, kind := range ir.Kinds() {
		verdict := capability.Native(capability.LockFree)
		if slices.Contains(refused, kind) {
			verdict = capability.Unsupported(serverReason)
		}

		rules[kind] = capability.Fixed(verdict)
	}

	set, err := capability.NewSet(capability.Facts{
		Driver: "recorder", Server: "1.0", TransactionalDDL: true,
	}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	conn.caps = set
}

func TestEveryOperationThisServerRefusesIsFoundAndNamed(t *testing.T) {
	conn := newRecorder(t, true)
	declaring(t, conn, ir.KindAddIndex, ir.KindDropView)

	found := Unsupported(ir.Operations{
		addIndex("by_email"),
		created(t, "users"),
		ir.DropView{View: "listing"},
	}, conn.caps)

	if len(found) != 2 {
		t.Fatalf("%d operations were found, want the two the declaration refuses: %+v", len(found), found)
	}

	for i, want := range []Refusal{
		{Operation: 1, Kind: ir.KindAddIndex, Subject: "by_email", Reason: serverReason},
		{Operation: 3, Kind: ir.KindDropView, Subject: "listing", Reason: serverReason},
	} {
		if found[i] != want {
			t.Errorf("finding %d is %+v, want %+v", i+1, found[i], want)
			t.Log("The index, the kind and the subject are what --json carries and what the plan")
			t.Log("marks, and the reason is the driver's own: the done-when asks for that verbatim.")
		}
	}
}

func TestAnEmulatedOperationIsNotARefusal(t *testing.T) {
	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Emulated("table rebuild", capability.TableRewrite))
	}

	set, err := capability.NewSet(capability.Facts{Driver: "recorder", Server: "1.0"}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	if found := Unsupported(ir.Operations{addIndex("by_email")}, set); found != nil {
		t.Errorf("an emulation was reported as a refusal: %+v", found)
		t.Log("Allowed() covers native and emulated both: the driver performs the operation, and")
		t.Log("how it performs it is the other axis.")
	}
}

func TestAMigrationThisServerCannotPerformIsRefusedBeforeAnythingRuns(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	declaring(t, conn, ir.KindAddIndex)

	opts, _ := staged(t, "a")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrUnsupported", err)
	}

	if slices.Contains(conn.calls, "begin") || slices.Contains(conn.calls, "exec a0") {
		t.Fatalf("the session was asked for %v before the refusal", conn.calls)
	}

	var refusal *UnsupportedError
	if !errors.As(err, &refusal) {
		t.Fatalf("Apply returned %v, which is not an *UnsupportedError", err)
	}

	for _, want := range []string{
		migrationAt(t, 0).Filename(),
		`driver "recorder" against server 1.0`,
		"operation 1",
		string(ir.KindAddIndex),
		`"a0"`,
		serverReason,
		"nothing in it ran",
	} {
		if !strings.Contains(refusal.Error(), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, refusal.Error())
		}
	}

	if refusal.Direction != ir.Up {
		t.Errorf("the refusal reports direction %q, want the half that was asked for", refusal.Direction)
	}
}

func TestTheRefusalNamesTheDriverAloneWhenNoServerVersionWasDetected(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Unsupported(serverReason))
	}

	set, err := capability.NewSet(capability.Facts{Driver: "recorder"}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	conn.caps = set

	opts, _ := staged(t, "a")

	_, err = Apply(t.Context(), conn, opts)

	var refusal *UnsupportedError
	if !errors.As(err, &refusal) {
		t.Fatalf("Apply returned %v, which is not an *UnsupportedError", err)
	}

	if strings.Contains(refusal.Error(), "against server") {
		t.Errorf("the refusal claims a server it never detected:\n%s", refusal.Error())
		t.Log("driver.Offline builds exactly this set, and a message naming an empty version")
		t.Log("reads as a server called nothing.")
	}
}

func TestARollbackIsRefusedForTheSameReasonAsARun(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)
	declaring(t, conn, ir.KindDropIndex)

	opts, _ := staged(t, "a")

	_, err := Rollback(t.Context(), conn, opts)

	var refusal *UnsupportedError
	if !errors.As(err, &refusal) {
		t.Fatalf("Rollback returned %v, which is not an *UnsupportedError", err)
	}

	if refusal.Direction != ir.Down {
		t.Errorf("the refusal reports direction %q, want down", refusal.Direction)
	}

	if slices.Contains(conn.calls, "begin") {
		t.Fatalf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("Unlike the destructive gate this is direction-neutral: ir.CreateTable inverts to")
	t.Log("DropTable, which is why permission is asked of the up half alone -- but an")
	t.Log("operation the server will not perform is just as unrunnable backwards. A rollback")
	t.Log("inverts its whole queue before running any of it, so migr down is preflighted whole.")
}

func TestAPreviewMarksWhatARunRefuses(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	declaring(t, conn, ir.KindAddIndex)

	opts, _ := staged(t, "a")

	previewed, err := Preview(t.Context(), conn, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview returned %v, and a report finds a problem without failing", err)
	}

	if len(previewed.Steps) != 1 {
		t.Fatalf("the preview compiled %d migrations, want the one it was given", len(previewed.Steps))
	}

	t.Log("migr plan is the command that tells an operator what migr up will refuse, so it must")
	t.Log("not refuse to -- CLAUDE.md §5 with no exception. The mode is what separates the two")
	t.Log(", and internal/cli is where the verdict becomes a × beside the statement.")
}

func TestAResumeIsNotGatedByTheDeclaration(t *testing.T) {
	conn, opts, _ := resumable(t, "b1", "abc")
	declaring(t, conn, ir.KindAddIndex)

	if _, err := resume(t, conn, opts); err != nil {
		t.Fatalf("Resolve --resume returned %v with the declaration refusing the operation", err)
	}

	t.Log("The schema is already half-changed, and the statements a resume runs are the ones that")
	t.Log("run reached rather than ones a verdict was asked about. Refusing here would leave a")
	t.Log("database between states with nothing but the asserted repairs to settle it.")
}
