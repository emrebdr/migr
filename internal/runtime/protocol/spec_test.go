package protocol

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// specPath is the specification this package answers to, relative to the package directory.
var specPath = filepath.Join("..", "..", "..", "docs", "protocol.md")

// message is one checked JSON block, with the line it starts on for failure messages.
type message struct {
	line int
	body string
}

// example is one worked conversation: a level-three prose heading and the messages under it.
type example struct {
	title    string
	line     int
	messages []message
}

// spec is what the specification says, read back from the document itself.
type spec struct {
	// methods are the level-three headings holding a backticked method name, in document order.
	methods []string
	// codes are the numbers and slugs the error tables assign.
	codes map[Code]string
	// messages is every checked JSON block in the whole document.
	messages []message
	// examples are the worked conversations in the last-but-one section.
	examples []example
}

func TestSpecDocumentsEveryMethod(t *testing.T) {
	documented := readSpec(t).methods

	for _, method := range Methods() {
		if !slices.Contains(documented, method) {
			t.Errorf("%s documents no section for %q", specPath, method)
			t.Log("A method an SDK author cannot find is one they cannot implement, and this")
			t.Log("document is the whole contract they are given.")
		}
	}
}

func TestEveryDocumentedMethodExists(t *testing.T) {
	for _, method := range readSpec(t).methods {
		if !Calls(method) {
			t.Errorf("%s has a section for %q, which this protocol version does not define",
				specPath, method)
			t.Log("A section for a method nothing serves is worse than a missing one: an SDK")
			t.Log("author implements it and finds out at the handshake.")
		}
	}
}

func TestSpecDocumentsEveryErrorCode(t *testing.T) {
	stated := readSpec(t).codes

	for _, code := range assigned() {
		slug, documented := stated[code]
		if !documented {
			t.Errorf("%s tabulates no row for code %d (%s)", specPath, code, code)

			continue
		}

		if slug != code.String() {
			t.Errorf("%s calls code %d %q, and this build calls it %q",
				specPath, code, slug, code)
			t.Log("The slug is what an SDK branches on, so the document and the code disagreeing")
			t.Log("about it is a disagreement about the wire.")
		}
	}
}

func TestEveryTabulatedCodeExists(t *testing.T) {
	for code, slug := range readSpec(t).codes {
		if !code.Valid() && !code.Reserved() {
			t.Errorf("%s tabulates code %d (%q), which this protocol version does not assign",
				specPath, code, slug)
		}
	}
}

func TestEveryApplicationCodeIsInTheBandJSONRPCLeavesToUs(t *testing.T) {
	for _, code := range Codes() {
		if code < ApplicationMin || code > ApplicationMax {
			t.Errorf("code %d (%s) sits outside %d..%d", code, code, ApplicationMin, ApplicationMax)
			t.Log("JSON-RPC 2.0 reserves -32768..-32000 for its own predefined errors and leaves")
			t.Log("only -32099..-32000 to the implementation. LSP borrows -32800 for a canceled")
			t.Log("request; §7.3 declines to, and this is what holds that.")
		}
	}
}

func TestEveryDeclaredCodeIsInTheOrderedList(t *testing.T) {
	listed := assigned()

	for code := range slugs {
		if !slices.Contains(listed, code) {
			t.Errorf("code %d (%s) has a slug and is in neither Codes() nor Reserved()", code, code)
			t.Log("Those two are what every other check walks, so a code left out of both is one")
			t.Log("the specification is never held to -- the same hole a driver would have if it")
			t.Log("handed a conformance suite the operations it chose to be asked about.")
		}
	}

	if len(listed) != len(slugs) {
		t.Errorf("Codes() and Reserved() list %d codes and %d have slugs", len(listed), len(slugs))
	}
}

func TestNoReservedCodeIsAlsoAnApplicationCode(t *testing.T) {
	for _, code := range Reserved() {
		if code.Valid() {
			t.Errorf("code %d (%s) is listed as both JSON-RPC's own and migr's", code, code)
		}

		if code >= ApplicationMin && code <= ApplicationMax {
			t.Errorf("reserved code %d (%s) sits inside %d..%d, the band left to an implementation",
				code, code, ApplicationMin, ApplicationMax)
			t.Log("Valid() means 'one of the twelve migr assigns' and Reserved() means 'one of the")
			t.Log("five JSON-RPC defines'. A code in both bands makes each of those answers a lie.")
		}
	}
}

func TestEveryCodeHasADistinctSlug(t *testing.T) {
	seen := make(map[string]Code, len(slugs))

	for _, code := range assigned() {
		if code.String() == "" {
			t.Errorf("code %d names itself with nothing", code)

			continue
		}

		if first, taken := seen[code.String()]; taken {
			t.Errorf("codes %d and %d share the slug %q", first, code, code)
		}

		seen[code.String()] = code
	}
}

func TestEveryCheckedBlockIsAJSONRPCMessage(t *testing.T) {
	found := readSpec(t).messages

	if len(found) == 0 {
		t.Fatalf("%s carries no checked example at all", specPath)
	}

	for _, m := range found {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(m.body), &decoded); err != nil {
			t.Errorf("%s:%d: the example is not JSON: %v", specPath, m.line, err)

			continue
		}

		if decoded["jsonrpc"] != "2.0" {
			t.Errorf("%s:%d: the example carries jsonrpc %v, want \"2.0\"",
				specPath, m.line, decoded["jsonrpc"])
			t.Log("Every block tagged json is a whole message. A payload on its own is tagged")
			t.Log("jsonc, which §1.3 states and this enforces.")
		}
	}
}

func TestEveryCheckedBlockRoundTripsThroughTheCodec(t *testing.T) {
	for _, m := range readSpec(t).messages {
		decoded, err := Decode([]byte(m.body))
		if err != nil {
			t.Errorf("%s:%d: the example does not decode: %v", specPath, m.line, err)

			continue
		}

		encoded, err := Encode(decoded)
		if err != nil {
			t.Errorf("%s:%d: the example does not re-encode: %v", specPath, m.line, err)

			continue
		}

		if string(encoded) != m.body {
			t.Errorf("%s:%d: the example does not round-trip byte for byte\n  document: %s\n  codec:    %s",
				specPath, m.line, m.body, encoded)
			t.Log("§1.3 says these are conformance vectors, so member order, spelling, and the")
			t.Log("absence of whitespace are part of what they specify. Either the document is")
			t.Log("wrong or the encoder is; a third-party SDK cannot tell which.")
		}
	}
}

func TestTheOnlyNullIDInTheDocumentIsAFramingError(t *testing.T) {
	shown := 0

	for _, m := range readSpec(t).messages {
		decoded, err := Decode([]byte(m.body))
		if err != nil || !decoded.ID.Null() {
			continue
		}

		shown++

		if decoded.Error == nil {
			t.Errorf("%s:%d: a null id answers no request, so it may only carry an error",
				specPath, m.line)
		}
	}

	if shown == 0 {
		t.Errorf("%s shows no message carrying the null id §2.6 permits", specPath)
		t.Log("§2.4 says an id is an integer and §2.6 carves out exactly one exception. An")
		t.Log("exception nothing illustrates is one an SDK author implements from prose alone.")
	}
}

func TestEveryCheckedBlockIsOneLine(t *testing.T) {
	for _, m := range readSpec(t).messages {
		if strings.Contains(strings.TrimSpace(m.body), "\n") {
			t.Errorf("%s:%d: the example spans several lines", specPath, m.line)
			t.Log("§2.2 says one message per line, so an example that is pretty-printed shows a")
			t.Log("reader something no implementation may emit.")
		}
	}
}

func TestEveryCheckedBlockNamesAMethodThisVersionDefines(t *testing.T) {
	for _, m := range readSpec(t).messages {
		var decoded struct {
			Method string `json:"method"`
		}

		if err := json.Unmarshal([]byte(m.body), &decoded); err != nil || decoded.Method == "" {
			continue
		}

		if !Calls(decoded.Method) {
			t.Errorf("%s:%d: the example calls %q, which this protocol version does not define",
				specPath, m.line, decoded.Method)
		}
	}
}

func TestAWorkedConversationNeverReusesAnIDInOneDirection(t *testing.T) {
	for _, e := range readSpec(t).examples {
		t.Run(e.title, func(t *testing.T) {
			assertDistinct(t, e)
		})
	}
}

// assertDistinct reports whether one conversation numbers each direction's requests distinctly.
func assertDistinct(t *testing.T, e example) {
	t.Helper()

	claimed := make(map[string]map[float64]int, 2)

	for _, m := range e.messages {
		var decoded struct {
			Method string   `json:"method"`
			ID     *float64 `json:"id"`
		}

		if err := json.Unmarshal([]byte(m.body), &decoded); err != nil || decoded.ID == nil {
			continue
		}

		if decoded.Method == "" {
			continue
		}

		side := directionOf(decoded.Method)
		if claimed[side] == nil {
			claimed[side] = make(map[float64]int, len(e.messages))
		}

		if first, taken := claimed[side][*decoded.ID]; taken {
			t.Errorf("%s:%d: %s request id %v is already claimed at line %d",
				specPath, m.line, side, *decoded.ID, first)
			t.Log("§2.5: each side numbers its own requests, so a reader has to be able to see")
			t.Log("which id belongs to which sequence. A worked example that reuses one inside")
			t.Log("a single direction teaches the mistake it exists to prevent.")
		}

		claimed[side][*decoded.ID] = m.line
	}
}

func TestTheWorkedConversationsShowBothDirections(t *testing.T) {
	for _, e := range readSpec(t).examples {
		sides := make(map[string]bool, 2)

		for _, m := range e.messages {
			var decoded struct {
				Method string `json:"method"`
			}

			if err := json.Unmarshal([]byte(m.body), &decoded); err == nil && decoded.Method != "" {
				sides[directionOf(decoded.Method)] = true
			}
		}

		if !sides["engine"] || !sides["host"] {
			t.Errorf("%s:%d: %q shows only one direction", specPath, e.line, e.title)
			t.Log("The callbacks are what make a migration a program rather than a document, so a")
			t.Log("worked conversation that never shows one is not a conversation.")
		}
	}
}

// assigned returns every code this protocol version names, JSON-RPC's own and migr's alike.
func assigned() []Code {
	return slices.Concat(Codes(), Reserved())
}

// directionOf names which side sends a method, since an id is unique only within its own sequence.
func directionOf(method string) string {
	if slices.Contains(HostMethods(), method) {
		return "host"
	}

	return "engine"
}

// readSpec parses the specification, failing the test if it cannot.
func readSpec(t testing.TB) spec {
	t.Helper()

	parsed, err := parseSpec(readSpecLines(t))
	if err != nil {
		t.Fatalf("parsing %s: %v", specPath, err)
	}

	return parsed
}

// readSpecLines reads the specification, refusing to skip when it is missing.
func readSpecLines(t testing.TB) []string {
	t.Helper()

	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Log("A skipped check is how a green run stops meaning anything, so this fails.")
		t.Fatalf("the specification must be readable from this package: %v", err)
	}

	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
}

// parseSpec walks the document, collecting its method sections, its code tables, and its examples.
func parseSpec(lines []string) (spec, error) {
	found := spec{codes: make(map[Code]string, len(Codes()))}

	var (
		fence   string
		block   []string
		start   int
		current *example
	)

	for i, line := range lines {
		number := i + 1

		if lang, open := fenceLang(line); open {
			if fence == "" {
				fence, start, block = lang, number, nil

				continue
			}

			if fence == "json" {
				found.collect(current, message{line: start, body: strings.Join(block, "\n")})
			}

			fence = ""

			continue
		}

		if fence != "" {
			block = append(block, line)

			continue
		}

		if method, ok := methodHeading(line); ok {
			found.methods = append(found.methods, method)
			current = nil

			continue
		}

		if title, ok := exampleHeading(line); ok {
			found.examples = append(found.examples, example{title: title, line: number})
			current = &found.examples[len(found.examples)-1]

			continue
		}

		if code, slug, ok := codeRow(line); ok {
			found.codes[code] = slug
		}
	}

	if fence != "" {
		return spec{}, fmt.Errorf("unclosed fenced block opened at line %d", start)
	}

	return found, nil
}

// collect files a checked message under the whole document and under the conversation it is inside.
func (s *spec) collect(current *example, m message) {
	s.messages = append(s.messages, m)

	if current != nil {
		current.messages = append(current.messages, m)
	}
}

// fenceLang reports a fenced block's language tag when a line opens or closes a fence.
func fenceLang(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "```")
	if !ok {
		return "", false
	}

	return strings.TrimSpace(rest), true
}

// methodHeading reports the method a level-three heading names, when it names one and nothing else.
func methodHeading(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "### ")
	if !ok {
		return "", false
	}

	return backticked(strings.TrimSpace(rest))
}

// exampleHeading reports the title of a worked conversation, which §1.3 spells as an unnumbered heading.
func exampleHeading(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "### ")
	if !ok {
		return "", false
	}

	title := strings.TrimSpace(rest)
	if _, isMethod := backticked(title); isMethod {
		return "", false
	}

	return title, !numbered(title)
}

// numbered reports whether a heading opens with a section number, which a worked conversation does not.
func numbered(title string) bool {
	return title != "" && unicode.IsDigit(rune(title[0]))
}

// backticked reports the content of a wholly backquoted string.
func backticked(s string) (string, bool) {
	rest, ok := strings.CutPrefix(s, "`")
	if !ok {
		return "", false
	}

	inner, ok := strings.CutSuffix(rest, "`")
	if !ok || strings.Contains(inner, "`") {
		return "", false
	}

	return inner, true
}

// codeRow reports the code and slug a table row assigns, when a row assigns one.
func codeRow(line string) (Code, string, bool) {
	cells := tableCells(line)
	if len(cells) < 3 {
		return 0, "", false
	}

	number, ok := backticked(cells[0])
	if !ok {
		return 0, "", false
	}

	parsed, err := strconv.Atoi(number)
	if err != nil || Code(parsed) < ReservedMin || Code(parsed) > ApplicationMax {
		return 0, "", false
	}

	slug, ok := backticked(cells[1])
	if !ok {
		return 0, "", false
	}

	return Code(parsed), slug, true
}

// tableCells splits a markdown table row into its trimmed cells, and reports nothing for other lines.
func tableCells(line string) []string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return nil
	}

	cells := strings.Split(strings.Trim(trimmed, "|"), "|")
	for i, cell := range cells {
		cells[i] = strings.TrimSpace(cell)
	}

	return cells
}
