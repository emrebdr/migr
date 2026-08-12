package sql

import (
	"slices"
	"testing"
)

func TestStatementsSplitsOnTheSemicolonsNothingHides(t *testing.T) {
	tests := []struct {
		name    string
		section string
		want    []string
	}{
		{
			name:    "nothing at all",
			section: "",
		},
		{
			name:    "whitespace only",
			section: "\n\n   \t\n",
		},
		{
			name:    "comments only",
			section: "-- write the undo here\n/* and here */\n",
		},
		{
			name:    "one statement with no terminator",
			section: "CREATE TABLE users (id bigint)",
			want:    []string{"CREATE TABLE users (id bigint)"},
		},
		{
			name:    "one statement with a terminator",
			section: "CREATE TABLE users (id bigint);\n",
			want:    []string{"CREATE TABLE users (id bigint);"},
		},
		{
			name:    "two statements",
			section: "CREATE TABLE users (id bigint);\nCREATE INDEX i ON users (id);\n",
			want: []string{
				"CREATE TABLE users (id bigint);",
				"CREATE INDEX i ON users (id);",
			},
		},
		{
			name:    "a trailing semicolon adds no empty statement",
			section: "DROP TABLE users;;\n;\n",
			want:    []string{"DROP TABLE users;"},
		},
		{
			name:    "a leading comment belongs to the statement it introduces",
			section: "-- the users table\nCREATE TABLE users (id bigint);\n",
			want:    []string{"-- the users table\nCREATE TABLE users (id bigint);"},
		},
		{
			name:    "a semicolon inside a line comment",
			section: "CREATE TABLE users (id bigint) -- a; b\n;\n",
			want:    []string{"CREATE TABLE users (id bigint) -- a; b\n;"},
		},
		{
			name:    "a semicolon inside a block comment",
			section: "CREATE TABLE users (/* a; b */ id bigint);",
			want:    []string{"CREATE TABLE users (/* a; b */ id bigint);"},
		},
		{
			name:    "a block comment that nests, as PostgreSQL allows",
			section: "SELECT 1 /* outer /* inner; */ still; */;",
			want:    []string{"SELECT 1 /* outer /* inner; */ still; */;"},
		},
		{
			name:    "a semicolon inside a string",
			section: "INSERT INTO t VALUES ('a;b');",
			want:    []string{"INSERT INTO t VALUES ('a;b');"},
		},
		{
			name:    "a doubled quote inside a string",
			section: "INSERT INTO t VALUES ('it''s; fine');\nSELECT 1;",
			want: []string{
				"INSERT INTO t VALUES ('it''s; fine');",
				"SELECT 1;",
			},
		},
		{
			name:    "a semicolon inside a quoted identifier",
			section: `ALTER TABLE "odd;name" ADD COLUMN a int;`,
			want:    []string{`ALTER TABLE "odd;name" ADD COLUMN a int;`},
		},
		{
			name:    "a dollar-quoted body",
			section: "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql;",
			want: []string{
				"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql;",
			},
		},
		{
			name:    "a tagged dollar-quoted body",
			section: "CREATE FUNCTION f() RETURNS int AS $body$ SELECT 1; $body$ LANGUAGE sql;\nSELECT 2;",
			want: []string{
				"CREATE FUNCTION f() RETURNS int AS $body$ SELECT 1; $body$ LANGUAGE sql;",
				"SELECT 2;",
			},
		},
		{
			name:    "adjacent placeholders are not a dollar quote",
			section: "SELECT $1$2 FROM t;\nSELECT 2;",
			want: []string{
				"SELECT $1$2 FROM t;",
				"SELECT 2;",
			},
		},
		{
			name:    "an empty string literal",
			section: "INSERT INTO t VALUES ('');\nSELECT 2;",
			want: []string{
				"INSERT INTO t VALUES ('');",
				"SELECT 2;",
			},
		},
		{
			name:    "a placeholder is not a dollar quote",
			section: "INSERT INTO t VALUES ($1);\nSELECT 2;",
			want: []string{
				"INSERT INTO t VALUES ($1);",
				"SELECT 2;",
			},
		},
		{
			name:    "an unterminated string swallows the rest rather than splitting it",
			section: "INSERT INTO t VALUES ('unclosed; SELECT 1;",
			want:    []string{"INSERT INTO t VALUES ('unclosed; SELECT 1;"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Statements(tt.section); !slices.Equal(got, tt.want) {
				t.Errorf("Statements() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Log("Splitting is a lexical property of the file, not of the engine. The one case that")
	t.Log("would cost data rather than tidiness is the dollar-quoted body: cut at the first")
	t.Log("semicolon inside it, a PL/pgSQL function becomes two statements that are both invalid.")
}

func TestACommentOnlyChunkIsNotAStatement(t *testing.T) {
	got := Statements("SELECT 1;\n-- nothing follows this\n")

	if len(got) != 1 {
		t.Errorf("Statements() = %q, want only the statement that runs", got)
		t.Log("A trailing comment compiled to a raw operation would be a statement migr counts,")
		t.Log("records a position for, and offers to resume from, having changed nothing.")
	}
}
