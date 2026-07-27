package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtures(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		wantInBody  int
		wantLongDoc int
	}{
		{name: "clean file reports nothing", fixture: "clean.go"},
		{name: "exemptions are honored", fixture: "exempt.go"},
		{name: "comments inside bodies", fixture: "inbody.go", wantInBody: 7},
		{name: "doc comments over one line", fixture: "longdoc.go", wantLongDoc: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings, err := CheckFile(filepath.Join("testdata", tt.fixture))
			if err != nil {
				t.Fatalf("checking %s: %v", tt.fixture, err)
			}

			inBody, longDoc := countByRule(findings)

			if inBody != tt.wantInBody {
				t.Errorf("in-body findings = %d, want %d\n%s", inBody, tt.wantInBody, render(findings))
			}

			if longDoc != tt.wantLongDoc {
				t.Errorf("long-doc findings = %d, want %d\n%s", longDoc, tt.wantLongDoc, render(findings))
			}
		})
	}
}

func TestInBodyRuleCoversEveryShape(t *testing.T) {
	shapes := []string{
		"Between",
		"Trailing",
		"Nested",
		"Composite",
		"Block",
		"Declaration",
		"packageLevelLiteral",
	}

	findings, err := CheckFile(filepath.Join("testdata", "inbody.go"))
	if err != nil {
		t.Fatalf("checking inbody.go: %v", err)
	}

	if len(findings) != len(shapes) {
		t.Fatalf("want one finding per shape (%d), got %d\n%s", len(shapes), len(findings), render(findings))
	}

	for _, f := range findings {
		if !strings.Contains(f.Message, "function body") {
			t.Errorf("%s: wrong rule reported: %s", f, f.Message)
		}
	}
}

func TestPackageDocIsNeverChecked(t *testing.T) {
	findings, err := CheckFile(filepath.Join("testdata", "clean.go"))
	if err != nil {
		t.Fatalf("checking clean.go: %v", err)
	}

	if len(findings) != 0 {
		t.Errorf("a multi-line package doc must not be reported:\n%s", render(findings))
	}
}

func TestExemptPrefixes(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "go directive", text: "//go:generate stringer", want: true},
		{name: "nolint with reason", text: "//nolint:gosec // operator-supplied", want: true},
		{name: "deprecated marker", text: "// Deprecated: use Kind instead", want: true},
		{name: "ordinary comment", text: "// this explains the next line"},
		{name: "spaced nolint is not a directive", text: "// nolint:gosec"},
		{name: "lowercase deprecated is not a marker", text: "// deprecated: use Kind"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isExempt(tt.text); got != tt.want {
				t.Errorf("isExempt(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestWalkSkipsTestdata(t *testing.T) {
	findings, err := Check(".")
	if err != nil {
		t.Fatalf("checking the package: %v", err)
	}

	for _, f := range findings {
		if strings.Contains(f.Path, "testdata") {
			t.Errorf("fixtures must not reach a real run: %s", f)
		}
	}
}

func TestPackagePassesItsOwnRules(t *testing.T) {
	findings, err := Check(".")
	if err != nil {
		t.Fatalf("checking the package: %v", err)
	}

	if len(findings) != 0 {
		t.Errorf("the check must satisfy the rules it enforces:\n%s", render(findings))
	}
}

func TestUnparseableFileIsAnError(t *testing.T) {
	_, err := CheckFile(filepath.Join("testdata", "does_not_exist.go"))
	if err == nil {
		t.Fatal("a file that cannot be read must be an error, not a silent skip")
	}
}

// countByRule splits findings into the two rules so a test can assert each separately.
func countByRule(findings []Finding) (inBody, longDoc int) {
	for _, f := range findings {
		if strings.Contains(f.Message, "function body") {
			inBody++

			continue
		}

		longDoc++
	}

	return inBody, longDoc
}

// render formats findings one per line for a readable test failure.
func render(findings []Finding) string {
	lines := make([]string, 0, len(findings))
	for _, f := range findings {
		lines = append(lines, "  "+f.String())
	}

	return strings.Join(lines, "\n")
}
