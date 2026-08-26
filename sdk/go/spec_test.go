package migr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// specPath is the specification this copy of the IR answers to, relative to the package directory.
var specPath = filepath.Join("..", "..", "docs", "ir.md")

// example is one checked JSON block, with the line it starts on for failure messages.
type example struct {
	line int
	body string
}

// section is one operation's entry in the specification.
type section struct {
	kind     Kind
	line     int
	examples []example
	fields   []field
}

// field is one row of an operation's field table.
type field struct {
	name string
	// required reports whether the document says the member is always present.
	required bool
	line     int
}

func TestSpecDocumentsEveryOperationThisSDKEmits(t *testing.T) {
	documented := make(map[Kind]int, len(Kinds()))

	for _, s := range readSpec(t) {
		if first, seen := documented[s.kind]; seen {
			t.Errorf("%s:%d: %q is documented twice, also at line %d", specPath, s.line, s.kind, first)
		}

		documented[s.kind] = s.line

		if !s.kind.Valid() {
			t.Errorf("%s:%d: section %q names an operation this SDK cannot emit", specPath, s.line, s.kind)
		}
	}

	for _, k := range Kinds() {
		if _, ok := documented[k]; !ok {
			t.Errorf("operation %q has no section in %s", k, specPath)
		}
	}

	t.Log("This is the biconditional internal/ir/spec_test.go asserts from the engine's side.")
	t.Log("Neither copy imports the other -- both answer to this document -- so a twenty-first")
	t.Log("operation has to fail here as well as there, on the day it is added.")
}

func TestSpecExamplesRoundTripThroughThisCopy(t *testing.T) {
	for _, s := range readSpec(t) {
		if len(s.examples) == 0 {
			t.Errorf("%s:%d: %q has no checked example", specPath, s.line, s.kind)
		}

		for _, e := range s.examples {
			t.Run(string(s.kind)+":"+strconv.Itoa(e.line), func(t *testing.T) {
				op, err := decodeOperation(s.kind, []byte(e.body))
				if err != nil {
					t.Fatalf("%s:%d: the example does not decode into this SDK's struct: %v",
						specPath, e.line, err)
				}

				if op.Kind() != s.kind {
					t.Errorf("%s:%d: example under %q decoded as a %q", specPath, e.line, s.kind, op.Kind())
				}

				assertCanonical(t, e, op)
			})
		}
	}
}

// assertCanonical reports whether an example is exactly what this SDK would emit.
func assertCanonical(t *testing.T, e example, op Operation) {
	t.Helper()

	if err := op.check(); err != nil {
		t.Errorf("%s:%d: this SDK refuses an operation the specification documents: %v",
			specPath, e.line, err)
	}

	want, err := marshal(op)
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

	t.Errorf("%s:%d: the example is not the document this SDK emits\n got %s\nwant %s",
		specPath, e.line, got.String(), want)
	t.Log("Decoding is what catches a field this copy forgot to declare, since a test that only")
	t.Log("constructed operations could not miss what it never wrote. Member order, omitted")
	t.Log("optional fields, escaping, and type spelling all have to match exactly.")
}

func TestSpecFieldTablesMatchTheStructsThisCopyDeclares(t *testing.T) {
	for _, s := range readSpec(t) {
		op, err := decodeOperation(s.kind, []byte("{}"))
		if err != nil {
			t.Errorf("%s:%d: this SDK declares no struct for %q: %v", specPath, s.line, s.kind, err)

			continue
		}

		t.Run(string(s.kind), func(t *testing.T) {
			if len(s.fields) == 0 {
				t.Fatalf("%s:%d: %q tabulates no fields", specPath, s.line, s.kind)
			}

			assertFieldsMatch(t, s, reflect.TypeOf(op))
		})
	}

	t.Log("docs/ir.md §3.6: an optional field is omitted when unset and is never emitted as null.")
	t.Log("Reading the Required column is what holds every omitempty to the document, including")
	t.Log("on operations no example exercises without their from payload.")
}

// assertFieldsMatch reports whether a struct declares exactly the members one section tabulates.
func assertFieldsMatch(t *testing.T, s section, typ reflect.Type) {
	t.Helper()

	omitted := declaredMembers(t, typ)

	for _, f := range s.fields {
		optional, declared := omitted[f.name]
		if !declared {
			t.Errorf("%s:%d: %q documents %q and this SDK declares no such member",
				specPath, f.line, s.kind, f.name)

			continue
		}

		delete(omitted, f.name)

		if optional == f.required {
			t.Errorf("%s:%d: %q is documented required=%t and is omitted when unset=%t",
				specPath, f.line, f.name, f.required, optional)
		}
	}

	for name := range omitted {
		t.Errorf("%s: this SDK declares %q on %q and the document tabulates no such member",
			specPath, name, s.kind)
	}
}

// declaredMembers returns each member a struct emits, and whether it is omitted when unset.
func declaredMembers(t *testing.T, typ reflect.Type) map[string]bool {
	t.Helper()

	members := make(map[string]bool, typ.NumField())

	for i := range typ.NumField() {
		name, options, found := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if !found && name == "" {
			t.Fatalf("%s.%s carries no json tag", typ.Name(), typ.Field(i).Name)
		}

		members[name] = slices.Contains(strings.Split(options, ","), "omitempty")
	}

	return members
}

func TestSpecTypeVocabularyMatchesThisCopy(t *testing.T) {
	lines := readSpecLines(t)

	documented := listAfter(lines, "Fourteen stand alone:")
	for _, form := range firstCellsAfter(lines, "Four take parameters:") {
		name, _, _ := strings.Cut(form, "(")
		documented = append(documented, name)
	}

	assertSameSet(t, "logical types", documented, spellings(TypeKinds()))
}

func TestSpecClosedSetsMatchThisCopy(t *testing.T) {
	lines := readSpecLines(t)

	assertSameSet(t, "default functions",
		listAfter(lines, "### 6.3 Default functions"), spellings(Functions()))

	assertSameSet(t, "referential actions",
		listAfter(lines, "### 6.4 Referential actions"), spellings(Actions()))

	t.Log("§10.2 makes extending either set a breaking change, so an SDK that quietly accepts")
	t.Log("a fifth default function emits a document the engine's decoder refuses.")
}

// assertSameSet reports whether what the document lists and what this SDK accepts are one set.
func assertSameSet(t *testing.T, subject string, documented, accepted []string) {
	t.Helper()

	if len(documented) == 0 {
		t.Fatalf("%s: no %s could be read out of the document", specPath, subject)
	}

	slices.Sort(documented)
	slices.Sort(accepted)

	if slices.Equal(documented, accepted) {
		return
	}

	t.Errorf("%s: the %s documented are %v, and this SDK accepts %v",
		specPath, subject, documented, accepted)
}

// spellings renders a closed set of string-kinded values as the names they carry on the wire.
func spellings[T ~string](values []T) []string {
	names := make([]string, len(values))
	for i, v := range values {
		names[i] = string(v)
	}

	return names
}

// decodeOperation reads one documented example into the struct this copy declares for it.
func decodeOperation(kind Kind, data []byte) (Operation, error) {
	switch kind {
	case KindCreateTable:
		return decodeOp[CreateTable](data)
	case KindDropTable:
		return decodeOp[DropTable](data)
	case KindRenameTable:
		return decodeOp[RenameTable](data)
	case KindAddColumn:
		return decodeOp[AddColumn](data)
	case KindDropColumn:
		return decodeOp[DropColumn](data)
	case KindRenameColumn:
		return decodeOp[RenameColumn](data)
	case KindAlterColumn:
		return decodeOp[AlterColumn](data)
	case KindAddIndex:
		return decodeOp[AddIndex](data)
	case KindDropIndex:
		return decodeOp[DropIndex](data)
	case KindAddForeignKey:
		return decodeOp[AddForeignKey](data)
	case KindDropForeignKey:
		return decodeOp[DropForeignKey](data)
	case KindAddUnique:
		return decodeOp[AddUnique](data)
	case KindDropUnique:
		return decodeOp[DropUnique](data)
	case KindAddCheck:
		return decodeOp[AddCheck](data)
	case KindDropCheck:
		return decodeOp[DropCheck](data)
	case KindAddPrimaryKey:
		return decodeOp[AddPrimaryKey](data)
	case KindDropPrimaryKey:
		return decodeOp[DropPrimaryKey](data)
	case KindCreateView:
		return decodeOp[CreateView](data)
	case KindDropView:
		return decodeOp[DropView](data)
	case KindRaw:
		return decodeOp[Raw](data)
	default:
		return nil, fmt.Errorf("this SDK declares no struct for %q", kind)
	}
}

// decodeOp reads a document into one operation type, refusing a member the type does not declare.
func decodeOp[T Operation](data []byte) (Operation, error) {
	var op T

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("reading fields: %w", err)
	}

	delete(fields, "op")

	body, err := marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("re-encoding fields: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&op); err != nil {
		return nil, fmt.Errorf("decoding: %w", err)
	}

	return op, nil
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
		t.Fatalf("the specification must be readable from this module: %v", err)
	}

	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
}

// parseSections walks the document, collecting each operation heading and the examples under it.
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

		if strings.HasPrefix(line, "## ") {
			current = nil

			continue
		}

		if f, ok := fieldRow(line, number); ok && current != nil {
			current.fields = append(current.fields, f)
		}
	}

	if fence != "" {
		return nil, fmt.Errorf("unclosed fenced block opened at line %d", start)
	}

	return sections, nil
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

// fieldRow reports the member an operation's field table names, when a line names one.
func fieldRow(line string, number int) (field, bool) {
	cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	if len(cells) < 3 {
		return field{}, false
	}

	required := strings.TrimSpace(strings.ReplaceAll(cells[2], "*", ""))
	if required != "yes" && required != "no" {
		return field{}, false
	}

	name, ok := backticked(strings.TrimSpace(cells[0]))
	if !ok {
		return field{}, false
	}

	return field{name: name, required: required == "yes", line: number}, true
}

// listAfter collects every backticked name in the run of lines following a marker.
func listAfter(lines []string, marker string) []string {
	var found []string

	for _, line := range runAfter(lines, marker) {
		found = append(found, allBackticked(line)...)
	}

	return found
}

// firstCellsAfter collects the backticked name in the first cell of each table row after a marker.
func firstCellsAfter(lines []string, marker string) []string {
	var found []string

	for _, line := range runAfter(lines, marker) {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) < 2 {
			continue
		}

		if name, ok := backticked(strings.TrimSpace(cells[0])); ok {
			found = append(found, name)
		}
	}

	return found
}

// sectionText returns everything between a heading and the next one, as one string.
func sectionText(lines []string, heading string) string {
	at := slices.IndexFunc(lines, func(line string) bool {
		return strings.HasPrefix(line, heading)
	})
	if at < 0 {
		return ""
	}

	end := at + 1
	for end < len(lines) && !strings.HasPrefix(lines[end], "### ") {
		end++
	}

	return strings.Join(lines[at:end], " ")
}

// runAfter returns the first unbroken run of non-empty lines following a marker line.
func runAfter(lines []string, marker string) []string {
	at := slices.IndexFunc(lines, func(line string) bool {
		return strings.HasPrefix(strings.TrimSpace(line), marker)
	})
	if at < 0 {
		return nil
	}

	for at++; at < len(lines) && strings.TrimSpace(lines[at]) == ""; at++ {
		continue
	}

	end := at
	for end < len(lines) && strings.TrimSpace(lines[end]) != "" {
		end++
	}

	return lines[at:end]
}

// backticked unwraps the first value written between backticks, with or without a link label.
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

// allBackticked unwraps every value written between backticks on one line.
func allBackticked(line string) []string {
	var found []string

	for rest := line; ; {
		_, after, ok := strings.Cut(rest, "`")
		if !ok {
			return found
		}

		name, remainder, ok := strings.Cut(after, "`")
		if !ok {
			return found
		}

		found = append(found, name)
		rest = remainder
	}
}
