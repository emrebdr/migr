package rule

import (
	"slices"
	"strings"
	"testing"
)

func TestEveryShippedRuleIsOneASetWillAccept(t *testing.T) {
	if _, err := NewSet(All(), nil); err != nil {
		t.Fatalf("the rules this build ships cannot be built into a set: %v", err)
	}
}

func TestEveryShippedRuleSaysWhatItIsAndWhatItChecks(t *testing.T) {
	for _, r := range All() {
		t.Run(string(r.ID), func(t *testing.T) {
			if !r.ID.Valid() {
				t.Errorf("id %q is not one migr.yaml can name", r.ID)
			}

			if strings.TrimSpace(r.Summary) == "" {
				t.Error("it has no summary, so migr config cannot say what it checks")
			}

			if !r.Default.Valid() {
				t.Errorf("it defaults to %q, which is not a severity", r.Default)
			}

			if r.Default == SeverityOff {
				t.Error("it ships switched off, so nothing it found would ever be reported")
			}

			if r.Judge == nil {
				t.Error("it judges nothing")
			}
		})
	}
}

func TestTheShippedRulesAreTheOnesThisTaskClaims(t *testing.T) {
	want := []ID{MissingPrimaryKey, UnboundedString}

	if got := IDs(); !slices.Equal(got, want) {
		t.Errorf("IDs() = %v, want %v", got, want)
	}
}

func TestEveryShippedRuleIsReachableByIdAndNothingElseIs(t *testing.T) {
	for _, r := range All() {
		severity, shipped := Default(r.ID)
		if !shipped {
			t.Errorf("Default(%q) reports it is not shipped", r.ID)
		}

		if severity != r.Default {
			t.Errorf("Default(%q) = %q, want %q", r.ID, severity, r.Default)
		}

		if Summary(r.ID) != r.Summary {
			t.Errorf("Summary(%q) = %q, want %q", r.ID, Summary(r.ID), r.Summary)
		}
	}

	if _, shipped := Default("nonesuch"); shipped {
		t.Error("a rule nothing ships reports a default")
	}

	if Summary("nonesuch") != "" {
		t.Error("a rule nothing ships reports a summary")
	}
}

func TestDefaultsResolveEveryShippedRuleAndNothingMore(t *testing.T) {
	defaults := Defaults()

	if len(defaults) != len(All()) {
		t.Fatalf("Defaults() holds %d rules and this build ships %d", len(defaults), len(All()))
	}

	for _, r := range All() {
		if defaults[r.ID] != r.Default {
			t.Errorf("Defaults()[%q] = %q, want %q", r.ID, defaults[r.ID], r.Default)
		}
	}
}

func TestTheRuleListsAreCopies(t *testing.T) {
	first := All()
	first[0].ID = "tampered"

	if All()[0].ID == "tampered" {
		t.Error("All() hands out the package's own rules")
	}

	ids := IDs()
	ids[0] = "tampered"

	if IDs()[0] == "tampered" {
		t.Error("IDs() hands out the package's own slice")
	}

	defaults := Defaults()
	defaults[MissingPrimaryKey] = SeverityOff

	if Defaults()[MissingPrimaryKey] == SeverityOff {
		t.Error("Defaults() hands out the package's own map")
	}
}

func TestNoShippedRuleIsAboutDestruction(t *testing.T) {
	for _, r := range All() {
		if strings.Contains(string(r.ID), "destructive") || strings.Contains(string(r.ID), "drop") {
			t.Errorf("rule %q looks like it judges destruction, which is a hard gate rather than "+
				"a rule: a severity must never be able to permit what validation.allow_destructive "+
				"permits (ADR 0063)", r.ID)
		}
	}
}

func TestTheShippedIdsRenderForAMessage(t *testing.T) {
	joined := JoinIDs()

	for _, id := range IDs() {
		if !strings.Contains(joined, string(id)) {
			t.Errorf("JoinIDs() = %q, which does not name %q", joined, id)
		}
	}
}
