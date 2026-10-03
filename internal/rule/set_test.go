package rule

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// asked records whether a rule was ever given anything to judge, which off must make never.
type asked struct {
	// times is how many subjects the rule was handed.
	times int
}

// fake returns a rule under the given id and default that reports one finding and counts its calls.
func (a *asked) fake(id ID, severity Severity) Rule {
	return Rule{
		ID:      id,
		Default: severity,
		Summary: "a fake rule, which reports everything it is shown",
		Judge: func(_ Subject) []Finding {
			a.times++

			return []Finding{{Direction: ir.Up, Operation: 1, Kind: ir.KindCreateTable,
				Detail: "something about " + string(id)}}
		},
	}
}

// subject returns a migration for a rule to judge, which the fakes above do not read.
func subject(t *testing.T) Subject {
	t.Helper()

	m, err := migration.Parse("20260727T153000_add_users.go")
	if err != nil {
		t.Fatalf("migration.Parse: %v", err)
	}

	return Subject{Migration: m, Undo: UndoAuthored}
}

func TestASetJudgesInRuleOrderWhateverOrderItWasGiven(t *testing.T) {
	var counted asked

	set, err := NewSet([]Rule{
		counted.fake("zebra-rule", SeverityWarn),
		counted.fake("alpha-rule", SeverityError),
		counted.fake("middle-rule", SeverityWarn),
	}, nil)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	if got := set.IDs(); !slices.Equal(got, []ID{"alpha-rule", "middle-rule", "zebra-rule"}) {
		t.Errorf("IDs() = %v, want them sorted", got)
	}

	judged := set.Judge(subject(t))
	if len(judged) != 3 {
		t.Fatalf("judged %d findings, want 3", len(judged))
	}

	for i, want := range []ID{"alpha-rule", "middle-rule", "zebra-rule"} {
		if judged[i].Rule != want {
			t.Errorf("judgement %d came from %q, want %q", i+1, judged[i].Rule, want)
		}
	}
}

func TestEveryJudgementCarriesTheSeverityThisProjectGaveItsRule(t *testing.T) {
	var counted asked

	set, err := NewSet([]Rule{
		counted.fake("defaulted", SeverityError),
		counted.fake("overridden", SeverityError),
	}, map[ID]Severity{"overridden": SeverityWarn})
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	for _, judged := range set.Judge(subject(t)) {
		want := SeverityError
		if judged.Rule == "overridden" {
			want = SeverityWarn
		}

		if judged.Severity != want {
			t.Errorf("%q judged at %q, want %q", judged.Rule, judged.Severity, want)
		}
	}

	if got := set.Severity("overridden"); got != SeverityWarn {
		t.Errorf("Severity(overridden) = %q, want %q", got, SeverityWarn)
	}

	if got := set.Severity("defaulted"); got != SeverityError {
		t.Errorf("Severity(defaulted) = %q, want %q", got, SeverityError)
	}

	if got := set.Severity("nonesuch"); got != SeverityOff {
		t.Errorf("Severity of a rule nothing holds = %q, want %q", got, SeverityOff)
	}
}

func TestARuleSetToOffIsNeverAsked(t *testing.T) {
	var counted asked

	set, err := NewSet([]Rule{counted.fake("silenced", SeverityError)},
		map[ID]Severity{"silenced": SeverityOff})
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	if judged := set.Judge(subject(t)); len(judged) != 0 {
		t.Errorf("a rule set to off reported %d findings", len(judged))
	}

	if counted.times != 0 {
		t.Errorf("a rule set to off was asked %d times, and off means it runs at all", counted.times)
	}
}

func TestADefaultOfOffIsAlsoNeverAsked(t *testing.T) {
	var counted asked

	set, err := NewSet([]Rule{counted.fake("quiet", SeverityOff)}, nil)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	if judged := set.Judge(subject(t)); len(judged) != 0 {
		t.Errorf("a rule defaulting to off reported %d findings", len(judged))
	}

	if counted.times != 0 {
		t.Error("a rule defaulting to off was asked anyway")
	}
}

func TestASetNothingBuiltIsTellableFromOneWithNoRules(t *testing.T) {
	var zero Set

	if !zero.Zero() {
		t.Error("a zero Set does not report itself zero")
	}

	if judged := zero.Judge(subject(t)); len(judged) != 0 {
		t.Errorf("a zero Set judged %d findings", len(judged))
	}

	empty, err := NewSet(nil, nil)
	if err != nil {
		t.Fatalf("NewSet over no rules: %v", err)
	}

	if empty.Zero() {
		t.Error("a set built over no rules reports itself unbuilt, and those are different things")
	}
}

func TestASetThatCannotMeanAnythingIsRefused(t *testing.T) {
	var counted asked

	for _, c := range []struct {
		name       string
		rules      []Rule
		configured map[ID]Severity
		says       string
	}{
		{"a rule with no id", []Rule{counted.fake("", SeverityWarn)}, nil, "has no id"},
		{"a rule whose id is not kebab-case", []Rule{counted.fake("Not-Kebab", SeverityWarn)}, nil,
			"not kebab-case"},
		{"two rules of one id", []Rule{counted.fake("twice", SeverityWarn),
			counted.fake("twice", SeverityError)}, nil, "declared twice"},
		{"a rule that judges nothing", []Rule{{ID: "mute", Default: SeverityWarn,
			Summary: "it has no judge"}}, nil, "judges nothing"},
		{"a rule with no summary", []Rule{{ID: "terse", Default: SeverityWarn,
			Judge: func(Subject) []Finding { return nil }}}, nil, "has no summary"},
		{"a rule defaulting to nothing", []Rule{counted.fake("undefaulted", Severity("fatal"))},
			nil, `defaults to "fatal" is not a severity`},
		{"a configured id nothing answers to", []Rule{counted.fake("real", SeverityWarn)},
			map[ID]Severity{"imaginary": SeverityOff}, `no rule is called "imaginary"`},
		{"a configured severity nothing recognizes", []Rule{counted.fake("real", SeverityWarn)},
			map[ID]Severity{"real": Severity("quiet")}, `"quiet" is not a severity`},
	} {
		t.Run(c.name, func(t *testing.T) {
			set, err := NewSet(c.rules, c.configured)
			if err == nil {
				t.Fatalf("it was accepted, holding %v", set.IDs())
			}

			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("error %q does not say %q", err, c.says)
			}
		})
	}
}

func TestARefusedIdNamesTheOnesThatDoExist(t *testing.T) {
	var counted asked

	_, err := NewSet([]Rule{counted.fake("beta", SeverityWarn), counted.fake("alpha", SeverityWarn)},
		map[ID]Severity{"gamma": SeverityOff})
	if err == nil {
		t.Fatal("an unknown id was accepted")
	}

	if !strings.Contains(err.Error(), "alpha, beta") {
		t.Errorf("error %q does not list the ids that exist, sorted", err)
	}
}

// settling is how many times a check is repeated before its answer is called deterministic.
const settling = 50

func TestTheSameConfigurationAlwaysReportsTheSameProblemFirst(t *testing.T) {
	var counted asked

	rules := []Rule{counted.fake("real", SeverityWarn)}
	configured := map[ID]Severity{
		"delta-imaginary":   SeverityOff,
		"alpha-imaginary":   SeverityOff,
		"charlie-imaginary": SeverityOff,
		"bravo-imaginary":   SeverityOff,
	}

	_, first := NewSet(rules, configured)
	if first == nil {
		t.Fatal("four ids nothing answers to were accepted")
	}

	for range settling {
		_, err := NewSet(rules, configured)
		if err == nil {
			t.Fatal("the same configuration was accepted on a later run")
		}

		if err.Error() != first.Error() {
			t.Fatalf("two runs over one configuration report different problems first:\n%v\n%v",
				first, err)
		}
	}
}

func TestASetDoesNotReadTheSliceItWasGivenAfterwards(t *testing.T) {
	var counted asked

	rules := []Rule{counted.fake("kept", SeverityWarn)}

	set, err := NewSet(rules, nil)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	rules[0] = counted.fake("swapped", SeverityError)

	if got := set.IDs(); !slices.Equal(got, []ID{"kept"}) {
		t.Errorf("IDs() = %v after the caller's slice changed, want [kept]", got)
	}
}
