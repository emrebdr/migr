package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// deadline is how long any single read may take before the test calls it a hang.
const deadline = 10 * time.Second

func TestAMessageRoundTripsOverARealPipe(t *testing.T) {
	in, out := pipe(t)

	w := NewWriter(out)
	r := NewReader(in)

	sent := NewRequest(3, MigrationExecute,
		json.RawMessage(`{"id":"20260727T153000","direction":"up","mode":"apply"}`))

	if err := w.Write(sent); err != nil {
		t.Fatalf("writing: %v", err)
	}

	got := readWithin(t, r)

	if got.Method != MigrationExecute || got.ID.String() != "3" {
		t.Errorf("read back %s %s", got.ID, got.Method)
	}

	if string(got.Params) != string(sent.Params) {
		t.Errorf("params crossed the pipe as %s", got.Params)
	}
}

func TestTheWriterFlushesEveryMessage(t *testing.T) {
	in, out := pipe(t)

	w := NewWriter(out)
	r := NewReader(in)

	if err := w.Write(NewNotification(HostLog,
		json.RawMessage(`{"level":"info","message":"still here"}`))); err != nil {
		t.Fatalf("writing: %v", err)
	}

	got := readWithin(t, r)

	if got.Method != HostLog {
		t.Errorf("read back %q", got.Method)
	}
}

func TestConcurrentWritersDoNotInterleave(t *testing.T) {
	var buf syncBuffer

	w := NewWriter(&buf)

	const writers = 16

	var wg sync.WaitGroup

	wg.Add(writers)

	for i := range writers {
		go func() {
			defer wg.Done()

			if err := w.Write(NewResponse(int64(i+1), json.RawMessage(`{"recorded":1}`))); err != nil {
				t.Errorf("writing %d: %v", i+1, err)
			}
		}()
	}

	wg.Wait()

	r := NewReader(strings.NewReader(buf.String()))
	seen := make(map[string]bool, writers)

	for range writers {
		m := readWithin(t, r)
		seen[m.ID.String()] = true
	}

	if len(seen) != writers {
		t.Errorf("%d writers produced %d distinct messages", writers, len(seen))
	}
}

func TestMalformedInputIsRefusedWithoutHanging(t *testing.T) {
	for _, c := range refusals() {
		t.Run(c.name, func(t *testing.T) {
			if c.wire == "" {
				t.Skip("an empty line is skipped rather than refused")
			}

			r := NewReader(strings.NewReader(c.wire + "\n"))
			assertCode(t, refusalWithin(t, r), c.code)
		})
	}
}

func TestAStreamThatNeverEndsALineIsRefusedAtTheBudget(t *testing.T) {
	r := NewReader(endless{'a'})

	assertCode(t, refusalWithin(t, r), CodeParse)
}

func TestALineLongerThanTheBudgetIsRefused(t *testing.T) {
	line := append(bytes.Repeat([]byte("a"), MaxLine+1), '\n')
	r := NewReader(bytes.NewReader(line))

	err := refusalWithin(t, r)
	assertCode(t, err, CodeParse)
	assertNames(t, err, "longer than")
}

func TestTheLineBudgetIsTheOneTheSpecificationStates(t *testing.T) {
	const preamble = "A reader must accept lines up to"

	for _, line := range readSpecLines(t) {
		if !strings.Contains(line, preamble) {
			continue
		}

		stated, ok := bolded(line)
		if !ok {
			t.Fatalf("%s states the budget as %q, which this cannot read", specPath, line)
		}

		if want := describeBudget(MaxLine); stated != want {
			t.Errorf("%s states a %s budget and this build allows %s", specPath, stated, want)
			t.Log("The budget is a number a third-party SDK reads out of the document and")
			t.Log("hard-codes. The two drifting apart is a stream one side will not read.")
		}

		return
	}

	t.Fatalf("%s no longer states a line budget at all", specPath)
}

func TestAnEncodedMessageNeverCarriesANewline(t *testing.T) {
	m := NewNotification(HostLog, json.RawMessage("{\n  \"level\": \"info\",\n  \"message\": \"x\"\n}"))

	line, err := Encode(m)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	if bytes.ContainsAny(line, "\n\r") {
		t.Errorf("a payload's own whitespace reached the wire: %s", line)
	}

	if err := checkLine([]byte("{\"a\":1}\n{\"b\":2}")); err == nil {
		t.Error("a line carrying a newline would arrive as two messages, so it is refused")
		t.Log("Encode compacts a json.RawMessage, so this guard cannot fire through it today.")
		t.Log("It is what stands between a payload and a split message if that ever changes.")
	}
}

func TestHTMLCharactersCrossTheWireUnescaped(t *testing.T) {
	wire := `{"jsonrpc":"2.0","id":1,"method":"host.db.query","params":{"sql":"SELECT * FROM t WHERE a < b AND c > d & e","args":[]}}`

	assertRoundTrip(t, wire)
}

func TestALineExactlyTheBudgetIsAccepted(t *testing.T) {
	m := NewNotification(HostLog, padding(t, MaxLine-1))

	line, err := Encode(m)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	if len(line)+1 != MaxLine {
		t.Fatalf("the fixture is %d bytes and the budget is %d", len(line)+1, MaxLine)
	}

	r := NewReader(bytes.NewReader(append(line, '\n')))

	if got := readWithin(t, r); got.Method != HostLog {
		t.Errorf("a line of exactly the budget must be read, and this read as %q", got.Method)
	}
}

func TestTheEncodingsSection22RulesOutAreRefused(t *testing.T) {
	message := `{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}`

	streams := []struct {
		name  string
		bytes []byte
		names string
	}{
		{"a byte-order mark", append([]byte("\xef\xbb\xbf"), message+"\n"...), "byte-order mark"},
		{"invalid UTF-8", []byte(`{"jsonrpc":"2.0","id":1,"method":"shut` + "\xff" + `down","params":{}}` + "\n"), "UTF-8"},
		{"a line cut off before its newline", []byte(message), "without a newline"},
	}

	for _, s := range streams {
		t.Run(s.name, func(t *testing.T) {
			r := NewReader(bytes.NewReader(s.bytes))

			err := refusalWithin(t, r)
			assertCode(t, err, CodeParse)
			assertNames(t, err, s.names)
		})
	}
}

func TestAnEmptyLineIsSkippedAndACarriageReturnIsStripped(t *testing.T) {
	message := `{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}`
	r := NewReader(strings.NewReader("\r\n\n" + message + "\r\n\r\n" + message + "\n"))

	for range 2 {
		if got := readWithin(t, r); got.Method != Shutdown {
			t.Errorf("read %q", got.Method)
		}
	}

	if _, err := r.Read(); !errors.Is(err, io.EOF) {
		t.Errorf("the stream ended cleanly, so the reader reports EOF and not %v", err)
	}
}

func TestAFailureNamesTheLineItWasFoundOn(t *testing.T) {
	good := `{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}`
	r := NewReader(strings.NewReader(good + "\n" + good + "\nxyzzy\n"))

	readWithin(t, r)
	readWithin(t, r)

	assertNames(t, refusalWithin(t, r), "line 3")
}

func TestAReaderIsSpentAfterAFailure(t *testing.T) {
	good := `{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}`
	r := NewReader(strings.NewReader("xyzzy\n" + good + "\n"))

	first := refusalWithin(t, r)
	assertCode(t, first, CodeParse)

	second := refusalWithin(t, r)
	if !errors.Is(second, first) {
		t.Errorf("the second read reports %v, and a spent reader reports what spent it", second)
		t.Log("§2.6: there is no recovery. A reader that reads past a line it could not frame")
		t.Log("leaves whoever was waiting on that line's unreadable id waiting forever.")
	}
}

func TestRefuseBuildsTheOneNullIDErrorAndNothingElse(t *testing.T) {
	r := NewReader(strings.NewReader("xyzzy\n"))

	m := Refuse(refusalWithin(t, r))
	if m == nil {
		t.Fatal("a framing failure is reportable, so Refuse builds the one message §2.6 permits")
	}

	if !m.ID.Null() {
		t.Errorf("the id is %s, and §2.6 permits null and nothing else", m.ID)
	}

	if err := m.Validate(); err != nil {
		t.Errorf("the refusal is itself a message this protocol defines: %v", err)
	}

	encoded, err := Encode(m)
	if err != nil {
		t.Fatalf("encoding the refusal: %v", err)
	}

	if !bytes.Contains(encoded, []byte(`"id":null`)) {
		t.Errorf("the refusal encodes as %s", encoded)
	}

	if Refuse(io.EOF) != nil {
		t.Error("a stream that ended is nobody's fault, so there is nothing to report")
	}

	if Refuse(nil) != nil {
		t.Error("no failure, no refusal")
	}
}

func TestTheWriterRefusesWhatAReaderWouldNotAccept(t *testing.T) {
	refusals := []struct {
		name string
		m    *Message
	}{
		{"nothing at all", nil},
		{"a message of no shape", &Message{}},
		{"an error carrying no slug", NewFailure(1, &Error{Code: CodeMigrationFailed, Message: "boom"})},
		{"a notification for a method that is not one", NewNotification(MigrationExecute, json.RawMessage(`{}`))},
		{"a line past the budget", NewNotification(HostLog, padding(t, MaxLine))},
	}

	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer

			if err := NewWriter(&buf).Write(c.m); err == nil {
				t.Fatalf("the writer emitted %s", buf.String())
			}

			if buf.Len() != 0 {
				t.Errorf("the writer emitted %d bytes before refusing", buf.Len())
			}
		})
	}
}

// endless is a stream of one byte, repeated, that never carries a newline.
type endless struct {
	b byte
}

// Read fills every buffer it is handed, forever.
func (e endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = e.b
	}

	return len(p), nil
}

// syncBuffer is a buffer several goroutines may write to, for asserting they do not interleave.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends under a lock of its own, so the test measures the writer's and not the buffer's.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// pipe returns the two ends of a real operating-system pipe, closed when the test ends.
func pipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	in, out, err := os.Pipe()
	if err != nil {
		t.Fatalf("opening a pipe: %v", err)
	}

	t.Cleanup(func() {
		_ = in.Close()
		_ = out.Close()
	})

	return in, out
}

// readWithin reads one message under a deadline, so a read that never returns fails as a hang.
func readWithin(t *testing.T, r *Reader) *Message {
	t.Helper()

	m, err := within(t, r)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	return m
}

// refusalWithin reads under a deadline and returns the failure the reader must produce.
func refusalWithin(t *testing.T, r *Reader) error {
	t.Helper()

	m, err := within(t, r)
	if err == nil {
		encoded, _ := Encode(m)
		t.Fatalf("the reader accepted %s", encoded)
	}

	return err
}

// within runs one read on its own goroutine, because the failure this task rules out is a hang.
func within(t *testing.T, r *Reader) (*Message, error) {
	t.Helper()

	type answer struct {
		m   *Message
		err error
	}

	done := make(chan answer, 1)

	go func() {
		m, err := r.Read()
		done <- answer{m: m, err: err}
	}()

	select {
	case a := <-done:
		return a.m, a.err
	case <-time.After(deadline):
		t.Fatalf("the reader did not answer within %s, which is the hang this task rules out", deadline)

		return nil, nil
	}
}

// assertNames reports whether a failure says which rule it refused, and not only that it did.
func assertNames(t *testing.T, err error, want string) {
	t.Helper()

	if !strings.Contains(err.Error(), want) {
		t.Errorf("the failure says %q and never mentions %q", err, want)
		t.Log("Several rules refuse with the same code, so the code alone leaves an SDK author")
		t.Log("guessing which one they broke. The message is the whole diagnosis.")
	}
}

// bolded returns the first bold run in a line of markdown prose.
func bolded(line string) (string, bool) {
	parts := strings.Split(line, "**")
	if len(parts) < 3 {
		return "", false
	}

	return parts[1], true
}

// describeBudget renders a byte count the way the specification writes one.
func describeBudget(bytes int) string {
	return strconv.Itoa(bytes>>20) + " MiB"
}

// padding builds a host.log payload whose encoded message is exactly the requested length.
func padding(t *testing.T, length int) json.RawMessage {
	t.Helper()

	const shape = `{"jsonrpc":"2.0","method":"host.log","params":{"level":"info","message":""}}`

	if length <= len(shape) {
		t.Fatalf("a %d-byte message cannot hold the shape, which is %d bytes", length, len(shape))
	}

	filler := strings.Repeat("x", length-len(shape))

	return json.RawMessage(`{"level":"info","message":"` + filler + `"}`)
}
