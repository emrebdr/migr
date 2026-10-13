package postgres

import (
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

func TestTheKeyIsTheSameEveryTime(t *testing.T) {
	first := namespaceKey("public", "migr_migrations")

	for range 64 {
		if again := namespaceKey("public", "migr_migrations"); again != first {
			t.Fatalf("the same namespace produced %s and then %s", first, again)
		}
	}

	if first != (lockKey{class: 1835495969, obj: 1813949449}) {
		t.Errorf("the default namespace keys on %s, and this value is pinned deliberately", first)
		t.Log("A changed key is a compatibility break: a new migr would not block an old one")
		t.Log("mid-migration, so two versions could run against one database at once. Changing it")
		t.Log("on purpose means changing this number and saying so in the release notes.")
	}

	if first.obj < 0 || first.class < 0 {
		t.Errorf("%s carries a negative half, which pg_locks reports as a signed number an", first)
		t.Log("operator then has to match by eye against a Go constant printed differently.")
	}

	t.Log("Determinism is the whole property. hash/maphash is seeded per process, so it would")
	t.Log("give every migr invocation a different key and the lock would block nobody -- a")
	t.Log("silent-wrong of exactly the kind invariant #8 exists to prevent.")
}

func TestDifferentNamespacesGetDifferentKeys(t *testing.T) {
	keys := map[lockKey]string{}

	namespaces := []struct{ schema, table string }{
		{schema: "public", table: "migr_migrations"},
		{schema: "billing", table: "migr_migrations"},
		{schema: "public", table: "other_migrations"},
		{schema: "", table: "migr_migrations"},
		{schema: "a", table: "bc"},
		{schema: "ab", table: "c"},
	}

	for _, ns := range namespaces {
		name := ns.schema + "/" + ns.table

		key := namespaceKey(ns.schema, ns.table)
		if clash, seen := keys[key]; seen {
			t.Errorf("%s and %s both key on %s", name, clash, key)
		}

		keys[key] = name
	}

	t.Log("The last two are the case that matters: a/bc and ab/c are different installations and")
	t.Log("concatenating the inputs without a separator would give them one key, so an unrelated")
	t.Log("migration would block. The separator is a NUL, which no identifier may contain.")
}

func TestEveryKeyCarriesTheSameClassID(t *testing.T) {
	varied := []lockKey{
		namespaceKey("public", "migr_migrations"),
		namespaceKey("billing", "other"),
		namespaceKey("", ""),
	}

	for _, key := range varied {
		if key.class != lockClassID {
			t.Errorf("key %s does not carry migr's class id %d", key, lockClassID)
		}
	}

	if varied[0].obj == varied[1].obj {
		t.Error("two different namespaces produced the same object id")
	}

	t.Log("PostgreSQL keeps the two-key advisory space separate from the one-key space, so an")
	t.Log("application using pg_advisory_lock(bigint) cannot collide with migr at all. The fixed")
	t.Log("class id is what makes migr's own locks recognizable in pg_locks, which is what")
	t.Log("M4-T4's \"say who holds the lock\" will read.")
}

func TestAnUnsetHistoryTableStillYieldsAKey(t *testing.T) {
	schema, table := driver.Options{Schema: "public"}.Namespace()

	if table != driver.DefaultHistoryTable {
		t.Errorf("an unset history table resolved to %q", table)
	}

	if namespaceKey(schema, table) != namespaceKey("public", driver.DefaultHistoryTable) {
		t.Error("the defaulted namespace keys differently from the explicit one")
		t.Log("config applies the same default, so a driver built straight from Options and one")
		t.Log("built through migr.yaml must lock on the same key -- otherwise the two would not")
		t.Log("block each other, which is the one thing the lock exists to do.")
	}
}

func TestTheSeedTableKeysNoLockOfItsOwn(t *testing.T) {
	keyed := func(seeds string) lockKey {
		t.Helper()

		built, err := New(driver.Options{Schema: "public", SeedTable: seeds})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		return built.(*pgDriver).key
	}

	if keyed("migr_seeds") != keyed("fixtures") {
		t.Error("renaming the seed table moved the migration lock")
		t.Log("Seeds are recorded beside the migration history and must run under its lock, or a")
		t.Log("seed run and a migr up interleave -- which is exactly the mixing seeds never do.")
	}
}
