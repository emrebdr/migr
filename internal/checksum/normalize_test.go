package checksum

import (
	"bytes"
	"testing"
)

func TestACheckoutRewritingLineEndingsDoesNotMoveTheHash(t *testing.T) {
	tests := []struct {
		name string
		in   string
		lf   string
	}{
		{name: "windows checkout", in: "line one\r\nline two\r\n", lf: "line one\nline two\n"},
		{name: "classic mac lone CR", in: "line one\rline two\r", lf: "line one\nline two\n"},
		{name: "mixed endings in one file", in: "a\r\nb\rc\nd", lf: "a\nb\nc\nd"},
		{name: "CR CR LF", in: "a\r\r\nb", lf: "a\n\nb"},
		{name: "carriage return at end of file", in: "trailing\r", lf: "trailing\n"},
		{name: "carriage return alone", in: "\r", lf: "\n"},
		{name: "empty file", in: "", lf: ""},
		{name: "CRLF inside a raw string literal", in: "q := `SELECT 1\r\nFROM t`", lf: "q := `SELECT 1\nFROM t`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, want := OfSource([]byte(tt.in)), OfSource([]byte(tt.lf))
			if got != want {
				t.Errorf("OfSource(%q) = %s\nwant the LF form's %s", tt.in, got, want)
				t.Log("git rewrites line endings on checkout and on checkin, so one commit")
				t.Log("produces different bytes on a Windows laptop and on Linux CI. Calling")
				t.Log("that drift is an accusation nobody can act on: git diff shows nothing,")
				t.Log("and invariant #6 offers no repair flag.")
			}
		})
	}
}

func TestAnyOtherWhitespaceChangeIsARewrite(t *testing.T) {
	const base = "func Up(s *migr.Schema) error {\n\treturn s.CreateTable(\"users\")\n}\n"

	tests := []struct {
		name string
		in   string
	}{
		{name: "trailing space added", in: "func Up(s *migr.Schema) error {\n\treturn s.CreateTable(\"users\") \n}\n"},
		{name: "tab reindented to spaces", in: "func Up(s *migr.Schema) error {\n    return s.CreateTable(\"users\")\n}\n"},
		{name: "blank line added", in: "func Up(s *migr.Schema) error {\n\n\treturn s.CreateTable(\"users\")\n}\n"},
		{name: "final newline removed", in: "func Up(s *migr.Schema) error {\n\treturn s.CreateTable(\"users\")\n}"},
		{name: "second final newline added", in: base + "\n"},
		{name: "leading space added", in: " " + base},
	}

	original := OfSource([]byte(base))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OfSource([]byte(tt.in)); got == original {
				t.Errorf("OfSource(%q) = %s, the unchanged source's digest", tt.in, got)
				t.Log("A rewrite is a rewrite. Normalization covers only what version control")
				t.Log("can change silently; anything an author typed moves the hash. Widening")
				t.Log("normalize into a whitespace trimmer would break this.")
			}
		})
	}
}

func TestAByteOrderMarkIsAnAuthoredChange(t *testing.T) {
	const source = "func Up() error { return nil }\n"

	bom := append([]byte{0xEF, 0xBB, 0xBF}, source...)

	if OfSource(bom) == OfSource([]byte(source)) {
		t.Error("a byte order mark was normalized away")
		t.Log("git never adds or removes a BOM, so a BOM appearing is a committed diff")
		t.Log("someone can see and revert. Only differences version control introduces")
		t.Log("without a visible diff are normalized, and that rule decides this case.")
	}
}

func TestNormalizationTouchesNothingWithoutACarriageReturn(t *testing.T) {
	sources := []string{
		"",
		"\n",
		"package migrations\n\nfunc Up() error {\n\treturn nil\n}\n",
		"CHECK (total >= 0 AND name <> '')",
		"tablo adı: müşteriler 🎉",
		"no trailing newline",
	}

	for _, s := range sources {
		t.Run(s, func(t *testing.T) {
			if got := normalize([]byte(s)); !bytes.Equal(got, []byte(s)) {
				t.Errorf("normalize(%q) = %q, want it returned unchanged", s, got)
			}
		})
	}
}
