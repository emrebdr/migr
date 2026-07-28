package capability

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

// everyKind returns a rule for every operation, so a test can remove or replace just one.
func everyKind(v Verdict) map[ir.Kind]Rule {
	rules := make(map[ir.Kind]Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = Fixed(v)
	}

	return rules
}

// mustSet builds a set from a complete rule table, failing the test if it will not build.
func mustSet(t *testing.T, facts Facts, rules map[ir.Kind]Rule) Set {
	t.Helper()

	set, err := NewSet(facts, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	return set
}

func TestASetMustAnswerForEveryOperation(t *testing.T) {
	for _, omitted := range ir.Kinds() {
		t.Run(string(omitted), func(t *testing.T) {
			rules := everyKind(Native(LockUnknown))
			delete(rules, omitted)

			_, err := NewSet(Facts{Driver: "postgres"}, rules)

			var incomplete *IncompleteError
			if !errors.As(err, &incomplete) {
				t.Fatalf("a set missing %s was accepted (%v)", omitted, err)
			}

			if !slices.Equal(incomplete.Missing, []ir.Kind{omitted}) {
				t.Errorf("Missing = %v, want exactly [%s]", incomplete.Missing, omitted)
			}

			if !strings.Contains(err.Error(), string(omitted)) {
				t.Errorf("message %q does not name the undeclared operation", err)
			}
		})
	}
}

func TestAnUndeclaredOperationIsNamedInDeclarationOrder(t *testing.T) {
	rules := everyKind(Native(LockUnknown))
	delete(rules, ir.KindDropView)
	delete(rules, ir.KindAddColumn)
	delete(rules, ir.KindRaw)

	_, err := NewSet(Facts{Driver: "sqlite"}, rules)

	var incomplete *IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("NewSet returned %v, want an *IncompleteError", err)
	}

	want := []ir.Kind{ir.KindAddColumn, ir.KindDropView, ir.KindRaw}
	if !slices.Equal(incomplete.Missing, want) {
		t.Errorf("Missing = %v, want %v", incomplete.Missing, want)
		t.Log("Ranging a map would order these differently on every run, so the same mistake")
		t.Log("would produce a different message each time (invariant #8).")
	}

	if !errors.Is(err, ErrIncomplete) {
		t.Errorf("errors.Is(err, ErrIncomplete) is false for %v", err)
	}
}

func TestARuleForSomethingThatIsNotAnOperationIsReported(t *testing.T) {
	rules := everyKind(Native(LockUnknown))
	delete(rules, ir.KindAddUnique)
	rules["add_uniqe"] = Fixed(Native(LockFree))

	_, err := NewSet(Facts{Driver: "mysql"}, rules)
	if err == nil {
		t.Fatal("a set with the right number of rules and a misspelled key was accepted")
	}

	for _, want := range []string{"add_unique", "add_uniqe"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
			t.Log("Counting rules rather than checking each operation would accept this. Naming")
			t.Log("both halves is what turns \"something is missing\" into the actual typo.")
		}
	}
}

func TestANilRuleIsRefusedRatherThanLeftToPanic(t *testing.T) {
	rules := everyKind(Native(LockUnknown))
	rules[ir.KindDropTable] = nil

	if _, err := NewSet(Facts{Driver: "postgres"}, rules); err == nil {
		t.Error("a set with a nil rule was accepted")
		t.Log("The key is present, so a length check passes and Resolve then calls nil -- the")
		t.Log("one panic path in a package forbidden to panic.")
	}
}

func TestASetMustNameItsDriver(t *testing.T) {
	if _, err := NewSet(Facts{Server: "16.2"}, everyKind(Native(LockFree))); !errors.Is(err, ErrNoDriver) {
		t.Errorf("NewSet with no driver name returned %v, want ErrNoDriver", err)
		t.Log("Every refusal this set produces names the driver that made it. Without a name the")
		t.Log("operator reads an error about nothing they can look up.")
	}
}

func TestAVerdictDependsOnTheOperationNotOnlyItsKind(t *testing.T) {
	rules := everyKind(Native(LockUnknown))
	rules[ir.KindAddColumn] = func(op ir.Operation) Verdict {
		add, ok := op.(ir.AddColumn)
		if !ok || add.Column.Default != nil {
			return Native(TableRewrite)
		}

		return Native(LockFree)
	}

	set := mustSet(t, Facts{Driver: "postgres", Server: "10.23"}, rules)

	plain := set.Resolve(ir.AddColumn{Table: "users", Column: column(t, nil)})
	if plain.Lock() != LockFree {
		t.Errorf("a column with no default resolved to %s, want %s", plain.Lock(), LockFree)
	}

	defaulted := set.Resolve(ir.AddColumn{Table: "users", Column: column(t, ir.Literal("0"))})
	if defaulted.Lock() != TableRewrite {
		t.Errorf("a column with a default resolved to %s, want %s", defaulted.Lock(), TableRewrite)
		t.Log("MySQL 8 chooses INSTANT, INPLACE, or COPY by column type and by the specific")
		t.Log("alteration, so a verdict keyed only by operation name cannot be right. This is")
		t.Log("the property that forces a rule to receive the whole operation.")
	}
}

func TestTwoServerVersionsDisagreeAboutTheSameOperation(t *testing.T) {
	sets := map[string]Set{
		"10.23": mustSet(t, Facts{Driver: "postgres", Server: "10.23"}, addColumnRules(false)),
		"16.2":  mustSet(t, Facts{Driver: "postgres", Server: "16.2"}, addColumnRules(true)),
	}

	op := ir.AddColumn{Table: "users", Column: column(t, ir.Literal("0"))}

	if got := sets["10.23"].Resolve(op).Lock(); got != TableRewrite {
		t.Errorf("PostgreSQL 10 resolved to %s, want %s", got, TableRewrite)
	}

	if got := sets["16.2"].Resolve(op).Lock(); got != LockFree {
		t.Errorf("PostgreSQL 16 resolved to %s, want %s", got, LockFree)
		t.Log("ADD COLUMN ... DEFAULT rewrote the whole table on PostgreSQL 10 and is instant")
		t.Log("from 11 onward. The version is closed over at construction, so this is provable")
		t.Log("with no database and Resolve stays free of I/O.")
		t.Log("These rules are a fixture demonstrating the mechanism, not PostgreSQL's real")
		t.Log("answers: the driver reports brief_exclusive rather than lock_free for the 11+ case,")
		t.Log("because ALTER TABLE still takes ACCESS EXCLUSIVE (ADR 0016).")
	}

	if sets["16.2"].Server() != "16.2" || sets["16.2"].Driver() != "postgres" {
		t.Errorf("a set reports itself as %s/%s", sets["16.2"].Driver(), sets["16.2"].Server())
	}
}

func TestASetReportsTheFactsItWasBuiltFrom(t *testing.T) {
	rules := everyKind(Native(LockFree))

	transactional := mustSet(t, Facts{
		Driver: "postgres", Server: "17.6", TransactionalDDL: true,
	}, rules)

	if !transactional.TransactionalDDL() {
		t.Error("a set built from a server with transactional DDL reports none")
	}

	plain := mustSet(t, Facts{Driver: "mysql", Server: "8.0.36"}, rules)

	if plain.TransactionalDDL() {
		t.Error("a set built from a server without transactional DDL reports it")
		t.Log("Both directions are asserted because only one of them fails safe. A driver wrongly")
		t.Log("reported as transactional means M4-T9 skips the dirty-state model and a partial")
		t.Log("failure is recorded as a clean one -- a mutation sweep found the accessor could")
		t.Log("return a constant true with nothing noticing.")
	}

	var unbuilt Set

	if unbuilt.TransactionalDDL() {
		t.Error("a set that was never built claims transactional DDL")
	}
}

func TestTheZeroSetRefusesEverythingRatherThanApprovingIt(t *testing.T) {
	var set Set

	for _, op := range everyOperation(t) {
		verdict := set.Resolve(op)

		if verdict.Allowed() {
			t.Errorf("an unbuilt set approved %s", op.Kind())
		}

		if verdict.Reason() == "" {
			t.Errorf("an unbuilt set refused %s without saying why", op.Kind())
		}

		if verdict.Lock() != LockUnknown {
			t.Errorf("an unbuilt set reported %s for %s", verdict.Lock(), op.Kind())
		}
	}

	if set.Resolve(nil).Allowed() {
		t.Error("an unbuilt set approved a nil operation")
	}
}

func TestEveryOperationResolvesThroughItsOwnRule(t *testing.T) {
	rules := make(map[ir.Kind]Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = Fixed(Unsupported("refused as " + string(kind)))
	}

	set := mustSet(t, Facts{Driver: "postgres"}, rules)

	for _, op := range everyOperation(t) {
		if want := "refused as " + string(op.Kind()); set.Resolve(op).Reason() != want {
			t.Errorf("%s resolved through the rule for %q", op.Kind(), set.Resolve(op).Reason())
			t.Log("Keying on anything but op.Kind(), or ranging the map and taking the first")
			t.Log("match, would answer one operation's question with another's rule.")
		}
	}
}

func TestARuleReturningAVerdictItNeverBuiltIsARefusal(t *testing.T) {
	rules := everyKind(Native(LockUnknown))
	rules[ir.KindDropTable] = func(ir.Operation) Verdict { return Verdict{} }

	set := mustSet(t, Facts{Driver: "postgres"}, rules)
	verdict := set.Resolve(ir.DropTable{Table: "users"})

	if verdict.Allowed() {
		t.Error("a rule returning a zero Verdict approved the operation")
	}

	for _, want := range []string{"postgres", "drop_table"} {
		if !strings.Contains(verdict.Reason(), want) {
			t.Errorf("reason %q does not name %q", verdict.Reason(), want)
			t.Log("A driver bug must surface as a refusal naming the driver and the operation,")
			t.Log("not as an empty verdict that downstream code reads however it likes.")
		}
	}
}

func TestCapabilitiesCannotChangeAfterTheSetIsBuilt(t *testing.T) {
	rules := everyKind(Unsupported("not classified yet"))
	set := mustSet(t, Facts{Driver: "postgres"}, rules)

	rules[ir.KindDropTable] = Fixed(Native(LockFree))

	if set.Resolve(ir.DropTable{Table: "users"}).Allowed() {
		t.Error("writing to the caller's map changed a set that was already built")
		t.Log("Capabilities are resolved once, against one server, at Open. If the table stays")
		t.Log("live the same plan can compile differently between two runs (invariant #8).")
	}
}

func TestAFixedRuleIgnoresThePayload(t *testing.T) {
	rule := Fixed(Native(BriefExclusive))

	for _, op := range everyOperation(t) {
		if rule(op) != Native(BriefExclusive) {
			t.Errorf("a fixed rule answered %v for %s", rule(op), op.Kind())
		}
	}
}

func TestTwoSetsShareNoState(t *testing.T) {
	first := everyKind(Native(LockFree))
	second := maps.Clone(first)
	second[ir.KindDropTable] = Fixed(Unsupported("this driver refuses it"))

	a := mustSet(t, Facts{Driver: "postgres"}, first)
	b := mustSet(t, Facts{Driver: "sqlite"}, second)

	if !a.Resolve(ir.DropTable{Table: "users"}).Allowed() {
		t.Error("the first set inherited the second's refusal")
	}

	if b.Resolve(ir.DropTable{Table: "users"}).Allowed() {
		t.Error("the second set did not keep its own refusal")
	}
}

// addColumnRules returns a full rule table whose add_column answer depends on the server version.
func addColumnRules(atLeast11 bool) map[ir.Kind]Rule {
	rules := everyKind(Native(LockUnknown))

	if atLeast11 {
		rules[ir.KindAddColumn] = Fixed(Native(LockFree))

		return rules
	}

	rules[ir.KindAddColumn] = func(op ir.Operation) Verdict {
		add, ok := op.(ir.AddColumn)
		if !ok || add.Column.Default != nil {
			return Native(TableRewrite)
		}

		return Native(LockFree)
	}

	return rules
}
