package typescript

import (
	"encoding/json"
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

// A renderer whose output does not typecheck is not a renderer, so this runs tsc over what it
// renders, against the published package by self-reference through the same exports map a consumer
// resolves -- which is what template.test.ts does for the template, one directory over.
//
// It is untagged and skips out loud, which is internal/runtime/node's own rule: the Node toolchain
// is a requirement make test does not have, and a tag would hide the test from golangci-lint and
// from CI's lint and test steps alike.

func TestWhatItRendersTypechecksAgainstThePublishedPackage(t *testing.T) {
	body, err := Render(sampleMigration(t), everyOperation(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	modules := filepath.Join(repoRoot(t), "sdk", "typescript", "node_modules")
	if _, err := os.Stat(filepath.Join(modules, "typescript")); err != nil {
		t.Skipf("sdk/typescript/node_modules/typescript is not installed, so tsc cannot run "+
			"over what this renders (install it with `make tools`): %v", err)
	}

	dir, err := os.MkdirTemp(modules, ".migr-rendered-")
	if err != nil {
		t.Fatalf("staging a directory inside node_modules: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	write(t, filepath.Join(dir, "20260727T153000_rendered.ts"), body)

	config, err := json.Marshal(map[string]any{
		"extends": "../../tsconfig.json",
		"include": []string{"*.ts"},
	})
	if err != nil {
		t.Fatalf("rendering a tsconfig: %v", err)
	}

	write(t, filepath.Join(dir, "tsconfig.json"), string(config))

	checked := exec.Command(filepath.Join(modules, "typescript", "bin", "tsc"),
		"--project", dir)

	if out, err := checked.CombinedOutput(); err != nil {
		t.Fatalf("what this renders does not typecheck: %v\n%s\n\nwhat was rendered:\n%s",
			err, out, body)
	}

	t.Log("The Go writer's output is held to being byte-exactly what gofmt writes, and this one is")
	t.Log("deliberately not held to prettier: gofmt has one canonical rendering, while prettier's")
	t.Log("line breaking is width-driven -- it joins a short chain onto one line and breaks a long")
	t.Log("one at its own column, so matching it means reproducing it. What is asserted instead is")
	t.Log("that this typechecks against the published package. A generated file lands in somebody")
	t.Log("else's project, where their own formatter is the one that will reformat it.")
}

func TestRenderingReadsAsTheChainTheTemplateShows(t *testing.T) {
	body, err := Render(sampleMigration(t), ir.Operations{
		ir.CreateTable{Table: "users", Columns: []ir.Column{
			{Name: "id", Type: typ(t, "uuid")},
			{Name: "nickname", Type: typ(t, "text"), Nullable: true, Default: ir.Literal("")},
		}, PrimaryKey: []string{"id"}},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, want := range []string{
		`await s.createTable("users")`,
		`.column("id", t.uuid())`,
		`.column("nickname", t.text()).null().default({ literal: "" })`,
		`.primaryKey("id")`,
		".apply();",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("what it renders does not read %q:\n%s", want, body)
		}
	}

	t.Log("The template shows exactly this shape, which is what makes a generated migration one an")
	t.Log("author can read and edit rather than one they would rewrite.")
}

func TestItRefusesTheOneOperationNoSourceExpresses(t *testing.T) {
	if _, err := Render(sampleMigration(t), ir.Operations{
		ir.Raw{Driver: "sqlite", SQL: "ANALYZE"},
	}); err == nil {
		t.Error("rendering engine SQL as a schema builder chain reported success")
	}
}

// everyOperation is one of each kind a differ emits, which is every kind but raw.
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

// typ parses one of these fixtures' logical types, which are all written out in this package.
func typ(t *testing.T, spelling string) schema.Type {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	return parsed
}

// typePointer returns a pointer to a parsed logical type, which an alteration states.
func typePointer(t *testing.T, spelling string) *schema.Type {
	t.Helper()

	parsed := typ(t, spelling)

	return &parsed
}

// pointerTo returns a pointer to a value, which is how an alteration states a changed attribute.
func pointerTo[T any](v T) *T { return &v }

// repoRoot finds the directory holding go.mod, which is where sdk/typescript sits beside.
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

// write stages one file of the throwaway project, failing the test rather than returning a reason.
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
