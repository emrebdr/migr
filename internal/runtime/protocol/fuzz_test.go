package protocol

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func FuzzDecode(f *testing.F) {
	for _, seed := range seeds(f) {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, line string) {
		m, err := Decode([]byte(line))
		if err != nil {
			assertReportable(t, err)

			return
		}

		if err := m.Validate(); err != nil {
			t.Fatalf("Decode returned a message that does not validate: %v", err)
		}

		once, err := Encode(m)
		if err != nil {
			t.Fatalf("Decode(%q) returned a message that will not encode: %v", line, err)
		}

		again, err := Decode(once)
		if err != nil {
			t.Fatalf("the re-encoded %q does not decode: %v", once, err)
		}

		twice, err := Encode(again)
		if err != nil {
			t.Fatalf("the twice-decoded message will not encode: %v", err)
		}

		if !bytes.Equal(once, twice) {
			t.Fatalf("encoding is not stable\n  once:  %s\n  twice: %s", once, twice)
		}
	})
}

func FuzzRead(f *testing.F) {
	for _, seed := range seeds(f) {
		f.Add([]byte(seed + "\n"))
	}

	f.Add([]byte("\n\n\n"))
	f.Add([]byte("\xef\xbb\xbf{}"))
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\r\n"))

	f.Fuzz(func(t *testing.T, stream []byte) {
		r := NewReader(bytes.NewReader(stream))

		for range len(stream) + 2 {
			m, err := r.Read()
			if err != nil {
				assertSpent(t, r, err)

				return
			}

			if err := m.Validate(); err != nil {
				t.Fatalf("Read returned a message that does not validate: %v", err)
			}
		}

		t.Fatalf("the reader neither ended nor consumed %d bytes of input", len(stream))
	})
}

// seeds are the lines the corpus starts from: the specification's own, and every refusal.
func seeds(f *testing.F) []string {
	f.Helper()

	corpus := []string{
		"",
		"{",
		"[]",
		"null",
		strings.Repeat("a", 1024),
		`{"jsonrpc":"2.0","id":9223372036854775808,"method":"shutdown","params":{}}`,
		`{"jsonrpc":"2.0","id":-1,"method":"shutdown","params":{}}`,
		`{"jsonrpc":"2.0","method":"$/cancel","params":{"id":3}}`,
	}

	for _, m := range readSpec(f).messages {
		corpus = append(corpus, m.body)
	}

	for _, c := range refusals() {
		corpus = append(corpus, c.wire)
	}

	return corpus
}

// assertReportable reports whether a failure is one the peer can be told about, or a clean end.
func assertReportable(t *testing.T, err error) {
	t.Helper()

	if errors.Is(err, io.EOF) {
		return
	}

	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("the failure is %T and neither reportable nor an ended stream: %v", err, err)
	}

	if !e.Code.Reserved() && !e.Code.Valid() {
		t.Fatalf("the failure carries code %d, which this protocol version does not assign", e.Code)
	}

	if e.Slug() == "" {
		t.Fatalf("the failure carries no data.slug, which §7.1 requires: %v", err)
	}

	if Refuse(err) == nil {
		t.Fatalf("a reportable failure builds the message §2.6 permits, and this built none: %v", err)
	}
}

// assertSpent reports whether a reader that failed stays failed, since §2.6 allows no recovery.
func assertSpent(t *testing.T, r *Reader, err error) {
	t.Helper()

	assertReportable(t, err)

	if _, again := r.Read(); !errors.Is(again, err) {
		t.Fatalf("the reader answered %v and then %v, and a spent reader repeats itself", err, again)
	}
}
