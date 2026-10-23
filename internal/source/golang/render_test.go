package golang

import (
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/schema"
)

func typ(t *testing.T, spelling string) schema.Type {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	return parsed
}

func pointerTo[T any](v T) *T { return &v }

// A renderer whose output does not compile is not a renderer, so the test that matters builds it.
// It goes through the real toolchain against the real SDK by a module replace, which is what
// internal/runtime/golang's own suite does and is untagged for the same reason: the Go toolchain is
// present by definition when go test is what is running.

func TestWhatItRendersCompilesAgainstTheRealSDK(t *testing.T) {
	body, err := Render(sampleMigration(t), everyOperation(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	dir := t.TempDir()
	root := repoRoot(t)

	write(t, filepath.Join(dir, "go.mod"), fmt.Sprintf(`module rendered

go 1.26

require github.com/emrebdr/migr/sdk/go v0.0.0

replace github.com/emrebdr/migr/sdk/go => %s
`, filepath.Join(root, "sdk", "go")))

	migrations := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging a migrations package: %v", err)
	}

	write(t, filepath.Join(migrations, "20261008T120000_rendered.go"), body)

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod")

	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("the rendered migration does not compile: %v\n%s\n\nwhat was rendered:\n%s",
			err, out, body)
	}

	formatted, err := format.Source([]byte(body))
	if err != nil {
		t.Fatalf("the rendered migration does not parse: %v", err)
	}

	if string(formatted) != body {
		t.Errorf("the rendered migration is not what gofmt writes:\n%s", diffLines(body,
			string(formatted)))
		t.Log("A generated file people read and edit has to arrive formatted, since the next thing")
		t.Log("that touches it will reformat it and leave a diff nobody wrote.")
	}

	if !strings.Contains(body, "migr.Literal") {
		t.Error("the file lost the recorded payloads")
	}

	t.Log("It is built as the package it is -- a Go migration lives in package migrations and is")
	t.Log("compiled into a runner by the go tool, so a rendered chain that did not")
	t.Log("compile would stop migr up rather than this test.")
}

func TestTwoGeneratedMigrationsInOnePackageCompileTogether(t *testing.T) {
	first, err := Render(sampleMigration(t), everyOperation(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	later, err := migration.New(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), "rendered", Ext)
	if err != nil {
		t.Fatalf("naming the second migration: %v", err)
	}

	second, err := Render(later, everyOperation(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	dir := t.TempDir()
	root := repoRoot(t)

	write(t, filepath.Join(dir, "go.mod"), fmt.Sprintf(`module rendered

go 1.26

require github.com/emrebdr/migr/sdk/go v0.0.0

replace github.com/emrebdr/migr/sdk/go => %s
`, filepath.Join(root, "sdk", "go")))

	migrations := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migrations, 0o750); err != nil {
		t.Fatalf("staging a migrations package: %v", err)
	}

	write(t, filepath.Join(migrations, "20261008T120000_rendered.go"), first)
	write(t, filepath.Join(migrations, "20261009T120000_rendered.go"), second)

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod")

	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("two generated migrations do not compile together: %v\n%s", err, out)
	}

	t.Log("Every Go migration in a project shares one package, and the default name is the same")
	t.Log("for every diff -- so the identifier carries the version discovery proved unique rather")
	t.Log("than the name. Named from the name, this is a project that does not build.")
}

// everyOperation is one of each kind the differ emits, which is every kind but raw.
func everyOperation(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.CreateTable{Table: "users", Columns: []ir.Column{
			{Name: "id", Type: typ(t, "uuid")},
			{Name: "email", Type: typ(t, "string(255)")},
			{Name: "note", Type: typ(t, "text"), Nullable: true, Default: ir.Literal("")},
			{Name: "seen", Type: typ(t, "timestamptz"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentTimestamp)},
			{Name: "rate", Type: typ(t, "decimal(10,2)"), Nullable: true},
			{Name: "tags", Type: typ(t, "array(string(8))"), Nullable: true},
			{Name: "mood", Type: typ(t, "enum(up,down)"), Nullable: true},
			{Name: "small", Type: typ(t, "int16"), Nullable: true},
			{Name: "mid", Type: typ(t, "int32"), Nullable: true},
			{Name: "big", Type: typ(t, "int64"), Nullable: true},
			{Name: "ratio", Type: typ(t, "float32"), Nullable: true},
			{Name: "amount", Type: typ(t, "float64"), Nullable: true},
			{Name: "live", Type: typ(t, "bool"), Nullable: true},
			{Name: "doc", Type: typ(t, "json"), Nullable: true},
			{Name: "stamp", Type: typ(t, "timestamp"), Nullable: true},
			{Name: "day", Type: typ(t, "date"), Nullable: true},
			{Name: "clock", Type: typ(t, "time"), Nullable: true},
			{Name: "blob", Type: typ(t, "binary"), Nullable: true},
		}, PrimaryKey: []string{"id"}},
		ir.RenameTable{From: "users", To: "accounts"},
		ir.AddColumn{Table: "accounts", Column: ir.Column{Name: "plan",
			Type: typ(t, "string(32)"), Nullable: true, Default: ir.Literal("free")}},
		ir.RenameColumn{Table: "accounts", From: "note", To: "memo"},
		ir.AlterColumn{Table: "accounts", Column: "memo",
			Type: typePointer(t, "string(512)"), Nullable: pointerTo(false),
			Default: ir.Literal("none"),
			From: &ir.ColumnState{Type: typePointer(t, "text"), Nullable: pointerTo(true),
				Default: ir.Literal("")}},
		ir.AlterColumn{Table: "accounts", Column: "plan", DropDefault: true,
			From: &ir.ColumnState{Default: ir.Literal("free")}},
		ir.AlterColumn{Table: "accounts", Column: "live", Default: ir.Literal("1"),
			From: &ir.ColumnState{NoDefault: true}},
		ir.AddIndex{Table: "accounts", Name: "accounts_email_idx", Columns: []string{"email"}},
		ir.AddUnique{Table: "accounts", Name: "accounts_email_key", Columns: []string{"email"}},
		ir.AddCheck{Table: "accounts", Name: "accounts_live", Expression: "live is not null"},
		ir.AddPrimaryKey{Table: "accounts", Name: "accounts_pkey", Columns: []string{"id"}},
		ir.AddForeignKey{Table: "accounts", Name: "accounts_self_fk", Columns: []string{"id"},
			ReferencesTable: "accounts", ReferencesColumns: []string{"id"},
			OnDelete: ir.ActionCascade, OnUpdate: ir.ActionSetNull},
		ir.DropIndex{Table: "accounts", Name: "accounts_email_idx",
			From: &ir.ColumnsDef{Columns: []string{"email"}}},
		ir.DropUnique{Table: "accounts", Name: "accounts_email_key",
			From: &ir.ColumnsDef{Columns: []string{"email"}}},
		ir.DropCheck{Table: "accounts", Name: "accounts_live",
			From: &ir.CheckDef{Expression: "live is not null"}},
		ir.DropPrimaryKey{Table: "accounts", Name: "accounts_pkey",
			From: &ir.ColumnsDef{Columns: []string{"id"}}},
		ir.DropForeignKey{Table: "accounts", Name: "accounts_self_fk",
			From: &ir.ForeignKeyDef{Columns: []string{"id"}, ReferencesTable: "accounts",
				ReferencesColumns: []string{"id"}, OnDelete: ir.ActionCascade,
				OnUpdate: ir.ActionSetNull}},
		ir.DropColumn{Table: "accounts", Column: "rate",
			From: &ir.Column{Name: "rate", Type: typ(t, "decimal(10,2)"), Nullable: true}},
		ir.CreateView{View: "live_accounts", Query: "SELECT id FROM accounts"},
		ir.CreateView{View: "live_accounts", Query: "SELECT id, email FROM accounts",
			OrReplace: true, From: &ir.ViewDef{Query: "SELECT id FROM accounts"}},
		ir.DropView{View: "live_accounts",
			From: &ir.ViewDef{Query: "SELECT id, email FROM accounts"}},
		ir.DropTable{Table: "accounts", From: &ir.TableDef{
			Columns: []ir.Column{
				{Name: "id", Type: typ(t, "uuid")},
				{Name: "email", Type: typ(t, "string(255)"), Nullable: true,
					Default: ir.Call(ir.FuncUUID)},
			},
			PrimaryKey: []string{"id"},
		}},
	}
}

// typePointer returns a pointer to a parsed logical type, which an alteration states.
func typePointer(t *testing.T, spelling string) *schema.Type {
	t.Helper()

	parsed := typ(t, spelling)

	return &parsed
}

// repoRoot finds the directory holding go.mod, which is what the module replace points at.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}

		dir = parent
	}
}

// write stages one file of the throwaway module, failing the test rather than returning a reason.
func write(t *testing.T, path, body string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("staging %s: %v", path, err)
	}
}

// sampleMigration is the file a renderer is told it is writing, which names what it declares.
func sampleMigration(t *testing.T) migration.Migration {
	t.Helper()

	m, err := migration.New(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), "rendered", ext())
	if err != nil {
		t.Fatalf("naming the migration a renderer writes: %v", err)
	}

	return m
}

// ext is the extension this language's migrations are stored under.
func ext() string { return Ext }

// diffLines renders the first line two renderings disagree on, which is where an indent went wrong.
func diffLines(got, want string) string {
	mine, theirs := strings.Split(got, "\n"), strings.Split(want, "\n")

	for i := range min(len(mine), len(theirs)) {
		if mine[i] != theirs[i] {
			return fmt.Sprintf("line %d:\n  rendered: %q\n  gofmt:    %q", i+1, mine[i], theirs[i])
		}
	}

	return fmt.Sprintf("the renderings are %d and %d lines long", len(mine), len(theirs))
}

// A byte-exact expectation rather than a set of fragments, because a renderer's output is read by
// people: a payload quietly dropped, a referential action left off, or an indent one tab deep all
// pass every "contains" check there is. Three of those survived the mutation sweep until this.

func TestWhatItRendersIsExactlyThis(t *testing.T) {
	ops := ir.Operations{
		ir.AddForeignKey{Table: "orders", Name: "orders_user_fk", Columns: []string{"user_id"},
			ReferencesTable: "users", ReferencesColumns: []string{"id"},
			OnDelete: ir.ActionCascade, OnUpdate: ir.ActionSetNull},
		ir.AlterColumn{Table: "users", Column: "note",
			Type: typePointer(t, "string(64)"), Nullable: pointerTo(false),
			From: &ir.ColumnState{Type: typePointer(t, "text"), Nullable: pointerTo(true),
				Default: ir.Literal("")}},
		ir.DropForeignKey{Table: "orders", Name: "orders_user_fk",
			From: &ir.ForeignKeyDef{Columns: []string{"user_id"}, ReferencesTable: "users",
				ReferencesColumns: []string{"id"}, OnDelete: ir.ActionCascade,
				OnUpdate: ir.ActionSetNull}},
	}

	body, err := statements(ops)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}

	want := "\tif err := s.Table(\"orders\").\n" +
		"\t\tAddForeignKey(\"orders_user_fk\", []string{\"user_id\"}, \"users\", " +
		"[]string{\"id\"}).\n" +
		"\t\tOnDelete(migr.ActionCascade).\n" +
		"\t\tOnUpdate(migr.ActionSetNull).\n" +
		"\t\tApply(ctx); err != nil {\n\t\treturn err\n\t}\n" +
		"\n" +
		"\tif err := s.Table(\"users\").\n" +
		"\t\tAlterColumn(\"note\").Type(migr.String(64)).NotNull().\n" +
		"\t\tWasType(migr.Text()).\n" +
		"\t\tWasNullable(true).\n" +
		"\t\tWasDefault(migr.Literal(\"\")).\n" +
		"\t\tApply(ctx); err != nil {\n\t\treturn err\n\t}\n" +
		"\n" +
		"\treturn s.Table(\"orders\").\n" +
		"\t\tDropForeignKey(\"orders_user_fk\").\n" +
		"\t\tWas(migr.ForeignKeyDef{Columns: []string{\"user_id\"}, " +
		"ReferencesTable: \"users\", ReferencesColumns: []string{\"id\"}, " +
		"OnDelete: migr.ActionCascade, OnUpdate: migr.ActionSetNull}).\n" +
		"\t\tApply(ctx)\n"

	if body != want {
		t.Errorf("what it renders is not what it should:\n%s", diffLines(body, want))
	}
}
