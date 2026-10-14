package migr

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

// warned is a host that keeps every operation document and every warning a phase sends.
type warned struct {
	collecting
	// warnings are the messages logged at warn, in the order they were sent.
	warnings []string
}

// Notify keeps a warning, which is how an unsafe step marks itself.
func (w *warned) Notify(method string, params any) error {
	logged, ok := params.(wire.LogParams)
	if method == wire.HostLog && ok && logged.Level == wire.Level(LevelWarn) {
		w.warnings = append(w.warnings, logged.Message)
	}

	return nil
}

// ran is what one phase emitted in one direction, and what it warned.
type ran struct {
	documents []string
	warnings  []string
}

// runPhase runs one direction of a phase against a recording host, failing on any error.
func runPhase(t *testing.T, m Migration, dir Direction) ran {
	t.Helper()

	host := &warned{}
	s := newSchema(host, version, dir, ModePlan)

	var err error

	if dir == Up {
		err = m.Up(context.Background(), s)
	} else {
		down, ok := m.(Downer)
		if !ok {
			t.Fatalf("%T authors no down", m)
		}

		err = down.Down(context.Background(), s)
	}

	if err != nil {
		t.Fatalf("%T %s: %v", m, dir, err)
	}

	return ran{documents: host.documents, warnings: host.warnings}
}

// email is the replacement the tests share: a NOT NULL string widened and lowercased, keeping its name.
func email() Replacement {
	return ReplaceColumn("users", "email", String(255)).With(String(320)).Through(TransformLower)
}

func TestTheKeepNameFormEmitsTheFourPhasesInBothDirections(t *testing.T) {
	r := email().Through(TransformCopy)

	for _, tc := range []struct {
		phase Migration
		dir   Direction
		want  []string
	}{
		{r.Expand(), Up, []string{
			`{"op":"add_column","table":"users","column":{"name":"email_next","type":"string(320)","nullable":true}}`,
		}},
		{r.Expand(), Down, []string{
			`{"op":"drop_column","table":"users","column":"email_next","from":{"name":"email_next","type":"string(320)","nullable":true}}`,
		}},
		{r.Backfill(), Up, []string{
			`{"op":"data_transform","table":"users","column":"email_next","type":"string(320)","from":"email","function":"copy"}`,
		}},
		{r.Backfill(), Down, nil},
		{r.Swap(), Up, []string{
			`{"op":"alter_column","table":"users","column":"email","nullable":true,"from":{"nullable":false}}`,
			`{"op":"alter_column","table":"users","column":"email_next","nullable":false,"from":{"nullable":true}}`,
			`{"op":"rename_column","table":"users","from":"email","to":"email_old"}`,
			`{"op":"rename_column","table":"users","from":"email_next","to":"email"}`,
		}},
		{r.Swap(), Down, []string{
			`{"op":"rename_column","table":"users","from":"email","to":"email_next"}`,
			`{"op":"rename_column","table":"users","from":"email_old","to":"email"}`,
			`{"op":"alter_column","table":"users","column":"email_next","nullable":true,"from":{"nullable":false}}`,
			`{"op":"alter_column","table":"users","column":"email","nullable":false,"from":{"nullable":true}}`,
		}},
		{r.Contract(), Up, []string{
			`{"op":"drop_column","table":"users","column":"email_old","from":{"name":"email_old","type":"string(255)","nullable":true}}`,
		}},
		{r.Contract(), Down, []string{
			`{"op":"add_column","table":"users","column":{"name":"email_old","type":"string(255)","nullable":true}}`,
			`{"op":"data_transform","table":"users","column":"email_old","type":"string(255)","from":"email","function":"copy"}`,
		}},
	} {
		got := runPhase(t, tc.phase, tc.dir).documents
		if !slices.Equal(got, tc.want) {
			t.Errorf("%T %s emitted\n%s\nwant\n%s", tc.phase, tc.dir, strings.Join(got, "\n"),
				strings.Join(tc.want, "\n"))
		}
	}
}

func TestTheRenameFormSwapsNoNamesAndContractDropsTheOriginal(t *testing.T) {
	r := ReplaceColumn("users", "email", String(255)).RenameTo("email_address")

	if got := runPhase(t, r.Expand(), Up).documents; len(got) != 1 ||
		!strings.Contains(got[0], `"name":"email_address"`) {
		t.Errorf("expand emitted %v, want the column added under its new name", got)
	}

	swap := runPhase(t, r.Swap(), Up).documents
	if len(swap) != 2 || strings.Contains(strings.Join(swap, ""), "rename_column") {
		t.Errorf("swap emitted %v, want two nullability changes and no rename", swap)
	}

	contract := runPhase(t, r.Contract(), Up).documents
	if len(contract) != 1 || !strings.Contains(contract[0], `"column":"email","from"`) {
		t.Errorf("contract emitted %v, want the original column dropped", contract)
	}

	down := runPhase(t, r.Contract(), Down).documents
	if len(down) != 2 || !strings.Contains(down[1], `"column":"email","type":"string(255)",`+
		`"from":"email_address"`) {
		t.Errorf("contract's down emitted %v, want the original refilled from the new name", down)
	}
}

func TestAContractAfterALossyBackfillIsIrreversibleAndSaysWhy(t *testing.T) {
	undo, reason := Undone(email().Contract())
	if undo != UndoDeclared || !strings.Contains(reason, "through lower") {
		t.Fatalf("a contract after a lowercasing backfill is %s with %q, want declared "+
			"irreversible naming the function", undo, reason)
	}

	if undo, _ := Undone(email().Through(TransformCopy).Contract()); undo != UndoAuthored {
		t.Errorf("a contract after a copy is %s, want an authored down", undo)
	}

	t.Log("A copy backfill left the old values whole in the new column, so contract's down can copy")
	t.Log("them back. A lowercased one did not, and a down that put lowercased values into the old")
	t.Log("column would report a rollback that restored something it did not.")
}

func TestOnlyTheUnsafeStepsWarnAndEachNamesWhatItBreaks(t *testing.T) {
	r := email().Through(TransformCopy)

	for _, tc := range []struct {
		phase Migration
		dir   Direction
		want  string
	}{
		{r.Expand(), Up, ""},
		{r.Expand(), Down, `unsafe step: expand down: drops "users"."email_next"`},
		{r.Backfill(), Up, ""},
		{r.Backfill(), Down, ""},
		{r.Swap(), Up, `unsafe step: swap: "users"."email" now names the new column`},
		{r.Swap(), Down, `unsafe step: swap down: "users"."email" is the column an app reads again`},
		{r.Contract(), Up, `unsafe step: contract: drops "users"."email_old" and the values in it`},
		{r.Contract(), Down, ""},
	} {
		got := runPhase(t, tc.phase, tc.dir).warnings

		switch {
		case tc.want == "" && len(got) > 0:
			t.Errorf("%T %s warned %q, and it is safe", tc.phase, tc.dir, got)
		case tc.want != "" && (len(got) != 1 || !strings.HasPrefix(got[0], tc.want)):
			t.Errorf("%T %s warned %q, want one warning starting %q", tc.phase, tc.dir, got, tc.want)
		}
	}

	t.Log("The warning is sent in plan mode too, so migr plan shows it before migr up does -- the")
	t.Log("step is marked where an operator reads the plan, not only where it runs.")
}

func TestEveryPhaseRegistersAsTheMigrationShapeItClaims(t *testing.T) {
	r := email()

	for _, phase := range []Migration{r.Expand(), r.Backfill(), r.Swap(), r.Contract(),
		r.Through(TransformCopy).Contract()} {
		if _, err := accept(phase, "20260727T153000_replace_email.go"); err != nil {
			t.Errorf("%T does not register: %v", phase, err)
		}
	}
}

func TestAReplacementThatCannotBeCarriedOutIsRefusedByEveryPhase(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    Replacement
		want string
	}{
		{"no table", ReplaceColumn("", "email", String(255)).With(Text()), "table"},
		{"no column", ReplaceColumn("users", "", String(255)).With(Text()), "column"},
		{"nothing changes", ReplaceColumn("users", "email", String(255)), "nothing to replace"},
		{"same type named", ReplaceColumn("users", "email", String(255)).With(String(255)),
			"nothing to replace"},
		{"via is the column", email().Via("email"), "is the column being replaced"},
		{"retired is the column", email().Retire("email"), "is the column being replaced"},
		{"via is retired", email().Via("spare").Retire("spare"), "cannot name both"},
		{"rename to itself", ReplaceColumn("users", "email", String(255)).RenameTo("email"),
			"names the column it renames"},
		{"rename with via", email().RenameTo("address").Via("spare"), "Via and Retire name nothing"},
		{"bad function", email().Through("reverse"), "unknown function"},
		{"bad type", email().With(String(0)), "the column it becomes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, phase := range []Migration{tc.r.Expand(), tc.r.Backfill(), tc.r.Swap(),
				tc.r.Contract()} {
				err := phase.Up(context.Background(), newSchema(&warned{}, version, Up, ModePlan))
				if !errors.Is(err, ErrInvalidReplacement) || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%T up returned %v, want ErrInvalidReplacement naming %q", phase, err,
						tc.want)
				}

				if v, ok := phase.(Validator); !ok || v.Validate() == nil {
					t.Errorf("%T does not refuse it at migr validate", phase)
				}
			}
		})
	}
}

func TestAReplacementIsAValueSoDerivingOneLeavesTheOriginalAlone(t *testing.T) {
	base := ReplaceColumn("users", "email", String(255)).With(String(320))
	_ = base.RenameTo("email_address").Through(TransformLower).Null()

	got := runPhase(t, base.Expand(), Up).documents
	if len(got) != 1 || !strings.Contains(got[0], `"name":"email_next"`) {
		t.Errorf("deriving a second replacement changed the first, which now emits %v", got)
	}
}

func TestANullableColumnIsNotRelaxedAndANullableResultIsNotTightened(t *testing.T) {
	r := ReplaceColumn("users", "email", String(255)).WasNull().With(String(320)).Null()

	got := runPhase(t, r.Swap(), Up).documents
	if len(got) != 2 || strings.Contains(strings.Join(got, ""), "alter_column") {
		t.Errorf("swap emitted %v, want the two renames and no nullability change", got)
	}
}
