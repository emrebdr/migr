package migrtest

import (
	"context"
	"testing"

	migr "github.com/emrebdr/migr/sdk/go"
)

// placeholder is the version a capture runs under, a migration handed over as a value having no file.
//
// Go's own reference time, which reads as a stand-in to anyone who has formatted a date in this
// language and is a real UTC instant rather than a string the migration grammar would refuse.
const placeholder = "20060102T150405"

// What the helpers report through, and why it is not *testing.T all the way down.
//
// The exported functions take a *testing.T, because that is what a user has and because
// drivertest.Suite.Run does. Threading the narrow thing underneath is what lets this package's own
// tests assert what a helper reports rather than be failed by it.

// reporter is what these helpers report through, a *testing.T everywhere but their own tests.
type reporter interface {
	// Helper marks the caller as a helper, so a failure names the line that asked.
	Helper()
	// Context reports the context a half runs under, which ends with the test.
	Context() context.Context
	// Errorf reports a finding and carries on.
	Errorf(format string, args ...any)
	// Fatalf reports a finding that leaves nothing to assert.
	Fatalf(format string, args ...any)
	// Log records the argument a finding rests on, which is printed beside it.
	Log(args ...any)
}

// Stage is what a capture answers a migration's own questions with, and the mode it runs it in.
type Stage struct {
	// Mode is whether the half is run or asked, and is apply unless a test says otherwise.
	Mode migr.Mode
	// Migration is the version the window reports, and is a placeholder unless a test names one.
	Migration string
	// Reads answers each statement the migration reads with, keyed by the SQL it sends.
	Reads map[string]migr.Rows
	// Affected is what a statement the migration sends itself reports changing, keyed by its SQL.
	Affected map[string]string
	// Info is what this run is against, which is refused rather than guessed when it is nil.
	Info *migr.Info
	// Verdicts answer what the engine makes of an operation, keyed by the kind asked about.
	Verdicts map[migr.Kind]migr.Verdict
}

// Capture is what one half of a migration did: the IR it emitted, and every callback it made.
type Capture struct {
	// Operations are the IR documents the engine would have received, in the order they were sent.
	Operations []string
	// Reads are the statements the migration read with, in order.
	Reads []Statement
	// Statements are the statements the migration sent itself, which no rollback undoes.
	Statements []Statement
	// Logs are the lines it filed on the engine's logger.
	Logs []Line
}

// Up captures what a migration's up emits, run in apply mode against an empty stage.
func Up(t *testing.T, m migr.Migration) Capture {
	t.Helper()

	return Stage{}.up(t, m)
}

// Down captures what a migration's own down emits, failing one that authors none.
func Down(t *testing.T, m migr.Migration) Capture {
	t.Helper()

	return Stage{}.down(t, m)
}

// Up captures what a migration's up emits against this stage.
func (s Stage) Up(t *testing.T, m migr.Migration) Capture {
	t.Helper()

	return s.up(t, m)
}

// Down captures what a migration's own down emits against this stage, failing one that authors none.
func (s Stage) Down(t *testing.T, m migr.Migration) Capture {
	t.Helper()

	return s.down(t, m)
}

// up runs the half every migration has.
func (s Stage) up(r reporter, m migr.Migration) Capture {
	r.Helper()

	return s.half(r, m, migr.Up, m.Up)
}

// down runs the half a migration may not have, failing one that authors none.
func (s Stage) down(r reporter, m migr.Migration) Capture {
	r.Helper()

	down, writes := m.(migr.Downer)
	if !writes {
		r.Fatalf("%T authors no down of its own: %s", m, undoneBy(m))

		return Capture{}
	}

	return s.half(r, m, migr.Down, down.Down)
}

// undoneBy says what undoes a migration that writes no down, which is what to assert instead.
func undoneBy(m migr.Migration) string {
	undo, reason := migr.Undone(m)

	if undo == migr.UndoDeclared {
		return "it declares that it cannot be undone (" + reason + "), so there is no down to capture"
	}

	return "the engine derives its inverse from whatever its up emits, which is Reversal's to report"
}

// half runs one direction against a recording host, and reports everything that host was asked.
func (s Stage) half(r reporter, m migr.Migration, dir migr.Direction,
	run func(context.Context, *migr.Schema) error) Capture {
	r.Helper()

	host := &recorder{stage: s, mode: s.mode()}
	window := migr.NewWindow(s.version(), dir, s.mode(), host)

	err := run(r.Context(), window.Schema())
	_, forgotten := window.Seal()

	if err != nil {
		r.Fatalf("%T %s: %v", m, dir, err)
	}

	if forgotten != nil {
		r.Fatalf("%T %s: %v", m, dir, forgotten)
	}

	if refused := host.problem(); refused != nil {
		r.Fatalf("%T %s asked something this capture could not answer: %v", m, dir, refused)
	}

	return host.captured()
}

// mode reports which mode this stage runs a half in, which is a run unless it says otherwise.
func (s Stage) mode() migr.Mode {
	if s.Mode == migr.ModePlan {
		return migr.ModePlan
	}

	return migr.ModeApply
}

// version reports the version the window reports, which a migration may read and nothing parses.
func (s Stage) version() string {
	if s.Migration != "" {
		return s.Migration
	}

	return placeholder
}

// Is holds a capture to having emitted exactly these operations, as documents the engine receives.
func (c Capture) Is(t *testing.T, want ...migr.Operation) {
	t.Helper()

	c.is(t, want...)
}

// is compares the bytes, which is what makes an assertion about IR an assertion about the wire.
func (c Capture) is(r reporter, want ...migr.Operation) {
	r.Helper()

	documents := documentsOf(r, want)

	if len(c.Operations) != len(documents) {
		r.Fatalf("this half emitted %d operations, want %d:\n got %v\nwant %v",
			len(c.Operations), len(documents), c.Operations, documents)

		return
	}

	for i, document := range documents {
		if c.Operations[i] != document {
			r.Errorf("operation %d:\n got %s\nwant %s", i+1, c.Operations[i], document)
		}
	}
}

// Kinds names each operation a capture holds, for an assertion about shape rather than bytes.
func (c Capture) Kinds() []migr.Kind {
	kinds := make([]migr.Kind, 0, len(c.Operations))

	for _, document := range c.Operations {
		kinds = append(kinds, kindOf(document))
	}

	return kinds
}

// Documents renders operations as the documents the engine receives, through the SDK's own encoder.
func Documents(t *testing.T, ops ...migr.Operation) []string {
	t.Helper()

	return documentsOf(t, ops)
}

// documentsOf renders expected operations the same way the emitted ones were rendered.
func documentsOf(r reporter, ops []migr.Operation) []string {
	r.Helper()

	host := &recorder{mode: migr.ModeApply}
	window := migr.NewWindow(placeholder, migr.Up, migr.ModeApply, host)

	if err := window.Schema().Apply(r.Context(), ops...); err != nil {
		r.Fatalf("these are not operations a migration could emit: %v", err)
	}

	if _, forgotten := window.Seal(); forgotten != nil {
		r.Fatalf("rendering operations: %v", forgotten)
	}

	return host.captured().Operations
}

// Answer builds what a staged read returns, every value being the server's own text.
func Answer(columns []string, rows ...[]string) migr.Rows {
	values := make([][]*string, 0, len(rows))

	for _, row := range rows {
		cells := make([]*string, 0, len(row))

		for i := range row {
			cells = append(cells, &row[i])
		}

		values = append(values, cells)
	}

	return migr.Rows{Columns: columns, Values: values}
}
