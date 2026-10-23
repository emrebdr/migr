package capability

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestARefusalAlwaysStatesAReason(t *testing.T) {
	v := Unsupported("SQLite cannot drop a column before 3.35 without rebuilding the table")

	if v.Support() != SupportUnsupported {
		t.Errorf("Support() = %q, want %q", v.Support(), SupportUnsupported)
	}

	if v.Reason() == "" {
		t.Error("a refusal carried no reason")
		t.Log("The conformance suite asserts every op is either compilable or refused with a")
		t.Log("reason -- no third possibility. An empty reason is that third possibility.")
	}

	for _, blank := range []string{"", " ", "\t\n  "} {
		refusal := Unsupported(blank)

		if refusal.Support() != SupportUnsupported {
			t.Errorf("Unsupported(%q) = %q, want a refusal", blank, refusal.Support())
		}

		if strings.TrimSpace(refusal.Reason()) == "" {
			t.Errorf("Unsupported(%q) carried %q as its reason", blank, refusal.Reason())
			t.Log("Whitespace is as useless to an operator as nothing at all, and it is what a")
			t.Log("check for the empty string alone lets through. The substituted text has to say")
			t.Log("that a reason was missing, or preflight prints a blank line and blocks anyway.")
		}

		if !strings.Contains(refusal.Reason(), "reason") {
			t.Errorf("reason %q does not say that a reason was missing", refusal.Reason())
		}
	}
}

func TestAnEmulationWithoutAStrategyBecomesARefusal(t *testing.T) {
	for _, strategy := range []string{"", "  ", "\t\n"} {
		v := Emulated(strategy, LockFree)

		if v.Support() != SupportUnsupported {
			t.Errorf("Emulated(%q, ...) = %q, want a refusal", strategy, v.Support())
			t.Log("An emulation nobody can name is one migr plan cannot label, so an operator")
			t.Log("would learn that their table was rebuilt only after it happened. Degrading to")
			t.Log("a refusal is the safe direction, and it makes the invalid state unbuildable.")
		}

		if !strings.Contains(v.Reason(), "strategy") {
			t.Errorf("reason %q does not name the missing strategy", v.Reason())
		}
	}
}

func TestAnEmulationCarriesTheLabelPlanOutputPrints(t *testing.T) {
	v := Emulated("rebuilds the table, preserving indexes and foreign keys", TableRewrite)

	if v.Support() != SupportEmulated || !v.Allowed() {
		t.Fatalf("Emulated(...) = %#v, want an allowed, emulated verdict", v)
	}

	if !strings.Contains(v.String(), "rebuilds the table") {
		t.Errorf("String() = %q, which does not show the operator what will happen", v)
	}

	if v.Reason() != "" {
		t.Errorf("Reason() = %q on a verdict that was not a refusal", v.Reason())
	}
}

func TestTheTwoAxesAreIndependent(t *testing.T) {
	for _, lock := range Impacts() {
		native := Native(lock)
		if !native.Allowed() || native.Lock() != lock {
			t.Errorf("Native(%s) = %#v, want a native verdict carrying that lock cost", lock, native)
		}

		emulated := Emulated("rebuilds the table", lock)
		if !emulated.Allowed() || emulated.Lock() != lock {
			t.Errorf("Emulated(_, %s) = %#v, want an emulated verdict carrying it", lock, emulated)
		}
	}

	t.Log("Support and lock impact are uncorrelated: ALTER TABLE ... TYPE is native on PostgreSQL")
	t.Log("and rewrites the whole table, while SQLite's emulated column drop on a small table is")
	t.Log("cheap. A model that cannot express both is the rejected single axis.")
}

func TestARefusedOperationHasNoLockCostToReport(t *testing.T) {
	v := Unsupported("MySQL has no partial indexes")

	if v.Lock() != LockUnknown {
		t.Errorf("Lock() = %q on a refusal, want %q", v.Lock(), LockUnknown)
		t.Log("An operation that cannot run costs a live database nothing. Letting a refusal")
		t.Log("carry lock_free would let it slip past a --require-safe gate that should never")
		t.Log("have been asked, since preflight refuses it on the support axis first.")
	}
}

func TestTheZeroVerdictRefuses(t *testing.T) {
	var v Verdict

	if v.Allowed() {
		t.Error("the zero Verdict approves the operation")
		t.Log("Allowed() must name the two verdicts that permit execution, not exclude the one")
		t.Log("that forbids it. Written as != SupportUnsupported, a zero Verdict would approve.")
	}

	if v.Support().Valid() {
		t.Errorf("Support() = %q on a verdict no constructor built", v.Support())
	}

	if v.Lock().AtMost(TableRewrite) {
		t.Error("the zero Verdict's lock cost passed the loosest ceiling there is")
	}

	if _, err := json.Marshal(v); !errors.Is(err, ErrInvalidVerdict) {
		t.Errorf("marshaling the zero Verdict returned %v, want ErrInvalidVerdict", err)
		t.Log("Its fields are unexported, so without this it would serialize to {} and reach an")
		t.Log("SDK over host.capability.get as a verdict with no content and no complaint.")
	}
}

func TestAVerdictSurvivesTheWireUnchanged(t *testing.T) {
	tests := []struct {
		name string
		in   Verdict
		want string
	}{
		{
			name: "native",
			in:   Native(LockFree),
			want: `{"support":"native","lock_impact":"lock_free"}`,
		},
		{
			name: "emulated",
			in:   Emulated("rebuilds the table", TableRewrite),
			want: `{"support":"emulated","strategy":"rebuilds the table","lock_impact":"table_rewrite"}`,
		},
		{
			name: "unsupported",
			in:   Unsupported("no partial indexes"),
			want: `{"support":"unsupported","reason":"no partial indexes","lock_impact":"unknown"}`,
		},
		{
			name: "unclassified",
			in:   Native(LockUnknown),
			want: `{"support":"native","lock_impact":"unknown"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			if string(got) != tt.want {
				t.Errorf("Marshal = %s, want %s", got, tt.want)
			}
		})
	}

	t.Log("Every SDK reads this shape through host.capability.get, so the keys and the verdict")
	t.Log("spellings are a wire contract rather than an implementation detail.")
}

func TestVerdictsAreComparable(t *testing.T) {
	cheap, dear := Native(LockFree), Native(TableRewrite)

	if cheap != Native(LockFree) {
		t.Error("two identical native verdicts compared unequal")
	}

	if cheap == dear {
		t.Error("two verdicts differing only in lock cost compared equal")
	}

	t.Log("Every field is a string, so == works and a test can assert a resolved verdict")
	t.Log("directly. schema.Type had to grow an Equal method because a slice field cost it this.")
}

func TestALowerableVerdictComparesAtItsPessimisticCost(t *testing.T) {
	v := Native(TableRewrite).Unless(BriefExclusive, "the table is not ROW_FORMAT=COMPRESSED")

	if v.Lock() != TableRewrite {
		t.Errorf("Lock = %s, want table_rewrite: a ceiling compares what nobody has confirmed", v.Lock())
	}

	if v.Lowest() != BriefExclusive {
		t.Errorf("Lowest = %s, want brief_exclusive", v.Lowest())
	}

	if v.Condition() != "the table is not ROW_FORMAT=COMPRESSED" {
		t.Errorf("Condition = %q", v.Condition())
	}

	if v.Lock().AtMost(BriefExclusive) {
		t.Error("a verdict whose condition nobody checked passed a brief_exclusive ceiling")
	}

	want := "native (table_rewrite; brief_exclusive where the table is not ROW_FORMAT=COMPRESSED)"
	if v.String() != want {
		t.Errorf("String = %q, want %q", v.String(), want)
	}

	t.Log("Resolve performs no I/O, so what depends on the table is reported at its worst and")
	t.Log("says what would make it cheaper. Only a session that read the table may lower it.")
}

func TestALowerableVerdictThatCannotBeWrittenIsLeftAsItWas(t *testing.T) {
	dear := Native(TableRewrite)

	tests := map[string]Verdict{
		"a blank condition":           dear.Unless(BriefExclusive, "  "),
		"an impact that is not lower": dear.Unless(TableRewrite, "anything"),
		"an impact that is higher":    Native(BriefExclusive).Unless(TableRewrite, "anything"),
		"unknown as the lower":        dear.Unless(LockUnknown, "anything"),
		"an unrecognized lower":       dear.Unless(LockImpact("instant"), "anything"),
	}

	for name, got := range tests {
		t.Run(name, func(t *testing.T) {
			if got.Condition() != "" || got.Lowest() != got.Lock() {
				t.Errorf("Unless kept %s where %q", got.Lowest(), got.Condition())
			}
		})
	}

	refused := Unsupported("no").Unless(LockFree, "anything")
	if refused != Unsupported("no") {
		t.Errorf("a refusal was given a cheaper cost: %s", refused)
	}

	t.Log("Every path fails toward the pessimistic verdict, so a mistaken Unless costs a flag")
	t.Log("rather than an outage.")
}

func TestALowerableVerdictSendsOnlyItsPessimisticCost(t *testing.T) {
	got, err := json.Marshal(Native(TableRewrite).Unless(BriefExclusive, "the table allows it"))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if want := `{"support":"native","lock_impact":"table_rewrite"}`; string(got) != want {
		t.Errorf("Marshal = %s, want %s", got, want)
	}

	t.Log("A runtime has no table to read, so the wire carries the cost it can rely on and the")
	t.Log("protocol does not change.")
}

func TestAConfirmedVerdictCostsWhatItsConditionAllowed(t *testing.T) {
	emulated := Emulated("redefines the column", TableRewrite).Unless(BriefExclusive, "the table allows it")

	got := emulated.Confirmed()
	if got != Emulated("redefines the column", BriefExclusive) {
		t.Errorf("Confirmed = %s, want the same emulation at brief_exclusive", got)
	}

	if plain := Native(LongExclusive); plain.Confirmed() != plain {
		t.Errorf("a verdict carrying no condition was changed: %s", plain.Confirmed())
	}

	t.Log("Confirming keeps the support axis and the strategy: only the cost was ever in doubt.")
}
