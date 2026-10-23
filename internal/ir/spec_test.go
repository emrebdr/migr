package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// specPath is the specification this package answers to, relative to the package directory.
var specPath = filepath.Join("..", "..", "docs", "ir.md")

// verdictPrefix introduces the destructiveness line the spec states for each operation.
const verdictPrefix = "- **Destructive:** "

// example is one checked JSON block, with the line it starts on for failure messages.
type example struct {
	line int
	body string
}

// section is one operation's entry in the specification.
type section struct {
	kind     Kind
	line     int
	verdict  string
	examples []example
}

func TestSpecDocumentsEveryOperation(t *testing.T) {
	documented := make(map[Kind]int, len(Kinds()))

	for _, s := range readSpec(t) {
		if first, seen := documented[s.kind]; seen {
			t.Errorf("%s:%d: %q is documented twice, also at line %d", specPath, s.line, s.kind, first)
		}

		documented[s.kind] = s.line

		if !s.kind.Valid() {
			t.Errorf("%s:%d: section %q names no operation in the set", specPath, s.line, s.kind)
		}
	}

	for _, k := range Kinds() {
		if _, ok := documented[k]; !ok {
			t.Errorf("operation %q has no section in %s", k, specPath)
			t.Log("An entry in the specification is one of the five parts of adding an operation.")
			t.Log("Without this check that part is a reviewer's memory; with it, the build says so.")
		}
	}
}

func TestSpecExamplesAreCanonicalDocuments(t *testing.T) {
	for _, s := range readSpec(t) {
		if len(s.examples) == 0 {
			t.Errorf("%s:%d: %q has no checked example", specPath, s.line, s.kind)
		}

		for _, e := range s.examples {
			t.Run(string(s.kind)+":"+strconv.Itoa(e.line), func(t *testing.T) {
				op, err := UnmarshalOperation([]byte(e.body))
				if err != nil {
					t.Fatalf("%s:%d: the example does not decode: %v", specPath, e.line, err)
				}

				if op.Kind() != s.kind {
					t.Errorf("%s:%d: example under %q is a %q", specPath, e.line, s.kind, op.Kind())
				}

				assertCanonical(t, e, op)
			})
		}
	}
}

// assertCanonical reports whether an example is exactly what the encoder would emit.
func assertCanonical(t *testing.T, e example, op Operation) {
	t.Helper()

	want, err := Marshal(op)
	if err != nil {
		t.Fatalf("%s:%d: re-encoding: %v", specPath, e.line, err)
	}

	var got bytes.Buffer
	if err := json.Compact(&got, []byte(e.body)); err != nil {
		t.Fatalf("%s:%d: compacting: %v", specPath, e.line, err)
	}

	if got.String() == string(want) {
		return
	}

	t.Errorf("%s:%d: the example is not the document this operation emits\n got %s\nwant %s",
		specPath, e.line, got.String(), want)
	t.Log("Every example is a conformance vector, not an illustration: an SDK author reading")
	t.Log("this document must be able to copy the bytes. Member order, omitted optional")
	t.Log("fields, escaping, and type spelling all have to match exactly.")
}

func TestACheckedVectorStillCarriesBothSeparatorsRaw(t *testing.T) {
	sections := readSpec(t)

	for _, separator := range []rune{lineSeparator, paragraphSeparator} {
		if vectorCarries(sections, separator) {
			continue
		}

		t.Errorf("%s: no checked example carries U+%04X any more", specPath, separator)
		t.Log("One vector holds both characters raw, which is the only form that is a")
		t.Log("byte-exact vector -- and an invisible one, so an editor or a reformatter can")
		t.Log("normalize it away. All three copies of the IR would then agree on a document")
		t.Log("that no longer states the rule they are being held to (§3.3).")
	}
}

// vectorCarries reports whether any checked example in the specification holds a rune.
func vectorCarries(sections []section, separator rune) bool {
	for _, s := range sections {
		for _, e := range s.examples {
			if strings.ContainsRune(e.body, separator) {
				return true
			}
		}
	}

	return false
}

func TestSpecDestructivenessMatchesTheCode(t *testing.T) {
	stated := make(map[Kind]string, len(Kinds()))

	for _, s := range readSpec(t) {
		stated[s.kind] = s.verdict

		if s.verdict == "" {
			t.Errorf("%s:%d: %q states no destructiveness verdict", specPath, s.line, s.kind)

			continue
		}

		assertVerdict(t, s)
	}

	for _, f := range fixtures(t) {
		verdict := stated[f.kind]
		if verdict != "yes" && verdict != "no" {
			continue
		}

		if want := verdictOf(f.op); verdict != want {
			t.Errorf("%s: %q is documented %q but its canonical form reports %q",
				specPath, f.kind, verdict, want)
		}
	}
}

// assertVerdict reports whether a section's examples bear out the verdict it states.
func assertVerdict(t *testing.T, s section) {
	t.Helper()

	seen := make(map[string]bool, 2)

	for _, e := range s.examples {
		op, err := UnmarshalOperation([]byte(e.body))
		if err != nil {
			continue
		}

		got := verdictOf(op)
		seen[got] = true

		if s.verdict != "when" && got != s.verdict {
			t.Errorf("%s:%d: the example reports %q but %q is documented %q",
				specPath, e.line, got, s.kind, s.verdict)
		}
	}

	if s.verdict == "when" && (!seen["yes"] || !seen["no"]) {
		t.Errorf("%s:%d: %q claims a conditional verdict but does not show both outcomes",
			specPath, s.line, s.kind)
		t.Log("A conditional claim that only ever demonstrates one branch is an assertion.")
		t.Log("Two examples with differing verdicts make it a demonstration.")
	}
}

func TestSpecSummaryMatrixAgreesWithItsSections(t *testing.T) {
	sections := readSpec(t)
	matrix := readMatrix(t)

	if len(matrix) != len(sections) {
		t.Errorf("the summary matrix has %d rows but there are %d sections", len(matrix), len(sections))
	}

	for _, s := range sections {
		row, ok := matrix[s.kind]
		if !ok {
			t.Errorf("%q has a section but no row in the summary matrix", s.kind)

			continue
		}

		if row != s.verdict {
			t.Errorf("%q is %q in the summary matrix and %q in its own section", s.kind, row, s.verdict)
			t.Log("The duplication is deliberate -- a reader arriving at one should not have to")
			t.Log("consult the other -- which is exactly why it has to be checked.")
		}
	}
}

// verdictOf renders an operation's destructiveness the way the specification writes it.
func verdictOf(op Operation) string {
	if op.Destructive() {
		return "yes"
	}

	return "no"
}

// readSpec parses the specification's operation sections, failing the test if it cannot.
func readSpec(t *testing.T) []section {
	t.Helper()

	sections, err := parseSections(readSpecLines(t))
	if err != nil {
		t.Fatalf("parsing %s: %v", specPath, err)
	}

	return sections
}

// readSpecLines reads the specification, refusing to skip when it is missing.
func readSpecLines(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Log("A skipped check is how a green run stops meaning anything, so this fails.")
		t.Fatalf("the specification must be readable from this package: %v", err)
	}

	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
}

// parseSections walks the document, collecting each operation heading and what follows it.
func parseSections(lines []string) ([]section, error) {
	var (
		sections []section
		current  *section
		fence    string
		block    []string
		start    int
	)

	for i, line := range lines {
		number := i + 1

		if lang, ok := fenceLang(line); ok {
			if fence == "" {
				fence, start, block = lang, number, nil

				continue
			}

			if current != nil && fence == "json" {
				current.examples = append(current.examples,
					example{line: start, body: strings.Join(block, "\n")})
			}

			fence = ""

			continue
		}

		if fence != "" {
			block = append(block, line)

			continue
		}

		if kind, ok := opHeading(line); ok {
			sections = append(sections, section{kind: kind, line: number})
			current = &sections[len(sections)-1]

			continue
		}

		if verdict, ok := parseVerdict(line); ok && current != nil && current.verdict == "" {
			current.verdict = verdict
		}
	}

	if fence != "" {
		return nil, fmt.Errorf("unclosed fenced block opened at line %d", start)
	}

	return sections, nil
}

// readMatrix collects the summary matrix's stated verdict for each operation.
func readMatrix(t *testing.T) map[Kind]string {
	t.Helper()

	rows := make(map[Kind]string, len(Kinds()))

	for _, line := range readSpecLines(t) {
		if kind, verdict, ok := parseMatrixRow(line); ok {
			rows[kind] = verdict
		}
	}

	if len(rows) == 0 {
		t.Fatalf("%s has no summary matrix", specPath)
	}

	return rows
}

// fenceLang reports a fenced block's language tag when a line opens or closes a fence.
func fenceLang(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "```")
	if !ok {
		return "", false
	}

	return strings.TrimSpace(rest), true
}

// opHeading reports the operation a level-three heading names, when it names one.
func opHeading(line string) (Kind, bool) {
	rest, ok := strings.CutPrefix(line, "### ")
	if !ok {
		return "", false
	}

	name, ok := backticked(strings.TrimSpace(rest))
	if !ok {
		return "", false
	}

	return Kind(name), true
}

// parseVerdict reports the destructiveness a section's verdict line states.
func parseVerdict(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), verdictPrefix)
	if !ok {
		return "", false
	}

	return normalizeVerdict(rest), true
}

// parseMatrixRow reports the operation and verdict a summary-matrix row states.
func parseMatrixRow(line string) (Kind, string, bool) {
	if !strings.HasPrefix(line, "| [`") {
		return "", "", false
	}

	cells := strings.Split(strings.Trim(line, "|"), "|")
	if len(cells) < 2 {
		return "", "", false
	}

	name, ok := backticked(strings.TrimSpace(cells[0]))
	if !ok {
		return "", "", false
	}

	return Kind(name), normalizeVerdict(cells[1]), true
}

// backticked unwraps a value written between backticks, with or without a link label.
func backticked(s string) (string, bool) {
	inner, ok := strings.CutPrefix(strings.TrimPrefix(s, "["), "`")
	if !ok {
		return "", false
	}

	name, _, ok := strings.Cut(inner, "`")
	if !ok {
		return "", false
	}

	return name, true
}

// normalizeVerdict reduces a stated verdict to yes, no, or when.
func normalizeVerdict(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "*", ""))

	for _, literal := range []string{"yes", "no", "when"} {
		if s == literal || strings.HasPrefix(s, literal+" ") || strings.HasPrefix(s, literal+".") {
			return literal
		}
	}

	return s
}
