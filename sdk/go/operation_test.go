package migr

import (
	"errors"
	"strings"
	"testing"
)

// refused is one operation this SDK must not send, and what it must say about it.
type refused struct {
	// why names the §9.1 rule the operation breaks.
	why string
	op  Operation
	// says is a fragment the refusal must carry, so the author is told which field.
	says string
}

// refusals covers every structural rule docs/ir.md §9.1 states, for every operation.
var refusals = map[Kind][]refused{
	KindCreateTable: {
		{why: "a required field is empty", op: CreateTable{
			Columns: []Column{{Name: "id", Type: UUID()}},
		}, says: "table is required"},
		{why: "a required list is empty", op: CreateTable{Table: "users"},
			says: "at least one column"},
		{why: "a list repeats", op: CreateTable{Table: "users", Columns: []Column{
			{Name: "id", Type: UUID()}, {Name: "id", Type: UUID()},
		}}, says: "declared more than once"},
		{why: "the key names a column the table has not got", op: CreateTable{
			Table:      "users",
			Columns:    []Column{{Name: "id", Type: UUID()}},
			PrimaryKey: []string{"uuid"},
		}, says: "not a column of this table"},
		{why: "a column has no type", op: CreateTable{
			Table: "users", Columns: []Column{{Name: "id"}},
		}, says: "no logical type"},
		{why: "a default sets both of its either-or members", op: CreateTable{
			Table: "users",
			Columns: []Column{{
				Name: "id", Type: UUID(),
				Default: &Default{Literal: pointerTo("x"), Function: FuncUUID},
			}},
		}, says: "not both"},
		{why: "a default names a function outside the closed set", op: CreateTable{
			Table: "users",
			Columns: []Column{{
				Name: "created_at", Type: TimestampTZ(), Default: Call("now()"),
			}},
		}, says: "unknown default function"},
	},
	KindDropTable: {
		{why: "a required field is empty", op: DropTable{}, says: "table is required"},
		{why: "the recorded definition rebuilds nothing", op: DropTable{
			Table: "users", From: &TableDef{},
		}, says: "at least one column"},
	},
	KindRenameTable: {
		{why: "a required field is empty", op: RenameTable{To: "people"}, says: "from is required"},
		{why: "the rename changes nothing", op: RenameTable{From: "users", To: "users"},
			says: "same from and to"},
	},
	KindAddColumn: {
		{why: "a required field is empty", op: AddColumn{
			Column: Column{Name: "email", Type: String(255)},
		}, says: "table is required"},
		{why: "the column has no name", op: AddColumn{
			Table: "users", Column: Column{Type: String(255)},
		}, says: "column name is required"},
	},
	KindDropColumn: {
		{why: "a required field is empty", op: DropColumn{Table: "users"},
			says: "column is required"},
		{why: "the recorded column is a different column", op: DropColumn{
			Table: "users", Column: "email", From: &Column{Name: "address", Type: String(255)},
		}, says: "does not name the column being dropped"},
	},
	KindRenameColumn: {
		{why: "a required field is empty", op: RenameColumn{From: "email", To: "address"},
			says: "table is required"},
		{why: "the rename changes nothing", op: RenameColumn{
			Table: "users", From: "email", To: "email",
		}, says: "same from and to"},
	},
	KindAlterColumn: {
		{why: "a required field is empty", op: AlterColumn{
			Table: "users", Nullable: pointerTo(true),
		}, says: "column is required"},
		{why: "it requests no change", op: AlterColumn{Table: "users", Column: "email"},
			says: "requests no change"},
		{why: "it sets both of two either-or members", op: AlterColumn{
			Table: "users", Column: "email", Default: Literal("x"), DropDefault: true,
		}, says: "cannot both be set"},
		{why: "the recorded state sets both of two either-or members", op: AlterColumn{
			Table: "users", Column: "email", Nullable: pointerTo(true),
			From: &ColumnState{Default: Literal("x"), NoDefault: true},
		}, says: "cannot both be set"},
	},
	KindAddIndex: {
		{why: "a required field is empty", op: AddIndex{Table: "users", Columns: []string{"email"}},
			says: "name is required"},
		{why: "a required list is empty", op: AddIndex{Table: "users", Name: "idx"},
			says: "at least one column"},
		{why: "a list repeats", op: AddIndex{
			Table: "users", Name: "idx", Columns: []string{"email", "email"},
		}, says: "more than once"},
	},
	KindDropIndex: {
		{why: "a required field is empty", op: DropIndex{Table: "users"}, says: "name is required"},
		{why: "the recorded list repeats", op: DropIndex{
			Table: "users", Name: "idx", From: &ColumnsDef{Columns: []string{"a", "a"}},
		}, says: "more than once"},
	},
	KindAddForeignKey: {
		{why: "a required field is empty", op: AddForeignKey{
			Table: "posts", Name: "fk", Columns: []string{"author_id"},
			ReferencesColumns: []string{"id"},
		}, says: "references_table is required"},
		{why: "the paired lists are not the same length", op: AddForeignKey{
			Table: "posts", Name: "fk", Columns: []string{"author_id", "tenant_id"},
			ReferencesTable: "users", ReferencesColumns: []string{"id"},
		}, says: "references_columns has 1"},
		{why: "an action is outside the closed set", op: AddForeignKey{
			Table: "posts", Name: "fk", Columns: []string{"author_id"},
			ReferencesTable: "users", ReferencesColumns: []string{"id"},
			OnDelete: "CASCADE",
		}, says: "unknown on_delete action"},
	},
	KindDropForeignKey: {
		{why: "a required field is empty", op: DropForeignKey{Name: "fk"},
			says: "table is required"},
		{why: "the recorded pair is not the same length", op: DropForeignKey{
			Table: "posts", Name: "fk", From: &ForeignKeyDef{
				Columns:         []string{"author_id", "tenant_id"},
				ReferencesTable: "users", ReferencesColumns: []string{"id"},
			},
		}, says: "from.references_columns has 1"},
	},
	KindAddUnique: {
		{why: "a required field is empty", op: AddUnique{Table: "users", Name: "uq"},
			says: "at least one column"},
		{why: "a list repeats", op: AddUnique{
			Table: "users", Name: "uq", Columns: []string{"email", "email"},
		}, says: "more than once"},
	},
	KindDropUnique: {
		{why: "a required field is empty", op: DropUnique{Table: "users"}, says: "name is required"},
		{why: "the recorded list is empty", op: DropUnique{
			Table: "users", Name: "uq", From: &ColumnsDef{},
		}, says: "at least one column"},
	},
	KindAddCheck: {
		{why: "a required field is empty", op: AddCheck{Table: "orders", Name: "ck"},
			says: "expression is required"},
	},
	KindDropCheck: {
		{why: "a required field is empty", op: DropCheck{Name: "ck"}, says: "table is required"},
		{why: "the recorded expression is empty", op: DropCheck{
			Table: "orders", Name: "ck", From: &CheckDef{},
		}, says: "from.expression is required"},
	},
	KindAddPrimaryKey: {
		{why: "a required list is empty", op: AddPrimaryKey{Table: "users"},
			says: "at least one column"},
		{why: "a list repeats", op: AddPrimaryKey{
			Table: "users", Columns: []string{"id", "id"},
		}, says: "more than once"},
	},
	KindDropPrimaryKey: {
		{why: "a required field is empty", op: DropPrimaryKey{}, says: "table is required"},
		{why: "the recorded list is empty", op: DropPrimaryKey{
			Table: "users", From: &ColumnsDef{Columns: []string{""}},
		}, says: "from.columns[0] is empty"},
	},
	KindCreateView: {
		{why: "a required field is empty", op: CreateView{Query: "SELECT 1"},
			says: "view is required"},
		{why: "a required field is empty", op: CreateView{View: "v"}, says: "query is required"},
	},
	KindDropView: {
		{why: "a required field is empty", op: DropView{}, says: "view is required"},
		{why: "the recorded query is empty", op: DropView{View: "v", From: &ViewDef{}},
			says: "from.query is required"},
	},
	KindRaw: {
		{why: "a required field is empty", op: Raw{SQL: "SELECT 1"}, says: "driver is required"},
		{why: "a required field is empty", op: Raw{Driver: "postgres"}, says: "sql is required"},
	},
	KindDataCopy: {
		{why: "a table is copied into itself", op: DataCopy{From: "a", To: "a",
			Columns: []CopyColumn{{From: "id", To: "id"}}}, says: "into itself"},
		{why: "a required list is empty", op: DataCopy{From: "a", To: "b"},
			says: "at least one column"},
		{why: "a target is written twice", op: DataCopy{From: "a", To: "b", Columns: []CopyColumn{
			{From: "x", To: "id"}, {From: "y", To: "id"}}}, says: "already writes"},
	},
	KindDataBackfill: {
		{why: "a required field is empty", op: DataBackfill{Table: "t", Column: "c",
			Value: Literal("x")}, says: "type is required"},
		{why: "a required field is empty", op: DataBackfill{Table: "t", Column: "c",
			Type: Text()}, says: "value is required"},
	},
	KindDataTransform: {
		{why: "a function is unknown", op: DataTransform{Table: "t", Column: "c", Type: Text(),
			From: "d", Function: "reverse"}, says: "unknown function"},
		{why: "a copy into itself changes nothing", op: DataTransform{Table: "t", Column: "c",
			Type: Text(), From: "c", Function: TransformCopy}, says: "changes nothing"},
	},
}

func TestEveryOperationRefusesWhatTheSpecificationSaysIsNotOne(t *testing.T) {
	for _, kind := range Kinds() {
		cases, covered := refusals[kind]
		if !covered {
			t.Errorf("%q has no §9.1 case, so nothing holds this SDK to refusing one", kind)

			continue
		}

		for _, c := range cases {
			t.Run(string(kind)+": "+c.why, func(t *testing.T) {
				assertRefused(t, kind, c)
			})
		}
	}

	t.Log("docs/ir.md §9.1: a document failing any of these is not an operation. The host runs")
	t.Log("every one of them again when it decodes -- this copy exists so the failure names the")
	t.Log("call that built it, and so two SDKs refuse the same operations.")
}

// assertRefused reports whether one operation is refused before it can reach the wire.
func assertRefused(t *testing.T, kind Kind, c refused) {
	t.Helper()

	if c.op.Kind() != kind {
		t.Fatalf("the case is filed under %q but is a %q", kind, c.op.Kind())
	}

	_, err := documentsOf([]Operation{c.op})
	if err == nil {
		t.Fatalf("this SDK would have sent it: %+v", c.op)
	}

	if !errors.Is(err, ErrInvalidOperation) {
		t.Errorf("the refusal is not an invalid operation: %v", err)
	}

	if !strings.Contains(err.Error(), c.says) {
		t.Errorf("the refusal does not say %q: %v", c.says, err)
	}

	if !strings.Contains(err.Error(), string(kind)) {
		t.Errorf("the refusal does not name the operation: %v", err)
	}
}

func TestAnOperationIsNumberedByItsPlaceInTheCall(t *testing.T) {
	_, err := documentsOf([]Operation{
		Raw{Driver: "postgres", SQL: "SELECT 1"},
		Raw{Driver: "postgres"},
	})
	if err == nil {
		t.Fatal("a call carrying an unusable operation was sent")
	}

	if !strings.Contains(err.Error(), "operation 2") {
		t.Errorf("the refusal does not say which operation it is about: %v", err)
	}
}

func TestNothingIsNotAnOperation(t *testing.T) {
	_, err := documentsOf([]Operation{nil})
	if err == nil {
		t.Fatal("a nil operation was sent")
	}

	if !errors.Is(err, ErrInvalidOperation) {
		t.Errorf("the refusal is not an invalid operation: %v", err)
	}
}

func TestTheDiscriminatorIsWrittenFirstAndIsNotAFieldAnAuthorSets(t *testing.T) {
	for _, kind := range Kinds() {
		expressed, ok := chains[kind]
		if !ok {
			continue
		}

		document, err := documentOf(expressed.want)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}

		if want := `{"op":"` + string(kind) + `"`; !strings.HasPrefix(string(document), want) {
			t.Errorf("%s does not open with its discriminator: %s", kind, document)
		}
	}

	t.Log("docs/ir.md §3.1: op comes first and is not a field of any operation. The encoder")
	t.Log("writes it, so the tag and the operation's identity cannot disagree.")
}
