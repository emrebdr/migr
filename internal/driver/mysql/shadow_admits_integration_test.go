//go:build integration

package mysql

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

func TestAdmitsRefusesEachTableAShadowWouldGetWrongNamingWhy(t *testing.T) {
	c := openServer(t, servers[0], driver.Options{HistoryTable: uniqueName(t)})
	base := "ad" + digestOf(t.Name())

	objects := []string{base + "_child", base + "_keyed", base + "_triggered", base + "_checked",
		base + "_spatial", base + "_generated", base + "_plain"}
	drop(t.Context(), c, objects...)
	dropLater(t, c, objects...)

	for _, stmt := range []string{
		"CREATE TABLE " + quote(base+"_keyed") + " (id int PRIMARY KEY)",
		"CREATE TABLE " + quote(base+"_child") + " (id int PRIMARY KEY, kid int, CONSTRAINT " +
			quote(base+"_fk") + " FOREIGN KEY (kid) REFERENCES " + quote(base+"_keyed") + " (id))",
		"CREATE TABLE " + quote(base+"_triggered") + " (id int PRIMARY KEY, n int)",
		"CREATE TABLE " + quote(base+"_checked") + " (id int PRIMARY KEY, n int, CONSTRAINT " +
			quote(base+"_positive") + " CHECK (n > 0))",
		"CREATE TABLE " + quote(base+"_spatial") + " (id int PRIMARY KEY, at point)",
		"CREATE TABLE " + quote(base+"_generated") + " (id int PRIMARY KEY, n int, CHECK (n > 0), " +
			"twice int GENERATED ALWAYS AS (n * 2) VIRTUAL)",
		"CREATE TABLE " + quote(base+"_plain") + " (id int PRIMARY KEY, n int)",
	} {
		exec(t, c, stmt)
	}

	if err := asRoot(t, "CREATE TRIGGER `migr`."+quote(base+"_tr")+" BEFORE INSERT ON `migr`."+
		quote(base+"_triggered")+" FOR EACH ROW SET NEW.n = 1"); err != nil {
		t.Fatalf("creating the trigger as root, which binary logging requires: %v", err)
	}

	for table, says := range map[string]string{
		base + "_keyed":     "foreign keys " + base + "_fk",
		base + "_child":     "foreign keys " + base + "_fk",
		base + "_triggered": "triggers " + base + "_tr",
		base + "_checked":   "named checks " + base + "_positive",
		base + "_spatial":   "at (point)",
	} {
		err := c.Admits(t.Context(), driver.Shadow{Table: table, Key: []string{"id"}})
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("Admits(%s) returned %v, want a refusal naming %q", table, err, says)
		}
	}

	for _, table := range []string{base + "_generated", base + "_plain"} {
		if err := c.Admits(t.Context(), driver.Shadow{Table: table, Key: []string{"id"}}); err != nil {
			t.Errorf("Admits(%s) refused a table a shadow carries: %v", table, err)
		}
	}
}
