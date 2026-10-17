package postgres

import "testing"

func TestTheIndexAConcurrentBuildNamesIsReadAsPostgreSQLWouldNameIt(t *testing.T) {
	for sql, want := range map[string]string{
		`CREATE INDEX CONCURRENTLY "idx_Email" ON "users" ("email");`: "idx_Email",
		"create unique index concurrently Idx_Tag on tags (tag)":      "idx_tag",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS "a""b" ON t (c)`:     `a"b`,
		"  CREATE   INDEX\n CONCURRENTLY idx ON t (c)":                "idx",
	} {
		if got, ok := builtIndex(sql); !ok || got != want {
			t.Errorf("builtIndex(%q) = %q, %v, want %q", sql, got, ok, want)
		}
	}

	for _, sql := range []string{`CREATE INDEX "idx" ON t (c)`, "DROP INDEX CONCURRENTLY idx", "SELECT 1"} {
		if _, ok := builtIndex(sql); ok {
			t.Errorf("builtIndex(%q) named an index, and only a concurrent build leaves one INVALID", sql)
		}
	}
}
