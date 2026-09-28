package sqlite

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// split takes a CREATE TABLE apart for a test, failing rather than returning a reason.
func split(t *testing.T, statement string) tableDefinition {
	t.Helper()

	d, err := splitTable("users", statement)
	if err != nil {
		t.Fatalf("splitTable(%s): %v", statement, err)
	}

	return d
}

func TestAColumnListIsSplitOnItsOwnCommasAndNoOthers(t *testing.T) {
	cases := map[string]struct {
		statement string
		want      []string
	}{
		"plain": {
			`CREATE TABLE users (id integer, email text)`,
			[]string{`id integer`, `email text`},
		},
		"a comma inside a type": {
			`CREATE TABLE users (id integer, total decimal(19,4))`,
			[]string{`id integer`, `total decimal(19,4)`},
		},
		"a comma inside a quoted name": {
			`CREATE TABLE users ("a,b" text, c text)`,
			[]string{`"a,b" text`, `c text`},
		},
		"a comma inside a bracketed name": {
			`CREATE TABLE users ([a,b] text, c text)`,
			[]string{`[a,b] text`, `c text`},
		},
		"a comma inside a backticked name": {
			"CREATE TABLE users (`a,b` text, c text)",
			[]string{"`a,b` text", `c text`},
		},
		"a comma inside a string literal": {
			`CREATE TABLE users (a text DEFAULT 'x,y', b text)`,
			[]string{`a text DEFAULT 'x,y'`, `b text`},
		},
		"a comma inside a check": {
			`CREATE TABLE users (a text, CHECK (a IN ('x', 'y')))`,
			[]string{`a text`, `CHECK (a IN ('x', 'y'))`},
		},
		"nested parentheses": {
			`CREATE TABLE users (a text, CHECK (length(substr(a, 1, 2)) > 0))`,
			[]string{`a text`, `CHECK (length(substr(a, 1, 2)) > 0)`},
		},
		"a doubled quote inside a name": {
			`CREATE TABLE users ("we""ird,name" text, b text)`,
			[]string{`"we""ird,name" text`, `b text`},
		},
		"a doubled quote inside a literal": {
			`CREATE TABLE users (a text DEFAULT 'it''s, fine', b text)`,
			[]string{`a text DEFAULT 'it''s, fine'`, `b text`},
		},
		"a line comment": {
			"CREATE TABLE users (a text, -- one, two\n b text)",
			[]string{`a text`, "-- one, two\n b text"},
		},
		"a block comment": {
			`CREATE TABLE users (a text /* one, two */, b text)`,
			[]string{`a text /* one, two */`, ` b text`},
		},
		"a foreign key": {
			`CREATE TABLE o (id integer, FOREIGN KEY (a, b) REFERENCES p (c, d))`,
			[]string{`id integer`, `FOREIGN KEY (a, b) REFERENCES p (c, d)`},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := trimmedItems(split(t, c.statement))

			if !slices.Equal(got, trimmed(c.want)) {
				t.Errorf("split into\n  %q\nwant\n  %q", got, trimmed(c.want))
			}
		})
	}

	t.Log("The splitter is what the whole rebuild rests on: everything it does not target is copied")
	t.Log("through byte for byte, so an item it mis-identifies is the one way a rebuild can lose")
	t.Log("something. It reads quotes, brackets, backticks, literals, comments and nesting, and")
	t.Log("understands nothing else about what it is splitting.")
}

func TestTheTableOptionsAfterTheColumnListSurvive(t *testing.T) {
	for _, suffix := range []string{"", " WITHOUT ROWID", " STRICT", " WITHOUT ROWID, STRICT"} {
		t.Run(strings.TrimSpace(suffix), func(t *testing.T) {
			d := split(t, `CREATE TABLE users (id integer PRIMARY KEY)`+suffix)

			if d.suffix != suffix {
				t.Errorf("the suffix is %q, want %q", d.suffix, suffix)
			}

			if !strings.HasSuffix(d.render("users"), ")"+suffix) {
				t.Errorf("rendering lost it: %s", d.render("users"))
			}
		})
	}

	t.Log("WITHOUT ROWID and STRICT change what the table is, and they live after the closing")
	t.Log("parenthesis where nothing else this driver reads would have found them.")
}

func TestRenderingPutsTheDefinitionUnderAGivenName(t *testing.T) {
	d := split(t, `CREATE TABLE "users" (id integer, email text)`)

	rendered := d.render("migr_rebuild_users")
	if !strings.HasPrefix(rendered, `CREATE TABLE "migr_rebuild_users" (`) {
		t.Errorf("rendered as %s", rendered)
	}

	if strings.Contains(rendered, `"users"`) {
		t.Errorf("the old name survived into the shadow: %s", rendered)
	}

	t.Log("The shadow and the final table are the same definition under two names, which is why")
	t.Log("the name is a parameter rather than something edited out of the recorded statement.")
}

func TestADefinitionThisDriverCannotTakeApartIsRefused(t *testing.T) {
	for name, statement := range map[string]string{
		"no column list":   `CREATE TABLE users AS SELECT 1`,
		"unclosed list":    `CREATE TABLE users (id integer`,
		"a trailing comma": `CREATE TABLE users (id integer,)`,
		"an empty list":    `CREATE TABLE users ()`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := splitTable("users", statement)
			if !errors.Is(err, ErrRebuild) {
				t.Fatalf("splitTable returned %v, want %v", err, ErrRebuild)
			}

			if !strings.Contains(err.Error(), "users") {
				t.Errorf("the refusal does not name the table: %v", err)
			}
		})
	}

	t.Log("A rebuild that guessed at a definition it could not read would emit a table that is not")
	t.Log("the one it replaced, which is the failure this whole task exists to avoid.")
}

// trimmedItems renders a definition's items with their surrounding whitespace removed.
func trimmedItems(d tableDefinition) []string {
	return trimmed(d.items)
}

// trimmed removes the whitespace a comparison should not depend on.
func trimmed(items []string) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = strings.TrimSpace(item)
	}

	return out
}
