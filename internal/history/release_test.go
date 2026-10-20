package history

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestTheReleaseTableIsNamedAfterItsHistoryAndKeyedByBatch(t *testing.T) {
	ops := ReleaseSchema("migr_migrations")

	created, ok := ops[0].(ir.CreateTable)
	if len(ops) != 1 || !ok || created.Table != "migr_migrations_releases" ||
		len(created.PrimaryKey) != 1 || created.PrimaryKey[0] != "batch" {
		t.Fatalf("ReleaseSchema is %+v", ops)
	}

	if err := created.Validate(); err != nil {
		t.Errorf("the release table does not validate: %v", err)
	}
}

func TestAReleaseNamesABatchAndSomethingAtMostItsColumnWide(t *testing.T) {
	for name, r := range map[string]Release{
		"no batch": {Name: "r1"},
		"blank":    {Batch: 1, Name: " "},
		"too long": {Batch: 1, Name: strings.Repeat("r", releaseLimit+1)},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: %+v was accepted", name, r)
		}
	}

	if err := (Release{Batch: 1, Name: strings.Repeat("r", releaseLimit)}).Validate(); err != nil {
		t.Errorf("a release exactly as wide as its column was refused: %v", err)
	}
}

func TestReleaseOfFindsTheBatchItNames(t *testing.T) {
	releases := []Release{{Batch: 1, Name: "r1"}, {Batch: 3, Name: "r3"}}

	if name, ok := ReleaseOf(releases, 3); !ok || name != "r3" {
		t.Errorf("batch 3 ran under %q (%v)", name, ok)
	}

	if _, ok := ReleaseOf(releases, 2); ok {
		t.Error("batch 2 ran under a release nothing recorded")
	}
}
