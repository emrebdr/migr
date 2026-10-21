package postgres

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// shadowOfPeople is a shadow of people keyed by id, whose columns the shadow writes as their own types.
var shadowOfPeople = driver.Shadow{Table: "people", Shadow: "_people_migr_new", Retired: "_people_migr_old",
	Key: []string{"id"}}

// peoplePair is what a copy and a replay know about people and its shadow.
var peoplePair = shadowPair{shared: []string{"id", "label", "note"},
	types: map[string]string{"id": "bigint", "label": "character varying", "note": "text"}}

func TestAShadowCarriesWhatItCanAndRefusesWhatItWouldGetWrong(t *testing.T) {
	c := &pgConn{}

	for _, op := range []ir.Operation{ir.AddColumn{}, ir.DropColumn{}, ir.AlterColumn{}, ir.AddIndex{},
		ir.DropIndex{}, ir.AddCheck{}, ir.DropCheck{}, ir.DropUnique{}} {
		if err := c.Shadows(op); err != nil {
			t.Errorf("%s was refused: %v", op.Kind(), err)
		}
	}

	for _, op := range []ir.Operation{ir.AddUnique{}, ir.RenameColumn{}, ir.AddForeignKey{}, ir.DropForeignKey{},
		ir.CreateTable{}} {
		if err := c.Shadows(op); err == nil {
			t.Errorf("%s was carried", op.Kind())
		}
	}
}

func TestADroppedIndexIsTheShadowsCopyOfIt(t *testing.T) {
	moved, err := retarget(ir.DropIndex{Table: "people", Name: "people_label"}, shadowOfPeople.Shadow)
	if err != nil {
		t.Fatalf("retarget: %v", err)
	}

	if got := moved.(ir.DropIndex); got.Name != newIndexName("people_label", shadowOfPeople.Shadow) ||
		got.Table != shadowOfPeople.Shadow {
		t.Errorf("a dropped index retargets to %+v, want the shadow's copy of it", got)
	}
}

func TestDerivedIndexNamesFitAndDiffer(t *testing.T) {
	long := strings.Repeat("x", 63)

	for _, name := range []string{newIndexName(long, "s"), oldIndexName(long, "s"), newIndexName("a", "s")} {
		if len(name) > nameLimit {
			t.Errorf("%q is %d bytes, past NAMEDATALEN", name, len(name))
		}
	}

	if newIndexName("a", "s") == oldIndexName("a", "s") || newIndexName("a", "s1") == newIndexName("a", "s2") {
		t.Error("two derived names collide")
	}
}

func TestAReplayUpsertsWholeRowsUpdatesPartialOnesAndMovesAPartialKeyChange(t *testing.T) {
	whole := capture.Change{Kind: capture.Update, Table: "people", Columns: []string{"id", "label", "note"},
		Row: []*string{s("1"), s("a"), s("n")}}
	again := whole
	again.Row = []*string{s("1"), s("b"), s("n")}

	statement, args, err := upsertStatement(shadowOfPeople, peoplePair, latest(shadowOfPeople, []capture.Change{whole, again}))
	if err != nil {
		t.Fatalf("upsertStatement: %v", err)
	}

	if !strings.Contains(statement, "ON CONFLICT (\"id\") DO UPDATE SET \"label\" = EXCLUDED.\"label\"") ||
		!strings.Contains(statement, "$2::text::character varying") || len(args) != 3 || *args[1].(*string) != "b" {
		t.Errorf("two writes of one key upsert as %s %v, want the last one alone, cast without a modifier", statement, args)
	}

	partial := capture.Change{Kind: capture.Update, Table: "people", Columns: []string{"id", "label"},
		Row: []*string{s("1"), s("c")}, Partial: true}

	statement, _, err = partialStatement(shadowOfPeople, peoplePair, partial)
	if err != nil || !strings.HasPrefix(statement, "UPDATE") || strings.Contains(statement, "INSERT") {
		t.Errorf("a partial change replays as %q (%v), want an update that inserts nothing", statement, err)
	}

	partial.From = &capture.Change{Kind: capture.Delete, Columns: []string{"id"}, Row: []*string{s("7")}}

	statement, _, err = moveStatement(shadowOfPeople, peoplePair, partial)
	if err != nil || !strings.Contains(statement, "DELETE FROM") || !strings.Contains(statement, "old.\"note\"") {
		t.Errorf("a partial key move replays as %q (%v), want the old row's note carried to the new key", statement, err)
	}
}

func TestAReplayGroupsDeletesAndKeepsTheirOrderAgainstWrites(t *testing.T) {
	removed := capture.Change{Kind: capture.Delete, Columns: []string{"id"}, Row: []*string{s("1")}}
	other := capture.Change{Kind: capture.Delete, Columns: []string{"id"}, Row: []*string{s("2")}}

	statement, args, err := deleteStatement(shadowOfPeople, peoplePair, []capture.Change{removed, other})
	if err != nil || strings.Count(statement, "\"id\" = ") != 2 || len(args) != 2 {
		t.Errorf("two deletes replay as %q %v (%v), want one statement for both", statement, args, err)
	}

	if replayKind(removed) == replayKind(capture.Change{Kind: capture.Insert}) {
		t.Error("a delete and an insert share a run, so their order would be lost")
	}
}

func TestATableACutoverCannotCarryIsRefusedByItsRelationAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		r    shadowedRelation
		says string
	}{
		"partitioned":      {shadowedRelation{kind: relkindPartitioned, identity: "d"}, "partitioned"},
		"inherited":        {shadowedRelation{kind: "r", inherits: true, identity: "d"}, "inheritance"},
		"secured":          {shadowedRelation{kind: "r", secured: true, identity: "d"}, "row-level security"},
		"identity nothing": {shadowedRelation{kind: "r", identity: "n"}, "REPLICA IDENTITY NOTHING"},
		"identity index":   {shadowedRelation{kind: "r", identity: "i"}, "REPLICA IDENTITY USING INDEX"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := strings.Join(relationProblems(tc.r), "; "); !strings.Contains(got, tc.says) {
				t.Errorf("the relation %+v reads as %q, want it to say %q", tc.r, got, tc.says)
			}
		})
	}

	if got := relationProblems(shadowedRelation{kind: "r", identity: "f"}); len(got) != 0 {
		t.Errorf("an ordinary table with REPLICA IDENTITY FULL was refused: %v", got)
	}
}

func TestAGrantIsCopiedForTheTableAndForAColumn(t *testing.T) {
	if got := grantOf("SELECT", "", "s", "", false); got != `GRANT SELECT ON TABLE "s" TO PUBLIC` {
		t.Errorf("a grant to PUBLIC renders as %q", got)
	}

	if got := grantOf("UPDATE", "label", "s", "reader", true); got !=
		`GRANT UPDATE ("label") ON TABLE "s" TO "reader" WITH GRANT OPTION` {
		t.Errorf("a column grant renders as %q", got)
	}
}
