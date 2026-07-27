package ir

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReverseMappingIsTotal(t *testing.T) {
	covered := make(map[Kind]bool, len(Kinds()))

	for _, f := range fixtures(t) {
		covered[f.kind] = true

		inverse, err := f.op.Reverse()

		switch {
		case err == nil && inverse == nil:
			t.Errorf("%s returned no inverse and no reason", f.kind)
		case err == nil:
			continue
		}

		var irr *IrreversibleError
		if !errors.As(err, &irr) {
			t.Errorf("%s failed with %T, want *IrreversibleError: %v", f.kind, err, err)

			continue
		}

		if strings.TrimSpace(irr.Reason) == "" {
			t.Errorf("%s is irreversible with no stated reason", f.kind)
		}

		if irr.Kind != f.kind {
			t.Errorf("%s reported irreversibility as %s", f.kind, irr.Kind)
		}

		if !errors.Is(err, ErrIrreversible) {
			t.Errorf("%s: errors.Is must reach ErrIrreversible", f.kind)
		}
	}

	for _, k := range Kinds() {
		if !covered[k] {
			t.Errorf("operation %q was never asked for its inverse", k)
		}
	}
}

func TestEveryIrreversibleReasonIsSpecific(t *testing.T) {
	seen := make(map[string]Kind)

	for _, f := range fixtures(t) {
		_, err := f.op.Reverse()
		if err == nil {
			continue
		}

		var irr *IrreversibleError
		if !errors.As(err, &irr) {
			continue
		}

		if prior, ok := seen[irr.Reason]; ok {
			t.Errorf("%s and %s share the reason %q; each needs its own diagnosis",
				prior, f.kind, irr.Reason)
		}

		seen[irr.Reason] = f.kind
	}
}

func TestAddOperationsPairWithTheirDrops(t *testing.T) {
	tests := []struct {
		name string
		op   Operation
		want Operation
	}{
		{
			name: "create_table",
			op:   CreateTable{Table: "users", Columns: []Column{{Name: "id", Type: mustType(t, "uuid")}}},
			want: DropTable{Table: "users"},
		},
		{
			name: "add_column",
			op:   AddColumn{Table: "users", Column: Column{Name: "email", Type: mustType(t, "text")}},
			want: DropColumn{Table: "users", Column: "email"},
		},
		{
			name: "add_index",
			op:   AddIndex{Table: "users", Name: "idx", Columns: []string{"email"}},
			want: DropIndex{Table: "users", Name: "idx"},
		},
		{
			name: "add_foreign_key",
			op: AddForeignKey{
				Table: "orders", Name: "fk", Columns: []string{"user_id"},
				ReferencesTable: "users", ReferencesColumns: []string{"id"},
			},
			want: DropForeignKey{Table: "orders", Name: "fk"},
		},
		{
			name: "add_unique",
			op:   AddUnique{Table: "users", Name: "uq", Columns: []string{"email"}},
			want: DropUnique{Table: "users", Name: "uq"},
		},
		{
			name: "add_check",
			op:   AddCheck{Table: "orders", Name: "ck", Expression: "total >= 0"},
			want: DropCheck{Table: "orders", Name: "ck"},
		},
		{
			name: "add_primary_key",
			op:   AddPrimaryKey{Table: "legacy", Name: "pk", Columns: []string{"id"}},
			want: DropPrimaryKey{Table: "legacy", Name: "pk"},
		},
		{
			name: "create_view",
			op:   CreateView{View: "v", Query: "SELECT 1"},
			want: DropView{View: "v"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.op.Reverse()
			if err != nil {
				t.Fatalf("should be reversible: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Reverse() = %#v, want %#v", got, tt.want)
			}

			if err := got.Validate(); err != nil {
				t.Errorf("the inverse must itself be a valid operation: %v", err)
			}
		})
	}
}

func TestRenamesAreTheirOwnInverse(t *testing.T) {
	table := RenameTable{From: "users", To: "accounts"}

	once, err := table.Reverse()
	if err != nil {
		t.Fatalf("rename_table must reverse: %v", err)
	}

	if !reflect.DeepEqual(once, RenameTable{From: "accounts", To: "users"}) {
		t.Errorf("rename_table reversed to %#v", once)
	}

	twice, err := once.Reverse()
	if err != nil {
		t.Fatalf("reversing twice: %v", err)
	}

	if !reflect.DeepEqual(twice, table) {
		t.Errorf("rename_table is not an involution: %#v", twice)
	}

	column := RenameColumn{Table: "users", From: "email", To: "email_address"}

	back, err := column.Reverse()
	if err != nil {
		t.Fatalf("rename_column must reverse: %v", err)
	}

	again, err := back.Reverse()
	if err != nil {
		t.Fatalf("reversing twice: %v", err)
	}

	if !reflect.DeepEqual(again, column) {
		t.Errorf("rename_column is not an involution: %#v", again)
	}
}

func TestDropsAreIrreversibleWithoutPriorState(t *testing.T) {
	drops := []struct {
		name string
		op   Operation
		want string
	}{
		{name: "drop_table", op: DropTable{Table: "users"}, want: "table definition"},
		{name: "drop_column", op: DropColumn{Table: "users", Column: "email"}, want: "column definition"},
		{name: "drop_index", op: DropIndex{Table: "users", Name: "idx"}, want: "index definition"},
		{name: "drop_foreign_key", op: DropForeignKey{Table: "orders", Name: "fk"}, want: "foreign key definition"},
		{name: "drop_unique", op: DropUnique{Table: "users", Name: "uq"}, want: "unique constraint definition"},
		{name: "drop_check", op: DropCheck{Table: "orders", Name: "ck"}, want: "check expression"},
		{name: "drop_primary_key", op: DropPrimaryKey{Table: "legacy"}, want: "primary key definition"},
		{name: "drop_view", op: DropView{View: "v"}, want: "view query"},
	}

	for _, tt := range drops {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.op.Reverse()
			if err == nil {
				t.Fatalf("must be irreversible without prior state, got %#v", got)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("reason should name the %s, got %q", tt.want, err)
			}

			if !strings.Contains(err.Error(), `"from"`) {
				t.Errorf("the reason should say how to fix it, got %q", err)
			}
		})
	}
}

func TestDropsReverseExactlyWhenPriorStateIsRecorded(t *testing.T) {
	column := Column{Name: "email", Type: mustType(t, "string(255)"), Nullable: true}

	tests := []struct {
		name string
		op   Operation
		want Operation
	}{
		{
			name: "drop_table",
			op: DropTable{Table: "users", From: &TableDef{
				Columns: []Column{column}, PrimaryKey: []string{"email"},
			}},
			want: CreateTable{Table: "users", Columns: []Column{column}, PrimaryKey: []string{"email"}},
		},
		{
			name: "drop_column",
			op:   DropColumn{Table: "users", Column: "email", From: &column},
			want: AddColumn{Table: "users", Column: column},
		},
		{
			name: "drop_index",
			op:   DropIndex{Table: "users", Name: "idx", From: &ColumnsDef{Columns: []string{"email"}}},
			want: AddIndex{Table: "users", Name: "idx", Columns: []string{"email"}},
		},
		{
			name: "drop_foreign_key",
			op: DropForeignKey{Table: "orders", Name: "fk", From: &ForeignKeyDef{
				Columns: []string{"user_id"}, ReferencesTable: "users",
				ReferencesColumns: []string{"id"}, OnDelete: ActionCascade,
			}},
			want: AddForeignKey{
				Table: "orders", Name: "fk", Columns: []string{"user_id"},
				ReferencesTable: "users", ReferencesColumns: []string{"id"}, OnDelete: ActionCascade,
			},
		},
		{
			name: "drop_unique",
			op:   DropUnique{Table: "users", Name: "uq", From: &ColumnsDef{Columns: []string{"email"}}},
			want: AddUnique{Table: "users", Name: "uq", Columns: []string{"email"}},
		},
		{
			name: "drop_check",
			op:   DropCheck{Table: "orders", Name: "ck", From: &CheckDef{Expression: "total >= 0"}},
			want: AddCheck{Table: "orders", Name: "ck", Expression: "total >= 0"},
		},
		{
			name: "drop_primary_key",
			op:   DropPrimaryKey{Table: "legacy", Name: "pk", From: &ColumnsDef{Columns: []string{"id"}}},
			want: AddPrimaryKey{Table: "legacy", Name: "pk", Columns: []string{"id"}},
		},
		{
			name: "drop_view",
			op:   DropView{View: "v", From: &ViewDef{Query: "SELECT 1"}},
			want: CreateView{View: "v", Query: "SELECT 1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.op.Reverse()
			if err != nil {
				t.Fatalf("recorded prior state must make it reversible: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Reverse() = %#v, want %#v", got, tt.want)
			}

			if err := got.Validate(); err != nil {
				t.Errorf("the inverse must itself be a valid operation: %v", err)
			}
		})
	}
}

func TestAlterColumnReversesNoDefaultAsDropDefault(t *testing.T) {
	op := AlterColumn{
		Table:   "users",
		Column:  "status",
		Default: Literal("draft"),
		From:    &ColumnState{Type: ptrType(t, "string(32)")},
	}

	inverse, err := op.Reverse()
	if err != nil {
		t.Fatalf("recorded prior state must make it reversible: %v", err)
	}

	back, ok := inverse.(AlterColumn)
	if !ok {
		t.Fatalf("inverse is %T, want AlterColumn", inverse)
	}

	if !back.DropDefault {
		t.Error("the column had no default before, so the inverse must drop the one this op set")
		t.Log("Merely omitting the default field leaves the new default in place after rollback.")
	}

	if back.Default != nil {
		t.Errorf("the inverse must not also set a default: %#v", back.Default)
	}

	if err := back.Validate(); err != nil {
		t.Errorf("the inverse must be a valid operation: %v", err)
	}
}

func TestAlterColumnRestoresARecordedDefault(t *testing.T) {
	op := AlterColumn{
		Table:   "users",
		Column:  "status",
		Default: Literal("live"),
		From:    &ColumnState{Default: Literal("draft")},
	}

	inverse, err := op.Reverse()
	if err != nil {
		t.Fatalf("reversing: %v", err)
	}

	back, ok := inverse.(AlterColumn)
	if !ok {
		t.Fatalf("inverse is %T, want AlterColumn", inverse)
	}

	if back.DropDefault {
		t.Error("a recorded prior default must be restored, not dropped")
	}

	if back.Default == nil || back.Default.Literal == nil || *back.Default.Literal != "draft" {
		t.Errorf("the inverse should restore the prior default, got %#v", back.Default)
	}
}

func TestAlterColumnIsAnInvolutionWithPriorState(t *testing.T) {
	op := AlterColumn{
		Table:    "users",
		Column:   "email",
		Type:     ptrType(t, "string(512)"),
		Nullable: ptrBool(true),
		From:     &ColumnState{Type: ptrType(t, "string(255)"), Nullable: ptrBool(false)},
	}

	once, err := op.Reverse()
	if err != nil {
		t.Fatalf("reversing: %v", err)
	}

	twice, err := once.Reverse()
	if err != nil {
		t.Fatalf("reversing twice: %v", err)
	}

	if !reflect.DeepEqual(twice, op) {
		t.Errorf("alter_column with prior state should be an involution:\n got %#v\nwant %#v", twice, op)
	}
}

func TestAlterColumnWithoutPriorStateIsIrreversible(t *testing.T) {
	op := AlterColumn{Table: "users", Column: "email", Nullable: ptrBool(true)}

	if _, err := op.Reverse(); err == nil {
		t.Fatal("without prior state there is nothing to restore")
	}

	empty := AlterColumn{Table: "users", Column: "email", Nullable: ptrBool(true), From: &ColumnState{}}

	if _, err := empty.Reverse(); err == nil {
		t.Fatal("an empty prior state records nothing and must be irreversible")
	}
}

func TestCreateViewDoesNotAssumeTheViewWasNew(t *testing.T) {
	fresh := CreateView{View: "v", Query: "SELECT 1"}

	got, err := fresh.Reverse()
	if err != nil {
		t.Fatalf("a new view reverses to a drop: %v", err)
	}

	if !reflect.DeepEqual(got, DropView{View: "v"}) {
		t.Errorf("Reverse() = %#v, want DropView", got)
	}

	replacing := CreateView{View: "v", Query: "SELECT 2", OrReplace: true}

	if _, err := replacing.Reverse(); err == nil {
		t.Fatal("or_replace may have overwritten a view; dropping it would destroy the original")
	}

	recorded := CreateView{
		View: "v", Query: "SELECT 2", OrReplace: true, From: &ViewDef{Query: "SELECT 1"},
	}

	restored, err := recorded.Reverse()
	if err != nil {
		t.Fatalf("a recorded prior query must make it reversible: %v", err)
	}

	view, ok := restored.(CreateView)
	if !ok {
		t.Fatalf("inverse is %T, want CreateView", restored)
	}

	if view.Query != "SELECT 1" || !view.OrReplace {
		t.Errorf("the inverse should replace the view with its prior query, got %#v", view)
	}
}

func TestRawIsAlwaysIrreversible(t *testing.T) {
	op := Raw{Driver: "postgres", SQL: "CREATE EXTENSION pgcrypto"}

	_, err := op.Reverse()
	if err == nil {
		t.Fatal("the framework cannot know what raw SQL did")
	}

	if !strings.Contains(err.Error(), "opaque") {
		t.Errorf("the reason should say why, got %q", err)
	}
}

func TestReverseAllInvertsAndReversesOrder(t *testing.T) {
	ops := Operations{
		CreateTable{Table: "users", Columns: []Column{{Name: "id", Type: mustType(t, "uuid")}}},
		AddColumn{Table: "users", Column: Column{Name: "email", Type: mustType(t, "text")}},
		AddIndex{Table: "users", Name: "idx", Columns: []string{"email"}},
	}

	reversed, err := ReverseAll(ops)
	if err != nil {
		t.Fatalf("ReverseAll: %v", err)
	}

	want := []Kind{KindDropIndex, KindDropColumn, KindDropTable}

	if len(reversed) != len(want) {
		t.Fatalf("got %d operations, want %d", len(reversed), len(want))
	}

	for i, k := range want {
		if reversed[i].Kind() != k {
			t.Errorf("position %d is %s, want %s", i, reversed[i].Kind(), k)
		}
	}
}

func TestReverseAllFailsWholeOnAnIrreversibleOperation(t *testing.T) {
	ops := Operations{
		AddColumn{Table: "users", Column: Column{Name: "email", Type: mustType(t, "text")}},
		Raw{Driver: "postgres", SQL: "VACUUM"},
		AddIndex{Table: "users", Name: "idx", Columns: []string{"email"}},
	}

	reversed, err := ReverseAll(ops)
	if err == nil {
		t.Fatal("a list containing an irreversible operation must fail")
	}

	if reversed != nil {
		t.Errorf("a partial reversal is worse than refusing, got %d operations", len(reversed))
	}

	if !strings.Contains(err.Error(), "operation 1") {
		t.Errorf("the error should name the offending position, got %q", err)
	}

	if !errors.Is(err, ErrIrreversible) {
		t.Error("errors.Is must reach ErrIrreversible through the wrapping")
	}
}

func TestReverseAllOnAnEmptyListSucceeds(t *testing.T) {
	reversed, err := ReverseAll(Operations{})
	if err != nil {
		t.Fatalf("an empty list reverses to an empty list: %v", err)
	}

	if len(reversed) != 0 {
		t.Errorf("got %d operations, want 0", len(reversed))
	}
}
