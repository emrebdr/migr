package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// specPath is the specification this package answers to, relative to the package directory.
var specPath = filepath.Join("..", "..", "..", "..", "docs", "protocol.md")

// spec is what the specification says, read back from the document itself.
type spec struct {
	// methods are the level-three headings holding a backticked method name, in document order.
	methods []string
	// codes are the numbers and slugs the error tables assign.
	codes map[Code]string
	// messages is every checked JSON block in the whole document, with the line it starts on.
	messages []message
}

// message is one checked JSON block, with the line it starts on for failure messages.
type message struct {
	line int
	body string
}

func TestEveryDocumentedMethodExistsHere(t *testing.T) {
	documented := readSpec(t).methods

	if len(documented) == 0 {
		t.Fatalf("%s documents no method at all", specPath)
	}

	defined := Methods()

	for _, method := range documented {
		if !slices.Contains(defined, method) {
			t.Errorf("%s documents %q, which this SDK does not define", specPath, method)
			t.Log("A method the engine may send and this SDK does not know is answered -32601,")
			t.Log("and a callback it documents and this SDK never makes is a feature nobody gets.")
		}
	}
}

func TestEveryMethodHereIsDocumented(t *testing.T) {
	documented := readSpec(t).methods

	for _, method := range Methods() {
		if !slices.Contains(documented, method) {
			t.Errorf("this SDK defines %q, which %s documents no section for", method, specPath)
		}
	}
}

func TestEveryTabulatedCodeAndSlugMatchesThisSDK(t *testing.T) {
	stated := readSpec(t).codes

	if len(stated) == 0 {
		t.Fatalf("%s tabulates no error code at all", specPath)
	}

	for code, slug := range stated {
		if _, known := slugs[code]; !known {
			t.Errorf("%s tabulates code %d (%q), which this SDK does not know", specPath, code, slug)

			continue
		}

		if got := code.String(); got != slug {
			t.Errorf("%s calls code %d %q, and this SDK calls it %q", specPath, code, slug, got)
			t.Log("The slug is what each side branches on, so the document and an implementation")
			t.Log("disagreeing about it is a disagreement about the wire itself.")
		}
	}
}

func TestEveryCodeThisSDKKnowsIsTabulated(t *testing.T) {
	stated := readSpec(t).codes

	for code := range slugs {
		if _, documented := stated[code]; !documented {
			t.Errorf("this SDK knows code %d (%s), which %s tabulates no row for",
				code, code, specPath)
		}
	}
}

func TestEveryCheckedBlockRoundTripsThroughThisCodecByteForByte(t *testing.T) {
	found := readSpec(t).messages

	if len(found) == 0 {
		t.Fatalf("%s carries no checked example at all", specPath)
	}

	t.Logf("%s carries %d checked messages, and this codec is held to every one of them.",
		specPath, len(found))

	for _, m := range found {
		roundTrip(t, m)
	}
}

// roundTrip decodes one documented message and re-encodes it, byte for byte.
func roundTrip(t *testing.T, m message) {
	t.Helper()

	decoded, err := Decode([]byte(m.body))
	if err != nil {
		t.Errorf("%s:%d: the example does not decode: %v", specPath, m.line, err)

		return
	}

	encoded, err := Encode(decoded)
	if err != nil {
		t.Errorf("%s:%d: the example does not re-encode: %v", specPath, m.line, err)

		return
	}

	if string(encoded) != m.body {
		t.Errorf("%s:%d: the example re-encodes differently\n  document: %s\n  this SDK: %s",
			specPath, m.line, m.body, encoded)
		t.Log("Member order and spelling are specified rather than incidental. A second")
		t.Log("implementation written from the code rather than the document diverges here.")
	}
}

func TestEveryCheckedBlockIsAWholeMessage(t *testing.T) {
	for _, m := range readSpec(t).messages {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(m.body), &decoded); err != nil {
			t.Errorf("%s:%d: the example is not JSON: %v", specPath, m.line, err)

			continue
		}

		if decoded["jsonrpc"] != JSONRPC {
			t.Errorf("%s:%d: the example carries jsonrpc %v, want %q",
				specPath, m.line, decoded["jsonrpc"], JSONRPC)
		}
	}
}

func TestTheProtocolVersionThisSDKSpeaksIsTheOneDocumented(t *testing.T) {
	lines := readSpecLines(t)

	want := "### 10.1 What `protocolVersion` " + strconv.Itoa(Version) + " covers"

	if !slices.Contains(lines, want) {
		t.Errorf("%s has no section %q, so this SDK speaks a version it does not describe",
			specPath, want)
	}
}

// readSpec parses the specification, failing the test when it cannot be read or parsed.
func readSpec(t testing.TB) spec {
	t.Helper()

	return parseSpec(readSpecLines(t))
}

// readSpecLines reads the specification, refusing to skip when it is missing.
func readSpecLines(t testing.TB) []string {
	t.Helper()

	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Log("A skipped check is how a green run stops meaning anything, so this fails. The")
		t.Log("specification lives in the repository this module is developed in.")
		t.Fatalf("the specification must be readable from this package: %v", err)
	}

	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
}

// parseSpec walks the document, collecting its method sections, its code tables, and its examples.
func parseSpec(lines []string) spec {
	found := spec{codes: map[Code]string{}}

	var (
		fence string
		block []string
		start int
	)

	for i, line := range lines {
		number := i + 1

		if lang, open := fenceLang(line); open {
			if fence == "" {
				fence, start, block = lang, number, nil

				continue
			}

			if fence == "json" {
				found.messages = append(found.messages,
					message{line: start, body: strings.Join(block, "\n")})
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

			continue
		}

		if code, slug, ok := codeRow(line); ok {
			found.codes[code] = slug
		}
	}

	return found
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

// codeRow reports the number and slug a table row assigns, for the rows that assign one.
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
	if err != nil {
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

// backticked reads the one backticked value a cell or heading holds, and nothing else.
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
