package rule

import (
	"strings"
	"testing"
)

func TestTheSeverityVocabularyIsTheThreeMigrYamlNames(t *testing.T) {
	const declared = 3

	if len(Severities()) != declared {
		t.Fatalf("M10-T4 declares %d severities, Severities() has %d", declared,
			len(Severities()))
	}

	for _, s := range Severities() {
		if !s.Valid() {
			t.Errorf("severity %q is listed but not valid", s)
		}
	}

	if Severity("fatal").Valid() {
		t.Error("an unlisted severity reports itself valid")
	}

	if Severity("").Valid() {
		t.Error("the empty severity reports itself valid")
	}
}

func TestOnlyTwoSeveritiesReportAnything(t *testing.T) {
	for _, c := range []struct {
		given   Severity
		reports bool
	}{
		{SeverityError, true},
		{SeverityWarn, true},
		{SeverityOff, false},
		{Severity("fatal"), false},
		{Severity(""), false},
	} {
		if got := c.given.Reports(); got != c.reports {
			t.Errorf("%q.Reports() = %v, want %v", c.given, got, c.reports)
		}
	}
}

func TestSeveritiesIsACopy(t *testing.T) {
	first := Severities()
	first[0] = "tampered"

	if Severities()[0] != SeverityError {
		t.Error("Severities() hands out the package's own slice")
	}
}

func TestAMisspelledSeverityIsDescribedWithTheSpellingItMeant(t *testing.T) {
	for _, c := range []struct {
		given string
		want  string
	}{
		{"OFF", `"OFF" is not a severity (did you mean "off"?)`},
		{"Warn", `"Warn" is not a severity (did you mean "warn"?)`},
		{"ERROR", `"ERROR" is not a severity (did you mean "error"?)`},
		{"no", `"no" is not a severity (expected: error, warn, off)`},
		{"", `"" is not a severity (expected: error, warn, off)`},
	} {
		if got := DescribeSeverity(c.given); got != c.want {
			t.Errorf("DescribeSeverity(%q) = %q, want %q", c.given, got, c.want)
		}
	}
}

func TestTheSeverityVocabularyRendersForAMessage(t *testing.T) {
	joined := JoinSeverities()

	for _, s := range Severities() {
		if !strings.Contains(joined, string(s)) {
			t.Errorf("JoinSeverities() = %q, which does not name %q", joined, s)
		}
	}
}
