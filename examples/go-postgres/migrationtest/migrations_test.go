package migrationtest

import (
	"slices"
	"testing"

	migr "github.com/emrebdr/migr/sdk/go"
	"github.com/emrebdr/migr/sdk/go/migrtest"

	"example.com/go-postgres/migrations"
)

func TestCreateAccountsHasItsDownDerived(t *testing.T) {
	if r := migrtest.Reversal(t, migrations.CreateAccounts{}); r.Undo != migr.UndoDerived {
		t.Errorf("Undo = %s, want derived", r.Undo)
	}
}

func TestAddNicknameAddsAColumnAndItsIndex(t *testing.T) {
	got := migrtest.Up(t, migrations.AddNickname{}).Kinds()

	if !slices.Equal(got, []migr.Kind{migr.KindAddColumn, migr.KindAddIndex}) {
		t.Errorf("Up emitted %v", got)
	}
}

func TestAddNicknameAuthorsItsDown(t *testing.T) {
	got := migrtest.Down(t, migrations.AddNickname{}).Kinds()

	if !slices.Equal(got, []migr.Kind{migr.KindDropIndex, migr.KindDropColumn}) {
		t.Errorf("Down emitted %v", got)
	}
}

// withAccounts answers AddTier's one read as a table already holding three accounts.
func withAccounts() migrtest.Stage {
	return migrtest.Stage{Reads: map[string]migr.Rows{
		"SELECT count(*) FROM accounts": migrtest.Answer([]string{"count"}, []string{"3"}),
	}}
}

// empty answers AddTier's one read as a table holding no accounts.
func empty() migrtest.Stage {
	return migrtest.Stage{Reads: map[string]migr.Rows{
		"SELECT count(*) FROM accounts": migrtest.Answer([]string{"count"}, []string{"0"}),
	}}
}

func TestAddTierDefaultsWhereAccountsExist(t *testing.T) {
	withAccounts().Up(t, migrations.AddTier{}).Is(t, migr.AddColumn{Table: "accounts", Column: migr.Column{
		Name: "tier", Type: migr.String(16), Default: migr.Literal("free"),
	}})
}

func TestAddTierNeedsNoDefaultOnAnEmptyTable(t *testing.T) {
	empty().Up(t, migrations.AddTier{}).Is(t, migr.AddColumn{Table: "accounts", Column: migr.Column{
		Name: "tier", Type: migr.String(16),
	}})
}

func TestAddTierCannotBeUndone(t *testing.T) {
	if r := withAccounts().Reversal(t, migrations.AddTier{}); r.Undo != migr.UndoDeclared {
		t.Errorf("Undo = %s, want declared", r.Undo)
	}
}

func TestTheMigrationsApplyAndRefuseToRollBackPastTheTier(t *testing.T) {
	project := migrtest.New(t, migrtest.Project{Dir: "../migrations"})

	project.Validate(t).NoProblems(t)

	if got := project.Up(t).Versions(); len(got) != 3 {
		t.Fatalf("migr up applied %v, want all three", got)
	}

	if refusal := project.Refused(t, "down"); !refusal.Says("cannot be undone") {
		t.Errorf("migr down failed with %q, want the tier's declared reason", refusal.Error)
	}
}
