package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/diff"
	"github.com/emrebdr/migr/internal/schema"
)

// These schemas are what the prompt is driven from rather than hand-made candidates, because the
// rounds are the prompt's own: it asks diff.Candidates again after a confirmation, so a test handed a
// fixed list could not reach the second round at all.

// exact returns a column type a catalog answered precisely, which every column here carries.
func exact(t *testing.T, spelling string) schema.ColumnType {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	built, err := schema.ExactType(parsed, spelling)
	if err != nil {
		t.Fatalf("ExactType(%q): %v", spelling, err)
	}

	return built
}

// liveSchema is the schema every case here renames something in.
func liveSchema(t *testing.T) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{Tables: []schema.Table{{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: exact(t, "int64")},
			{Name: "email", Type: exact(t, "string(255)")},
			{Name: "note", Type: exact(t, "text"), Nullable: true},
		},
		PrimaryKey: &schema.PrimaryKey{Name: "users_pkey", Columns: []string{"id"}},
	}}}
}

// tableRenamed is that schema with the table under another name and nothing else changed.
func tableRenamed(t *testing.T) schema.Snapshot {
	t.Helper()

	renamed := liveSchema(t)
	renamed.Tables[0].Name = "clients"

	return renamed
}

// columnRenamed is that schema with one column under another name and nothing else changed.
func columnRenamed(t *testing.T) schema.Snapshot {
	t.Helper()

	renamed := liveSchema(t)
	renamed.Tables[0].Columns[2].Name = "memo"

	return renamed
}

// bothRenamed is that schema with the table and one of its columns both under another name.
func bothRenamed(t *testing.T) schema.Snapshot {
	t.Helper()

	renamed := tableRenamed(t)
	renamed.Tables[0].Columns[2].Name = "memo"

	return renamed
}

func TestOnlyAnExplicitYesConfirmsARename(t *testing.T) {
	cases := []struct {
		name  string
		typed string
		want  int
	}{
		{"y", "y\n", 1},
		{"yes", "yes\n", 1},
		{"Y in capitals", "Y\n", 1},
		{"n", "n\n", 0},
		{"no", "no\n", 0},
		{"an empty line", "\n", 0},
		{"whitespace", "   \n", 0},
		{"nothing at all", "", 0},
		{"a yes with no newline after it", "y", 1},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			var out strings.Builder

			confirmed, err := ConfirmRenames(strings.NewReader(one.typed), &out,
				liveSchema(t), tableRenamed(t))
			if err != nil {
				t.Fatalf("ConfirmRenames: %v", err)
			}

			if got := len(confirmed.Tables); got != one.want {
				t.Errorf("%q confirmed %d renames, want %d\n%s", one.typed, got, one.want,
					out.String())
			}
		})
	}

	t.Log("The default is no, and the asymmetry is the argument: a declined rename becomes a drop")
	t.Log("and a create, which invariant #7 gates, while a confirmed one is Destructive() == false")
	t.Log("and is gated by nothing. So the answer nothing else checks is the one a person types.")
}

func TestQuittingDeclinesEveryRenameItDidNotAskAbout(t *testing.T) {
	var out strings.Builder

	confirmed, err := ConfirmRenames(strings.NewReader("q\n"), &out,
		twoTableSchema(t), twoTablesRenamed(t))
	if err != nil {
		t.Fatalf("ConfirmRenames: %v", err)
	}

	if !confirmed.Empty() {
		t.Errorf("quitting confirmed %+v", confirmed)
	}

	if !strings.Contains(out.String(), "stopped: 0 confirmed, and 2 renames left unconfirmed") {
		t.Errorf("quitting does not say what it left behind:\n%s", out.String())
	}

	t.Log("There is deliberately no yes-to-all: that is a guess wearing a confirmation's clothes")
	t.Log("for every candidate the operator did not read. q is the safe bulk answer, since what it")
	t.Log("bulk-applies is the side that has its own gate.")
}

func TestAStreamWithNothingInItConfirmsNothingAndSaysNobodyAnswered(t *testing.T) {
	var out strings.Builder

	confirmed, err := ConfirmRenames(strings.NewReader(""), &out,
		liveSchema(t), tableRenamed(t))
	if err != nil {
		t.Fatalf("ConfirmRenames: %v", err)
	}

	if !confirmed.Empty() {
		t.Error("a stream with nothing in it confirmed a rename")
	}

	if !strings.Contains(out.String(), "nobody answered") {
		t.Errorf("an exhausted stream is reported as somebody's answer:\n%s", out.String())
	}

	t.Log("A stream that ran out is not somebody saying no, and the two are reported apart because")
	t.Log("they are different facts. Both decline, which is the half that matters: an unread byte")
	t.Log("must never become a rename, and a migr diff with no terminal must not hang either.")
}

func TestAnUnrecognizedAnswerIsAskedAgainRatherThanTakenAsEither(t *testing.T) {
	var out strings.Builder

	confirmed, err := ConfirmRenames(strings.NewReader("maybe\nsure\ny\n"), &out,
		liveSchema(t), tableRenamed(t))
	if err != nil {
		t.Fatalf("ConfirmRenames: %v", err)
	}

	if len(confirmed.Tables) != 1 {
		t.Errorf("the answer after two unrecognized ones was not read:\n%s", out.String())
	}

	if got := strings.Count(out.String(), "answer y to rename"); got != 2 {
		t.Errorf("two unrecognized answers were re-asked %d times, want 2:\n%s", got, out.String())
	}
}

func TestEachPromptStatesWhatDecliningCostsAsWellAsWhatConfirmingDoes(t *testing.T) {
	cases := []struct {
		name string
		to   schema.Snapshot
		says []string
	}{
		{"a table", tableRenamed(t), []string{
			`table "users" -> "clients"`,
			"all 3 of its columns are there unchanged",
			"rename the table, keeping every row in it",
			`drop "users" and create "clients", which loses every row in "users"`,
		}},
		{"a column", columnRenamed(t), []string{
			"column users.note -> users.memo",
			"both are text",
			"rename the column, keeping every row's value in it",
			`drop "note" and add "memo", which loses every value in "note"`,
		}},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			var out strings.Builder

			if _, err := ConfirmRenames(strings.NewReader("n\n"), &out,
				liveSchema(t), one.to); err != nil {
				t.Fatalf("ConfirmRenames: %v", err)
			}

			for _, want := range one.says {
				if !strings.Contains(out.String(), want) {
					t.Errorf("the prompt does not say %q:\n%s", want, out.String())
				}
			}
		})
	}

	t.Log("A prompt reading \"rename x to y? [y/N]\" hides that saying no destroys the data instead.")
	t.Log("Both outcomes are printed because both are the choice, and the evidence is printed")
	t.Log("because that is what a person checks the rename against.")
}

func TestNothingIsAskedWhereThereIsNothingToAsk(t *testing.T) {
	confirmed, err := ConfirmRenames(nil, nil, liveSchema(t), liveSchema(t))
	if err != nil {
		t.Fatalf("ConfirmRenames over two schemas that agree: %v", err)
	}

	if !confirmed.Empty() {
		t.Error("two schemas that agree confirmed something")
	}

	if _, err := ConfirmRenames(nil, nil, liveSchema(t), tableRenamed(t)); !errors.Is(err,
		ErrNotConfirmable) {
		t.Errorf("asking with nowhere to ask reported %v, want ErrNotConfirmable", err)
	}

	var out strings.Builder

	if _, err := ConfirmRenames(strings.NewReader(""), &out,
		liveSchema(t), liveSchema(t)); err != nil {
		t.Fatalf("ConfirmRenames over two schemas that agree: %v", err)
	}

	if out.String() != "" {
		t.Errorf("a prompt with nothing to ask wrote:\n%s", out.String())
	}

	t.Log("A prompt with no streams is a confirmation nobody could have given, so it is a failure")
	t.Log("rather than a silent no -- the one place declining would be wrong, since the caller")
	t.Log("would read it as an operator's own answer. Two schemas that agree ask nothing at all,")
	t.Log("which is why that is not a failure: there was never a question.")
}

func TestTheSummarySaysHowManyOfTheRestBecomeADropAndACreate(t *testing.T) {
	var out strings.Builder

	if _, err := ConfirmRenames(strings.NewReader("y\nn\n"), &out,
		twoTableSchema(t), twoTablesRenamed(t)); err != nil {
		t.Fatalf("ConfirmRenames: %v", err)
	}

	for _, want := range []string{"2 likely renames", "1 of 2 confirmed",
		"the other will be proposed as a drop and a create"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the prompt does not say %q:\n%s", want, out.String())
		}
	}
}

func TestAConfirmedCandidateIsTheRenameTheDifferIsTold(t *testing.T) {
	var out strings.Builder

	confirmed, err := ConfirmRenames(strings.NewReader("y\ny\n"), &out,
		liveSchema(t), bothRenamed(t))
	if err != nil {
		t.Fatalf("ConfirmRenames: %v", err)
	}

	if len(confirmed.Tables) != 1 || confirmed.Tables[0] !=
		(diff.TableRename{From: "users", To: "clients"}) {
		t.Fatalf("the confirmed table rename reads %+v\n%s", confirmed.Tables, out.String())
	}

	if len(confirmed.Columns) != 1 || confirmed.Columns[0] !=
		(diff.ColumnRename{Table: "clients", From: "note", To: "memo"}) {
		t.Fatalf("the confirmed column rename reads %+v\n%s", confirmed.Columns, out.String())
	}

	t.Log("The value this returns is the parameter diff.Diff takes, so detect -> confirm -> diff is")
	t.Log("three calls with nothing between them. M11-T5 is the one that makes them.")
}

func TestConfirmingATableRenameIsWhatMakesTheRenamesInsideItDetectable(t *testing.T) {
	var out strings.Builder

	confirmed, err := ConfirmRenames(strings.NewReader("y\ny\n"), &out,
		liveSchema(t), bothRenamed(t))
	if err != nil {
		t.Fatalf("ConfirmRenames: %v", err)
	}

	if !strings.Contains(out.String(), "confirming that made 1 more rename likely") {
		t.Errorf("the second round does not say why there is one:\n%s", out.String())
	}

	if len(confirmed.Columns) != 1 {
		t.Errorf("the column rename inside the renamed table was never asked about:\n%s",
			out.String())
	}

	declined := &strings.Builder{}

	also, err := ConfirmRenames(strings.NewReader("n\n"), declined,
		liveSchema(t), bothRenamed(t))
	if err != nil {
		t.Fatalf("ConfirmRenames: %v", err)
	}

	if !also.Empty() {
		t.Errorf("declining the table rename still confirmed %+v", also)
	}

	if strings.Contains(declined.String(), "users.note") {
		t.Errorf("a column rename was asked about inside a table nobody confirmed:\n%s",
			declined.String())
	}

	t.Log("A rename conditional on another rename is not a question anybody can answer, so the")
	t.Log("second is asked only once the first is confirmed -- and never where it was declined,")
	t.Log("since the column rename would then name a table the schema being changed does not hold.")
	t.Log("The rounds are driven here because this is the only thing that knows what it has asked:")
	t.Log("a loop outside would re-propose every declined candidate forever.")
}

// twoTableSchema is the schema whose both tables the cases that count questions rename.
func twoTableSchema(t *testing.T) schema.Snapshot {
	t.Helper()

	live := liveSchema(t)
	live.Tables = append(live.Tables, schema.Table{
		Name:       "orders",
		Columns:    []schema.Column{{Name: "ref", Type: exact(t, "string(8)")}},
		PrimaryKey: &schema.PrimaryKey{Name: "orders_pkey", Columns: []string{"ref"}},
	})

	return live
}

// twoTablesRenamed is that schema with both of its tables under another name, and nothing else moved.
func twoTablesRenamed(t *testing.T) schema.Snapshot {
	t.Helper()

	renamed := twoTableSchema(t)
	renamed.Tables[0].Name = "clients"
	renamed.Tables[1].Name = "purchases"

	return renamed
}
