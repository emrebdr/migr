package migr

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

// built is one operation expressed twice: as a builder chain, and constructed by hand.
type built struct {
	chain func(*Schema) *builder
	want  Operation
}

// chains expresses every v1 operation both ways, which is what this task's done-when compares.
var chains = map[Kind]built{
	KindCreateTable: {
		chain: func(s *Schema) *builder {
			return s.CreateTable("users").
				Column("id", UUID()).
				Column("email", String(255)).
				Column("created_at", TimestampTZ()).Null().Default(Call(FuncCurrentTimestamp)).
				PrimaryKey("id").build
		},
		want: CreateTable{
			Table: "users",
			Columns: []Column{
				{Name: "id", Type: UUID()},
				{Name: "email", Type: String(255)},
				{
					Name: "created_at", Type: TimestampTZ(),
					Nullable: true, Default: Call(FuncCurrentTimestamp),
				},
			},
			PrimaryKey: []string{"id"},
		},
	},
	KindDropTable: {
		chain: func(s *Schema) *builder {
			return s.Table("users").
				Drop().Was(TableDef{
				Columns:    []Column{{Name: "id", Type: UUID()}},
				PrimaryKey: []string{"id"},
			}).build
		},
		want: DropTable{Table: "users", From: &TableDef{
			Columns:    []Column{{Name: "id", Type: UUID()}},
			PrimaryKey: []string{"id"},
		}},
	},
	KindRenameTable: {
		chain: func(s *Schema) *builder { return s.Table("users").RenameTo("people").build },
		want:  RenameTable{From: "users", To: "people"},
	},
	KindAddColumn: {
		chain: func(s *Schema) *builder {
			return s.Table("users").AddColumn("email", String(255)).NotNull().build
		},
		want: AddColumn{Table: "users", Column: Column{Name: "email", Type: String(255)}},
	},
	KindDropColumn: {
		chain: func(s *Schema) *builder {
			return s.Table("users").
				DropColumn("email").Was(Column{Name: "email", Type: String(255)}).build
		},
		want: DropColumn{
			Table: "users", Column: "email",
			From: &Column{Name: "email", Type: String(255)},
		},
	},
	KindRenameColumn: {
		chain: func(s *Schema) *builder {
			return s.Table("users").RenameColumn("email", "address").build
		},
		want: RenameColumn{Table: "users", From: "email", To: "address"},
	},
	KindAlterColumn: {
		chain: func(s *Schema) *builder {
			return s.Table("users").
				AlterColumn("email").Type(String(512)).WasType(String(255)).build
		},
		want: AlterColumn{
			Table: "users", Column: "email",
			Type: pointerTo(String(512)),
			From: &ColumnState{Type: pointerTo(String(255))},
		},
	},
	KindAddIndex: {
		chain: func(s *Schema) *builder {
			return s.Table("users").AddIndex("idx_users_email", "email").build
		},
		want: AddIndex{Table: "users", Name: "idx_users_email", Columns: []string{"email"}},
	},
	KindDropIndex: {
		chain: func(s *Schema) *builder {
			return s.Table("users").DropIndex("idx_users_email").Was(Columns("email")).build
		},
		want: DropIndex{
			Table: "users", Name: "idx_users_email",
			From: &ColumnsDef{Columns: []string{"email"}},
		},
	},
	KindAddForeignKey: {
		chain: func(s *Schema) *builder {
			return s.Table("posts").
				AddForeignKey("fk_posts_author", []string{"author_id"}, "users", []string{"id"}).
				OnDelete(ActionCascade).build
		},
		want: AddForeignKey{
			Table: "posts", Name: "fk_posts_author", Columns: []string{"author_id"},
			ReferencesTable: "users", ReferencesColumns: []string{"id"},
			OnDelete: ActionCascade,
		},
	},
	KindDropForeignKey: {
		chain: func(s *Schema) *builder {
			return s.Table("posts").
				DropForeignKey("fk_posts_author").Was(ForeignKeyDef{
				Columns:         []string{"author_id"},
				ReferencesTable: "users", ReferencesColumns: []string{"id"},
				OnDelete: ActionCascade,
			}).build
		},
		want: DropForeignKey{
			Table: "posts", Name: "fk_posts_author",
			From: &ForeignKeyDef{
				Columns:         []string{"author_id"},
				ReferencesTable: "users", ReferencesColumns: []string{"id"},
				OnDelete: ActionCascade,
			},
		},
	},
	KindAddUnique: {
		chain: func(s *Schema) *builder {
			return s.Table("users").AddUnique("uq_users_email", "email").build
		},
		want: AddUnique{Table: "users", Name: "uq_users_email", Columns: []string{"email"}},
	},
	KindDropUnique: {
		chain: func(s *Schema) *builder {
			return s.Table("users").DropUnique("uq_users_email").Was(Columns("email")).build
		},
		want: DropUnique{
			Table: "users", Name: "uq_users_email",
			From: &ColumnsDef{Columns: []string{"email"}},
		},
	},
	KindAddCheck: {
		chain: func(s *Schema) *builder {
			return s.Table("orders").AddCheck("ck_orders_total", "total >= 0").build
		},
		want: AddCheck{Table: "orders", Name: "ck_orders_total", Expression: "total >= 0"},
	},
	KindDropCheck: {
		chain: func(s *Schema) *builder {
			return s.Table("orders").
				DropCheck("ck_orders_total").Was(CheckDef{Expression: "total >= 0"}).build
		},
		want: DropCheck{
			Table: "orders", Name: "ck_orders_total",
			From: &CheckDef{Expression: "total >= 0"},
		},
	},
	KindAddPrimaryKey: {
		chain: func(s *Schema) *builder {
			return s.Table("users").AddPrimaryKey("pk_users", "id").build
		},
		want: AddPrimaryKey{Table: "users", Name: "pk_users", Columns: []string{"id"}},
	},
	KindDropPrimaryKey: {
		chain: func(s *Schema) *builder {
			return s.Table("users").DropPrimaryKey("pk_users").Was(Columns("id")).build
		},
		want: DropPrimaryKey{
			Table: "users", Name: "pk_users",
			From: &ColumnsDef{Columns: []string{"id"}},
		},
	},
	KindCreateView: {
		chain: func(s *Schema) *builder {
			return s.View("active_users").
				Create("SELECT id FROM users WHERE active").build
		},
		want: CreateView{View: "active_users", Query: "SELECT id FROM users WHERE active"},
	},
	KindDropView: {
		chain: func(s *Schema) *builder {
			return s.View("active_users").
				Drop().Was("SELECT id FROM users WHERE active").build
		},
		want: DropView{
			View: "active_users",
			From: &ViewDef{Query: "SELECT id FROM users WHERE active"},
		},
	},
	KindRaw: {
		chain: func(s *Schema) *builder {
			return s.Raw("postgres", "CREATE EXTENSION pgcrypto").build
		},
		want: Raw{Driver: "postgres", SQL: "CREATE EXTENSION pgcrypto"},
	},
	KindDataCopy: {
		chain: func(s *Schema) *builder {
			return s.Table("users").CopyInto("users_new",
				CopyColumn{From: "id", To: "id"}, CopyColumn{From: "name", To: "full_name"}).build
		},
		want: DataCopy{From: "users", To: "users_new", Columns: []CopyColumn{
			{From: "id", To: "id"}, {From: "name", To: "full_name"},
		}},
	},
	KindDataBackfill: {
		chain: func(s *Schema) *builder {
			return s.Table("users").Backfill("plan", String(16), Literal("free")).build
		},
		want: DataBackfill{Table: "users", Column: "plan", Type: String(16), Value: Literal("free")},
	},
	KindDataTransform: {
		chain: func(s *Schema) *builder {
			return s.Table("users").Transform("email_key", String(255), "email", TransformLower).build
		},
		want: DataTransform{Table: "users", Column: "email_key", Type: String(255), From: "email",
			Function: TransformLower},
	},
}

func TestTheBuilderEmitsWhatTheHandConstructedOperationDoes(t *testing.T) {
	for _, kind := range Kinds() {
		expressed, covered := chains[kind]
		if !covered {
			t.Errorf("%q has no builder chain, so the builder does not cover the operation set", kind)

			continue
		}

		t.Run(string(kind), func(t *testing.T) {
			if expressed.want.Kind() != kind {
				t.Fatalf("the hand-constructed operation is a %q", expressed.want.Kind())
			}

			assertSameDocument(t, kind, expressed)
		})
	}

	for kind := range chains {
		if !kind.Valid() {
			t.Errorf("%q is chained but is not part of the v1 operation set", kind)
		}
	}
}

// assertSameDocument reports whether one chain and one hand-constructed operation agree byte for byte.
func assertSameDocument(t *testing.T, kind Kind, expressed built) {
	t.Helper()

	ops := expressed.chain(newSchema(nil, version, Up, ModeApply)).ops
	if len(ops) != 1 {
		t.Fatalf("the chain built %d operations, and one kind is one operation", len(ops))
	}

	if ops[0].Kind() != kind {
		t.Fatalf("the chain built a %q", ops[0].Kind())
	}

	got, err := documentOf(ops[0])
	if err != nil {
		t.Fatalf("the chain built an operation this SDK refuses: %v", err)
	}

	want, err := documentOf(expressed.want)
	if err != nil {
		t.Fatalf("the hand-constructed operation is refused: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("the builder and the hand-constructed operation differ\nchain %s\n hand %s", got, want)
	}
}

func TestAnUnqualifiedColumnIsNotNull(t *testing.T) {
	s := newSchema(nil, version, Up, ModeApply)

	added := s.Table("users").AddColumn("email", String(255)).build.ops[0]
	declared := s.CreateTable("people").Column("email", String(255)).build.ops[0]

	for _, op := range []Operation{added, declared} {
		document, err := documentOf(op)
		if err != nil {
			t.Fatalf("%s: %v", op.Kind(), err)
		}

		if !strings.Contains(string(document), `"nullable":false`) {
			t.Errorf("%s made an unqualified column nullable: %s", op.Kind(), document)
		}
	}

	t.Log("nullable is always emitted, so every producer chooses what an unqualified column")
	t.Log("means, and two choosing differently emit different bytes for one schema change.")
	t.Log("M7-T3 mirrors this builder and M7-T7 compares the two byte for byte, so the choice")
	t.Log("is stated in the document both answer to rather than in either SDK.")

	stated := sectionText(readSpecLines(t), "### 7.2 Conventions")
	if !strings.Contains(stated, "NOT NULL unless the migration says otherwise") {
		t.Errorf("%s §7.2 no longer states which way an unqualified column goes", specPath)
	}
}

func TestANewTableStatesItsKeyInlineAndAnExistingOneStatesItAsAnOperation(t *testing.T) {
	s := newSchema(nil, version, Up, ModeApply)

	created := s.CreateTable("users").Column("id", UUID()).PrimaryKey("id").build
	keyed := s.Table("people").AddPrimaryKey("pk_people", "id").build

	if len(created.ops) != 1 || created.ops[0].Kind() != KindCreateTable {
		t.Fatalf("creating a table with a key emitted %d operations", len(created.ops))
	}

	if keyed.ops[0].Kind() != KindAddPrimaryKey {
		t.Errorf("keying an existing table emitted a %q", keyed.ops[0].Kind())
	}

	t.Log("docs/ir.md §7.2: both spellings are valid, so nothing rejects the wrong choice --")
	t.Log("but two producers disagreeing emit different bytes for one schema change. Making")
	t.Log("them different calls is how the builder settles the convention structurally.")
}

func TestReplacingAViewRecordsTheDefinitionItOverwrote(t *testing.T) {
	s := newSchema(nil, version, Up, ModeApply)

	ops := s.View("active_users").
		Replace("SELECT id FROM users WHERE active").
		Was("SELECT id FROM users").build.ops

	replaced, ok := ops[0].(*CreateView)
	if !ok {
		t.Fatalf("replacing a view built a %T", ops[0])
	}

	if !replaced.OrReplace {
		t.Error("Replace built a create_view that does not replace")
	}

	if replaced.From == nil || replaced.From.Query != "SELECT id FROM users" {
		t.Errorf("the overwritten definition was not recorded: %+v", replaced.From)
	}
}

func TestAChangeAfterARenameNamesTheTableByItsNewName(t *testing.T) {
	s := newSchema(nil, version, Up, ModeApply)

	ops := s.Table("users").
		RenameTo("people").
		AddColumn("note", Text()).
		build.ops

	added, ok := ops[1].(*AddColumn)
	if !ok {
		t.Fatalf("the chain built a %T after the rename", ops[1])
	}

	if added.Table != "people" {
		t.Errorf("the column was added to %q, and after a rename the table is called %q",
			added.Table, "people")
	}

	t.Log("A chain that kept naming the old table would emit a migration the engine compiles and")
	t.Log("the database refuses -- and the author wrote nothing wrong.")
}

func TestRecordingTwoThingsAboutAColumnKeepsBoth(t *testing.T) {
	s := newSchema(nil, version, Up, ModeApply)

	ops := s.Table("users").
		AlterColumn("status").Default(Literal("draft")).
		WasType(String(16)).WasNoDefault().build.ops

	altered, ok := ops[0].(*AlterColumn)
	if !ok {
		t.Fatalf("altering a column built a %T", ops[0])
	}

	if altered.From == nil || altered.From.Type == nil || !altered.From.NoDefault {
		t.Errorf("recording the prior state twice kept only part of it: %+v", altered.From)
	}
}

func TestApplyingDrainsTheChainSoApplyingTwiceDoesNotEmitTwice(t *testing.T) {
	sent := 0
	s := newSchema(counting{recorded: &sent}, version, Up, ModeApply)

	users := s.Table("users").AddColumn("email", String(255)).Table

	if err := users.Apply(t.Context()); err != nil {
		t.Fatalf("applying: %v", err)
	}

	if err := users.Apply(t.Context()); err != nil {
		t.Fatalf("applying an emptied chain: %v", err)
	}

	if sent != 1 {
		t.Errorf("one column reached the host %d times", sent)
	}
}

func TestAnInvalidTypeIsReportedAtTheEmitNamingTheOperation(t *testing.T) {
	s := newSchema(counting{}, version, Up, ModeApply)

	err := s.Table("users").AddColumn("email", String(0)).Apply(t.Context())
	if err == nil {
		t.Fatal("a column with no usable type was emitted")
	}

	if !errors.Is(err, ErrInvalidOperation) {
		t.Errorf("the failure is not an invalid operation: %v", err)
	}

	for _, part := range []string{"add_column", `"email"`, "at least 1"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the failure does not name %s: %v", part, err)
		}
	}

	t.Log("A constructor returning (Type, error) could not be inlined into a chain, so the")
	t.Log("reason travels inside the Type and surfaces here, where the builder's own errors do.")
}

func TestTheOperationCodecLeavesAloneTheCharactersTheDocumentDoes(t *testing.T) {
	document, err := documentOf(AddCheck{
		Table: "orders", Name: "ck_orders_total", Expression: "total >= 0 & paid <= total",
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	t.Log("docs/ir.md §3.3: <, > and & are not escaped, because a check expression such as")
	t.Log("total >= 0 must produce identical bytes from every SDK and language defaults disagree.")

	for _, unescaped := range []string{">", "<", "&"} {
		if !strings.Contains(string(document), unescaped) {
			t.Errorf("the codec escaped %s: %s", unescaped, document)
		}
	}
}

func TestTheOperationCodecEmitsTheTwoSeparatorsAsUTF8(t *testing.T) {
	query := "SELECT id, 98 AS rank" + string(lineSeparator) + "FROM users" +
		string(paragraphSeparator) + "WHERE active"

	document, err := documentOf(CreateView{View: "active_users", Query: query})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	if bytes.Contains(document, separatorEscape) {
		t.Errorf("the codec escaped the separators: %s", document)
		t.Log("docs/ir.md §3.3: U+2028 and U+2029 are emitted as UTF-8 like any other")
		t.Log("non-ASCII character. encoding/json escapes both whatever SetEscapeHTML is")
		t.Log("told, and JSON.stringify escapes neither, so this SDK undoes it.")
	}

	if !bytes.Contains(document, []byte(query)) {
		t.Errorf("the query should survive verbatim, got %s", document)
		t.Log("The digits are load-bearing: a scanner that reads an escape by its last byte")
		t.Log("alone, rather than by the five before it, rewrites any string holding an 8 or")
		t.Log("a 9 six bytes in -- and a query with neither would never notice.")
	}
}

func TestTheOperationCodecLeavesAnAuthoredEscapeSequenceAlone(t *testing.T) {
	written := "SELECT id\\u2028FROM users"

	document, err := documentOf(CreateView{View: "active_users", Query: written})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	decoded, err := decodeOp[CreateView](document)
	if err != nil {
		t.Fatalf("decoding what the codec emitted: %v", err)
	}

	if view, ok := decoded.(CreateView); !ok || view.Query != written {
		t.Errorf("a query holding the escape as text did not round-trip: %s", document)
		t.Log("Go writes an authored backslash doubled, so a ReplaceAll over the output would")
		t.Log("match the tail of that and leave a stray backslash before a raw separator --")
		t.Log("which is neither the document the author wrote nor valid JSON.")
	}
}

func TestAChangeBuiltAndNeverAppliedFailsTheMigration(t *testing.T) {
	staged(t, map[string]Migration{file: forgetful{}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	answer := e.answer(id, func(m *wire.Message) *wire.Message { return recorded(t, m) })

	if slug := slugOf(t, answer); slug != "migration_failed" {
		t.Fatalf("a migration that emitted nothing succeeded with %q", slug)
	}

	if !strings.Contains(answer.Error.Message, `table "users"`) {
		t.Errorf("the failure does not name what was never applied: %v", answer.Error)
	}

	t.Log("A chain that is built and never sent emits nothing, and a migration that reports")
	t.Log("success having changed nothing is the failure docs/ir.md §3.4 exists to prevent.")
}

func TestAChangeNothingWasAddedToIsNotAFailure(t *testing.T) {
	staged(t, map[string]Migration{file: undecided{}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	answer := e.answer(id, func(m *wire.Message) *wire.Message { return recorded(t, m) })

	if answer.Error != nil {
		t.Errorf("a branch that opened a table and added nothing to it failed: %v", answer.Error)
	}
}

func TestAMigrationThatFailsIsNotAlsoBlamedForWhatItLeftUnapplied(t *testing.T) {
	staged(t, map[string]Migration{file: forgetful{fail: errMigration}})

	e := dial(t)
	e.greet()

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	answer := e.answer(id, func(m *wire.Message) *wire.Message { return recorded(t, m) })

	if !strings.Contains(answer.Error.Message, errMigration.Error()) {
		t.Errorf("the failure reported is not the one the migration returned: %v", answer.Error)
	}
}

// counting is a host that records how many callbacks were made and answers each one emptily.
type counting struct {
	recorded *int
}

// Call answers a callback with what the host would have recorded, counting the apply calls.
func (c counting) Call(_ context.Context, method string, params, target any) error {
	if method != wire.HostSchemaApply {
		return nil
	}

	if c.recorded != nil {
		*c.recorded++
	}

	sent, ok := params.(wire.SchemaApplyParams)
	result, answered := target.(*wire.SchemaApplyResult)

	if ok && answered {
		result.Recorded = len(sent.Operations)
	}

	return nil
}

// Notify accepts a notification, which nothing answers and nothing waits on.
func (c counting) Notify(string, any) error {
	return nil
}

// forgetful is a migration that builds a change to a table and returns without applying it.
type forgetful struct {
	// fail is what Up returns, nil when it succeeds having applied nothing.
	fail error
}

// Up builds three column changes and never sends them.
func (m forgetful) Up(_ context.Context, s *Schema) error {
	s.Table("users").
		AddColumn("email", String(255)).
		AddColumn("bio", Text()).Null().
		AddIndex("idx_users_email", "email")

	return m.fail
}

// undecided is a migration that opens a table on a branch it does not take.
type undecided struct{}

// Up opens a change to a table, adds nothing to it, and emits an operation elsewhere.
func (m undecided) Up(ctx context.Context, s *Schema) error {
	s.Table("users")

	return s.Raw("postgres", "CREATE EXTENSION pgcrypto").Apply(ctx)
}

// pointerTo returns a pointer to a value, which the optional IR fields are declared as.
func pointerTo[T any](v T) *T {
	return &v
}
