package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/remedy"
)

// update rewrites testdata/failures.txt from what every failure now renders, for make golden-update.
var update = flag.Bool("update", false, "rewrite testdata/failures.txt from the failures as rendered")

// said is a failure as an operator reads it: the message, then what to do next.
func said(err error) string {
	return Rendered(err)
}

// failuresGolden is every failure a command can end in, as an operator reads it on stderr.
const failuresGolden = "testdata/failures.txt"

// ending is one failing case of either table, as run and as rendered.
type ending struct {
	// title says which command line and which case, which is the golden file's header.
	title string
	// rendered is the failure as stderr carries it, the project's own path replaced.
	rendered string
	// err is what the command returned.
	err error
}

// endings runs every failing case of the exit-code matrix and of the named table, in table order.
func endings(t *testing.T) []ending {
	t.Helper()

	var found []ending

	for _, c := range codes {
		if c.want == ExitOK {
			continue
		}

		dir, _, drivers := c.stage(t)
		_, _, err := runOptions(t, dir, &options{clock: ticking(noon), drivers: drivers,
			runtimes: registryFor(t, c.scripted, c.declares)},
			append(strings.Fields(c.command), c.args...)...)

		found = append(found, ending{title: fmt.Sprintf("migr %s: %s (exit %d)",
			strings.Join(append(strings.Fields(c.command), c.args...), " "), c.name, ExitCode(err)),
			rendered: placeless(dir, Rendered(err)), err: err})
	}

	for _, c := range named {
		dir, _, drivers := c.stage(t)
		_, _, err := runWith(t, dir, noon, drivers, c.args...)

		found = append(found, ending{title: fmt.Sprintf("migr %s: %s (exit %d)",
			strings.Join(c.args, " "), c.name, ExitCode(err)),
			rendered: placeless(dir, Rendered(err)), err: err})
	}

	return found
}

// backoff is how long a run backed off between lock attempts, which the jitter makes differ every run.
var backoff = regexp.MustCompile(`[0-9.]+(ns|µs|ms|s) of backoff`)

// placeless replaces the project's temporary directory and the jittered backoff with placeholders.
func placeless(dir, text string) string {
	text = backoff.ReplaceAllString(text, "<jittered> of backoff")

	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		text = strings.ReplaceAll(text, resolved, "<project>")
	}

	return strings.ReplaceAll(text, dir, "<project>")
}

func TestEveryFailureSaysWhatToDoNext(t *testing.T) {
	path, err := filepath.Abs(failuresGolden)
	if err != nil {
		t.Fatalf("resolving %s: %v", failuresGolden, err)
	}

	var golden strings.Builder

	for _, e := range endings(t) {
		if e.err == nil {
			t.Errorf("%s: the command succeeded", e.title)

			continue
		}

		if remedy.Of(e.err) == "" {
			t.Errorf("%s: the failure says what failed and nothing about what to do next:\n%s",
				e.title, e.rendered)
		}

		if got := failureOf(e.err).Next; got != remedy.Of(e.err) {
			t.Errorf("%s: --json carries next %q, want %q", e.title, got, remedy.Of(e.err))
		}

		fmt.Fprintf(&golden, "== %s\n%s\n", e.title, e.rendered)
	}

	compareFailures(t, path, golden.String())

	t.Log("Invariant #9: a failure names what failed, why, and what to do next. The first two are")
	t.Log("prose and are read here, in testdata/failures.txt; the third is held by this test.")
}

// compareFailures holds the rendered failures to the golden file at path, or rewrites it under -update.
func compareFailures(t *testing.T, path, got string) {
	t.Helper()

	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("writing %s: %v", failuresGolden, err)
		}

		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s (make golden-update writes it): %v", failuresGolden, err)
	}

	if got != string(want) {
		t.Errorf("a failure renders differently from %s; read the difference, then make golden-update",
			failuresGolden)
		t.Log(lineDifference(string(want), got))
	}
}

// lineDifference names the first line two renderings disagree on, which is where to start reading.
func lineDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")

	for i := 0; i < len(w) && i < len(g); i++ {
		if w[i] != g[i] {
			return fmt.Sprintf("line %d:\n  want %s\n  got  %s", i+1, w[i], g[i])
		}
	}

	return fmt.Sprintf("one rendering is longer: %d lines against %d", len(w), len(g))
}
