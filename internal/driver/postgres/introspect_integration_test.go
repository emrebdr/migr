//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
	conformance "github.com/emrebdr/migr/test/conformance/driver"
)

// introspect reads the snapshot back, failing the test rather than returning a reason.
func introspect(t *testing.T, session *pgConn) *schema.Snapshot {
	t.Helper()

	snapshot, err := session.Introspect(t.Context())
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}

	return snapshot
}

// tableIn returns one table of a snapshot, failing the test when the snapshot does not hold it.
func tableIn(t *testing.T, snapshot *schema.Snapshot, name string) schema.Table {
	t.Helper()

	table, held := snapshot.Table(name)
	if !held {
		t.Fatalf("the snapshot holds no table %q, only %s", name, describeTables(snapshot))
	}

	return table
}

// columnIn returns one column of a table, failing the test when the table does not hold it.
func columnIn(t *testing.T, table schema.Table, name string) schema.Column {
	t.Helper()

	column, held := table.Column(name)
	if !held {
		t.Fatalf("table %q holds no column %q", table.Name, name)
	}

	return column
}

// describeTables names what a snapshot holds, for a failure about something it does not.
func describeTables(snapshot *schema.Snapshot) string {
	names := make([]string, 0, len(snapshot.Tables))
	for _, table := range snapshot.Tables {
		names = append(names, table.Name)
	}

	return "[" + strings.Join(names, " ") + "]"
}

// scratchSession gives one test a schema of its own and a session pointed at it.
func scratchSession(t *testing.T, server int) *pgConn {
	t.Helper()

	dsn := dsnFor(servers[server].env, servers[server].dsn)

	return sessionIn(t, dsn, scratchSchema(t, dsn))
}

// eachMajor runs one case against every PostgreSQL this driver is held to.
func eachMajor(t *testing.T, run func(t *testing.T, session *pgConn)) {
	t.Helper()

	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			run(t, scratchSession(t, at))
		})
	}
}

func TestAnEmptySchemaIntrospectsAsAnEmptySnapshot(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		snapshot := introspect(t, session)

		if !snapshot.Empty() {
			t.Errorf("a schema with nothing in it reported %s", describeTables(snapshot))
		}
	})
}

func TestEveryLogicalTypeThisDriverEmitsIsReadBackExactly(t *testing.T) {
	spellings := []string{
		"string(255)", "text", "bool", "int16", "int32", "int64", "float32", "float64",
		"decimal(10,2)", "uuid", "json", "timestamp", "timestamptz", "date", "time", "binary",
		"array(int32)", "array(string(8))",
	}

	eachMajor(t, func(t *testing.T, session *pgConn) {
		columns := make([]ir.Column, 0, len(spellings))
		for i, spelling := range spellings {
			columns = append(columns, ir.Column{
				Name: fmt.Sprintf("c%02d", i), Type: probeType(t, spelling), Nullable: true,
			})
		}

		apply(t, session, ir.Operations{ir.CreateTable{Table: "types", Columns: columns}})

		table := tableIn(t, introspect(t, session), "types")

		for i, spelling := range spellings {
			read := columnIn(t, table, fmt.Sprintf("c%02d", i)).Type

			if !read.Exact() {
				t.Errorf("%s reads back %s (%s), and this engine's map is injective",
					spelling, read.Certainty(), read.Native())

				continue
			}

			if logical, _ := read.Logical(); logical.String() != spelling {
				t.Errorf("%s reads back as %s, through the native spelling %q",
					spelling, logical, read.Native())
			}
		}

		t.Log("All eighteen are exact here, and only because this engine refuses enum outright --")
		t.Log("the other two drivers each have collisions this one does not (ADR 0065).")
	})
}

func TestATypeNoLogicalTypeNamesIsReportedUnknownRatherThanGuessedAt(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		exec(t, session, `CREATE TABLE "exotic" ("vec" tsvector, "pt" point, "when" timestamp(3))`)

		table := tableIn(t, introspect(t, session), "exotic")

		for _, name := range []string{"vec", "pt", "when"} {
			read := columnIn(t, table, name).Type

			if read.Certainty() != schema.CertaintyUnknown {
				t.Errorf("column %q reads back as %s, want unknown", name, read)
			}

			if read.Native() == "" {
				t.Errorf("column %q reports no native spelling at all", name)
			}
		}

		t.Log("timestamp(3) is the interesting one: the base name is a spelling this driver emits")
		t.Log("and the precision is one no logical type carries, so reading it as timestamp would")
		t.Log("claim an exactness the type system cannot hold.")
	})
}

func TestADefaultIsReadBackAsTheLiteralOrTheFunctionItHolds(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		apply(t, session, ir.Operations{ir.CreateTable{Table: "defaults", Columns: []ir.Column{
			{Name: "plain", Type: probeType(t, "string(10)"), Nullable: true,
				Default: ir.Literal("x")},
			{Name: "quoted", Type: probeType(t, "string(10)"), Nullable: true,
				Default: ir.Literal("it's")},
			{Name: "numeric", Type: probeType(t, "int32"), Nullable: true,
				Default: ir.Literal("7")},
			{Name: "blank", Type: probeType(t, "string(10)"), Nullable: true,
				Default: ir.Literal("")},
			{Name: "flagged", Type: probeType(t, "bool"), Nullable: true,
				Default: ir.Literal("true")},
			{Name: "stamped", Type: probeType(t, "timestamp"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentTimestamp)},
			{Name: "dated", Type: probeType(t, "date"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentDate)},
			{Name: "timed", Type: probeType(t, "time"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentTime)},
			{Name: "identified", Type: probeType(t, "uuid"), Nullable: true,
				Default: ir.Call(ir.FuncUUID)},
			{Name: "none", Type: probeType(t, "int32"), Nullable: true},
		}}})

		table := tableIn(t, introspect(t, session), "defaults")

		for _, one := range []struct {
			column string
			want   *schema.ColumnDefault
		}{
			{"plain", &schema.ColumnDefault{Literal: literal("x")}},
			{"quoted", &schema.ColumnDefault{Literal: literal("it's")}},
			{"numeric", &schema.ColumnDefault{Literal: literal("7")}},
			{"blank", &schema.ColumnDefault{Literal: literal("")}},
			{"flagged", &schema.ColumnDefault{Literal: literal("true")}},
			{"stamped", &schema.ColumnDefault{Function: schema.FuncCurrentTimestamp}},
			{"dated", &schema.ColumnDefault{Function: schema.FuncCurrentDate}},
			{"timed", &schema.ColumnDefault{Function: schema.FuncCurrentTime}},
			{"identified", &schema.ColumnDefault{Function: schema.FuncUUID}},
			{"none", nil},
		} {
			read := columnIn(t, table, one.column).Default

			if !read.Equal(one.want) {
				t.Errorf("the default on %q reads back as %v, want %v", one.column, read, one.want)
			}
		}

		t.Log("This engine renders a string literal with a cast after it -- 'x'::character varying")
		t.Log("-- and a number with neither quotes nor cast, so stripping both is what makes a")
		t.Log("literal compare equal to the one SQLite records as 'x' and MySQL as x.")
	})
}

func TestASequenceDefaultIsEngineTextAndIsMarked(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		exec(t, session, `CREATE TABLE "counted" ("id" serial)`)

		column := columnIn(t, tableIn(t, introspect(t, session), "counted"), "id")

		if column.Default == nil || column.Default.Expression == "" {
			t.Fatalf("a serial column's default reads back as %v", column.Default)
		}

		if column.Beyond == nil || !strings.Contains(column.Beyond.Detail, "sequence") {
			t.Errorf("a serial column is marked %v", column.Beyond)
		}
	})
}

func TestAUniqueConstraintAndAUniqueIndexAreBothReportedAsConstraints(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		apply(t, session, ir.Operations{
			ir.CreateTable{Table: "users", Columns: []ir.Column{
				{Name: "id", Type: probeType(t, "int64")},
				{Name: "email", Type: probeType(t, "string(255)")},
				{Name: "handle", Type: probeType(t, "string(32)")},
				{Name: "nick", Type: probeType(t, "string(32)")},
			}, PrimaryKey: []string{"id"}},
			ir.AddUnique{Table: "users", Name: "uq_users_email", Columns: []string{"email"}},
			ir.AddIndex{Table: "users", Name: "idx_users_handle", Columns: []string{"handle"}},
		})

		exec(t, session, `CREATE UNIQUE INDEX "uq_users_nick" ON "users" ("nick")`)

		table := tableIn(t, introspect(t, session), "users")

		if got := uniqueNames(table); got != "uq_users_email uq_users_nick" {
			t.Errorf("the unique constraints read back as %q", got)
			t.Log("This engine can tell a unique constraint from a bare unique index, and the")
			t.Log("other two cannot, so both are reported as the one element ir.AddUnique creates.")
		}

		if len(table.Indexes) != 1 || table.Indexes[0].Name != "idx_users_handle" {
			t.Errorf("the indexes read back as %+v", table.Indexes)
		}

		if table.PrimaryKey == nil || strings.Join(table.PrimaryKey.Columns, ",") != "id" {
			t.Errorf("the primary key reads back as %+v", table.PrimaryKey)
		}
	})
}

// uniqueNames renders a table's unique constraints in canonical order, for one assertion.
func uniqueNames(table schema.Table) string {
	canonical := table.Canonical()

	names := make([]string, 0, len(canonical.Uniques))
	for _, unique := range canonical.Uniques {
		names = append(names, unique.Name)
	}

	return strings.Join(names, " ")
}

func TestTheIndexBackingAConstraintIsNotReportedAsAnIndexOfItsOwn(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		apply(t, session, ir.Operations{
			ir.CreateTable{Table: "parent", Columns: []ir.Column{
				{Name: "id", Type: probeType(t, "int64")},
			}, PrimaryKey: []string{"id"}},
			ir.CreateTable{Table: "child", Columns: []ir.Column{
				{Name: "id", Type: probeType(t, "int64")},
				{Name: "parent_id", Type: probeType(t, "int64"), Nullable: true},
			}, PrimaryKey: []string{"id"}},
			ir.AddForeignKey{Table: "child", Name: "fk_child_parent",
				Columns: []string{"parent_id"}, ReferencesTable: "parent",
				ReferencesColumns: []string{"id"}, OnDelete: ir.ActionCascade},
		})

		snapshot := introspect(t, session)

		for _, name := range []string{"parent", "child"} {
			table := tableIn(t, snapshot, name)

			if len(table.Indexes) != 0 {
				t.Errorf("table %q reports %+v, and every one of them backs a constraint",
					name, table.Indexes)
			}

			if len(table.Uniques) != 0 {
				t.Errorf("table %q reports %+v, and the only unique thing on it is its own key",
					name, table.Uniques)
				t.Log("A primary key's index is unique, so one reported as an index of its own")
				t.Log("lands here rather than beside the plain indexes.")
			}

			t.Log("Joining pg_constraint on conindid alone reports the parent's key twice: a")
			t.Log("foreign key's conindid names the index on the referenced side.")
		}

		key := tableIn(t, snapshot, "child").ForeignKeys
		if len(key) != 1 {
			t.Fatalf("the foreign keys read back as %+v", key)
		}

		if key[0].Name != "fk_child_parent" || key[0].OnDelete != schema.ActionCascade {
			t.Errorf("the key reads back as %+v", key[0])
		}

		if key[0].OnUpdate != schema.ActionNoAction {
			t.Errorf("an unnamed on_update reads back as %q, want no_action", key[0].OnUpdate)
		}
	})
}

func TestACheckCarriesTheConditionThisEngineWroteRatherThanTheOneAskedFor(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		apply(t, session, ir.Operations{
			ir.CreateTable{Table: "bounded", Columns: []ir.Column{
				{Name: "n", Type: probeType(t, "int32"), Nullable: true},
			}},
			ir.AddCheck{Table: "bounded", Name: "ck_bounded_n", Expression: `"n" > 0`},
		})

		table := tableIn(t, introspect(t, session), "bounded")

		if len(table.Checks) != 1 {
			t.Fatalf("the checks read back as %+v", table.Checks)
		}

		if table.Checks[0].Name != "ck_bounded_n" {
			t.Errorf("the check reads back named %q", table.Checks[0].Name)
		}

		if strings.HasPrefix(table.Checks[0].Expression, "CHECK") {
			t.Errorf("the condition reads back as %q, which is the whole rendering rather than "+
				"the condition inside it", table.Checks[0].Expression)
		}

		if !strings.Contains(table.Checks[0].Expression, "> 0") {
			t.Errorf("the condition reads back as %q", table.Checks[0].Expression)
		}

		t.Log("ADR 0065: a check expression is engine text. This server re-renders it, so the")
		t.Log("condition that comes back is not the one the migration asked for -- which is why")
		t.Log("nothing compares one across engines.")
	})
}

func TestEverythingTheOperationSetCannotExpressIsMarkedRatherThanOmitted(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		for _, statement := range []string{
			`CREATE TABLE "beyond" (
				"id" bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
				"plain" text,
				"gen" integer GENERATED ALWAYS AS (1 + 1) STORED,
				"shouty" text COLLATE "C"
			)`,
			`CREATE INDEX "idx_partial" ON "beyond" ("plain") WHERE "plain" IS NOT NULL`,
			`CREATE INDEX "idx_desc" ON "beyond" ("plain" DESC)`,
			`CREATE INDEX "idx_expr" ON "beyond" (lower("plain"))`,
			`CREATE TABLE "deferred" ("id" bigint PRIMARY KEY,
				"p" bigint REFERENCES "beyond" ("id") DEFERRABLE INITIALLY DEFERRED)`,
			`CREATE MATERIALIZED VIEW "counted" AS SELECT "id" FROM "beyond"`,
		} {
			exec(t, session, statement)
		}

		snapshot := introspect(t, session)

		findings := make(map[string]string, 8)
		for _, found := range schema.Unrepresentable(*snapshot) {
			findings[string(found.Element)+":"+found.Object] = found.Detail
		}

		for _, one := range []struct {
			at   string
			says string
		}{
			{"column:id", "identity"},
			{"column:gen", "generated"},
			{"column:shouty", "collation"},
			{"index:idx_partial", "partial index"},
			{"index:idx_desc", "descending order"},
			{"view:counted", "materialized"},
		} {
			detail, found := findings[one.at]
			if !found {
				t.Errorf("%s is not marked at all, so a differ would act on it", one.at)

				continue
			}

			if !strings.Contains(detail, one.says) {
				t.Errorf("%s is marked %q, which does not say %q", one.at, detail, one.says)
			}
		}

		carrier := tableIn(t, snapshot, "beyond")
		if carrier.Beyond == nil || !strings.Contains(carrier.Beyond.Detail, "idx_expr") {
			t.Errorf("an index over an expression is not named on its table: %v", carrier.Beyond)
		}

		for _, key := range tableIn(t, snapshot, "deferred").ForeignKeys {
			if key.Beyond == nil || !strings.Contains(key.Beyond.Detail, "DEFERRABLE") {
				t.Errorf("a deferrable foreign key is marked %v", key.Beyond)
			}
		}
	})
}

func TestATableWithEveryColumnDroppedIsStillInTheSnapshot(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		only := ir.Column{Name: "gone", Type: probeType(t, "int32"), Nullable: true}

		apply(t, session, ir.Operations{
			ir.CreateTable{Table: "emptied", Columns: []ir.Column{only}},
			ir.DropColumn{Table: "emptied", Column: only.Name, From: &only},
		})

		table := tableIn(t, introspect(t, session), "emptied")

		if len(table.Columns) != 0 {
			t.Errorf("the emptied table reports %d columns", len(table.Columns))
		}

		t.Log("ir.TableDef requires a column and this engine permits a relation with none, which")
		t.Log("is why schema.Table accepts it -- and why the digest this supersedes had to list")
		t.Log("relations as well as describe them.")
	})
}

func TestTheSnapshotHoldsNeitherTheHistoryTableNorItsIndexes(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		if err := session.History().Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		if err := session.Seeds().Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure the seed history: %v", err)
		}

		apply(t, session, ir.Operations{ir.CreateTable{Table: "app", Columns: []ir.Column{
			{Name: "id", Type: probeType(t, "int64")},
		}, PrimaryKey: []string{"id"}}})

		snapshot := introspect(t, session)

		for _, name := range []string{session.history.table, session.seeds.table} {
			if _, held := snapshot.Table(name); held {
				t.Errorf("the snapshot holds %q, which a differ would propose dropping", name)
			}
		}

		if _, held := snapshot.Table("app"); !held {
			t.Error("the snapshot lost the table a migration created")
		}
	})
}

func TestASnapshotCanBeTakenInsideAnOpenTransaction(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		apply(t, session, ir.Operations{ir.CreateTable{Table: "app", Columns: []ir.Column{
			{Name: "id", Type: probeType(t, "int64")},
		}}})

		var inside *schema.Snapshot

		err := session.Transact(t.Context(), func(ctx context.Context) error {
			read, err := session.Introspect(ctx)
			inside = read

			return err
		})
		if err != nil {
			t.Fatalf("introspecting inside a transaction: %v", err)
		}

		if !inside.Equal(*introspect(t, session)) {
			t.Error("the snapshot read inside a transaction differs from the one read outside it")
		}
	})
}

func TestADriverReadsItsOwnCompiledSchemaBackDeterministically(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		apply(t, session, roundTripOperations(t))

		before := introspect(t, session)

		if !before.Equal(*introspect(t, session)) {
			t.Error("two snapshots of one unchanged schema compare unequal")
			t.Log("Introspection is a planning path, so it must be deterministic (invariant #8).")
		}
	})
}

// roundTripOperations builds one schema exercising the elements a snapshot holds.
func roundTripOperations(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.CreateTable{Table: "users", Columns: []ir.Column{
			{Name: "id", Type: probeType(t, "int64")},
			{Name: "email", Type: probeType(t, "string(255)")},
			{Name: "note", Type: probeType(t, "text"), Nullable: true},
		}, PrimaryKey: []string{"id"}},
		ir.AddUnique{Table: "users", Name: "uq_users_email", Columns: []string{"email"}},
		ir.AddIndex{Table: "users", Name: "idx_users_note", Columns: []string{"note"}},
		ir.AddCheck{Table: "users", Name: "ck_users_email", Expression: `"email" <> ''`},
		ir.CreateTable{Table: "orders", Columns: []ir.Column{
			{Name: "id", Type: probeType(t, "int64")},
			{Name: "user_id", Type: probeType(t, "int64"), Nullable: true},
		}, PrimaryKey: []string{"id"}},
		ir.AddForeignKey{Table: "orders", Name: "fk_orders_user", Columns: []string{"user_id"},
			ReferencesTable: "users", ReferencesColumns: []string{"id"},
			OnDelete: ir.ActionCascade},
		ir.CreateView{View: "emails", Query: `SELECT "email" FROM "users"`},
	}
}

// exec executes one statement no operation can express, which is what a probe fixture needs.
func exec(t *testing.T, session *pgConn, statement string) {
	t.Helper()

	if _, err := session.conn.Exec(t.Context(), statement); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

// text returns a pointer to a default's value, which is what distinguishes empty from absent.
func literal(v string) *string {
	return &v
}

func TestTheSnapshotConformanceSuitePasses(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		conformance.Snapshot{
			Driver: newDriver(t, driver.Options{}),
			Conn:   session,
			Prefix: "migr_snap",
		}.Run(t)
	})

	t.Log("One expected snapshot, and every engine is held to it: SQLite runs this untagged and")
	t.Log("MySQL runs it against both variants, so the cross-engine claim is four servers deep")
	t.Log("without any test opening two connections at once -- which invariant #1 forbids anyway.")
}

func TestTheDiffConformanceSuitePasses(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		conformance.Diff{
			Driver: newDriver(t, driver.Options{}),
			Conn:   session,
			Prefix: "migr_diff",
		}.Run(t)
	})

	t.Log("An index name is namespace-scoped on this engine, which is what makes the diff's own")
	t.Log("rule -- every drop before every create -- something a server can disagree with.")
}
