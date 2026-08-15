package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestEveryTimeoutIsTheOneTheSpecificationTabulates(t *testing.T) {
	tabulated := tabulatedTimeouts(t)

	for key, want := range map[string]time.Duration{
		"runtime.handshake_timeout": DefaultHandshake,
		"runtime.request_timeout":   DefaultRequest,
		"runtime.cancel_grace":      DefaultCancelGrace,
		"runtime.shutdown_grace":    DefaultShutdownGrace,
	} {
		got, documented := tabulated[key]
		if !documented {
			t.Errorf("%s §8.2 tabulates no row for %s", specPath, key)

			continue
		}

		if got != want {
			t.Errorf("%s §8.2 says %s defaults to %s, and this build uses %s",
				specPath, key, got, want)
			t.Log("These defaults are the protocol's, so a build that carries its own would let two")
			t.Log("installations disagree about what the handshake timeout is while both claiming")
			t.Log("to implement version 1.")
		}
	}
}

func TestTheOneCallWithNoTimeoutHasNoKeyToSetEither(t *testing.T) {
	if NoTimeout != 0 {
		t.Errorf("NoTimeout is %s, and a bound of none is the zero one", NoTimeout)
	}

	row := timeoutRow(t, "`migration.execute`")
	if len(row) < 3 || row[1] != "none" {
		t.Fatalf("%s §8.2 offers migration.execute the key %q", specPath, row)
	}

	if !strings.Contains(row[2], "none") {
		t.Errorf("%s §8.2 gives migration.execute the default %q", specPath, row[2])
		t.Log("§8.2: a backfill over a large table legitimately runs for hours, so a default that")
		t.Log("killed it would be a data-loss bug wearing a safety flag. A key would be the same")
		t.Log("bug with an operator's name on it.")
	}
}

// tabulatedTimeouts reads §8.2's table as the migr.yaml key each default is surfaced under.
func tabulatedTimeouts(t *testing.T) map[string]time.Duration {
	t.Helper()

	found := make(map[string]time.Duration, 4)

	for _, cells := range timeoutRows(t) {
		key, ok := strings.CutPrefix(cells[1], "`")
		if !ok {
			continue
		}

		key = strings.TrimSuffix(key, "`")

		bound, err := time.ParseDuration(cells[2])
		if err != nil {
			t.Errorf("%s §8.2 states the default for %s as %q, which is not a duration",
				specPath, key, cells[2])

			continue
		}

		found[key] = bound
	}

	return found
}

// timeoutRow returns the one §8.2 row whose first cell is the given text, failing when there is none.
func timeoutRow(t *testing.T, what string) []string {
	t.Helper()

	for _, cells := range timeoutRows(t) {
		if cells[0] == what {
			return cells
		}
	}

	t.Fatalf("%s §8.2 tabulates no row for %s", specPath, what)

	return nil
}

// timeoutRows returns §8.2's table as cells, which is the only table carrying that heading.
func timeoutRows(t *testing.T) [][]string {
	t.Helper()

	var (
		rows   [][]string
		inside bool
	)

	for _, line := range readSpecLines(t) {
		if strings.HasPrefix(line, "### 8.2") {
			inside = true

			continue
		}

		if inside && strings.HasPrefix(line, "#") {
			break
		}

		if cells, ok := tableRow(line); inside && ok {
			rows = append(rows, cells)
		}
	}

	if len(rows) == 0 {
		t.Fatalf("%s carries no §8.2 timeout table at all", specPath)
	}

	return rows
}

// tableRow splits one markdown row into its cells, skipping the heading and the rule beneath it.
func tableRow(line string) ([]string, bool) {
	if !strings.HasPrefix(line, "| ") {
		return nil, false
	}

	cells := strings.Split(strings.Trim(line, "|"), "|")
	for i, cell := range cells {
		cells[i] = strings.TrimSpace(cell)
	}

	if len(cells) < 3 || cells[0] == "What" || strings.HasPrefix(cells[0], "---") {
		return nil, false
	}

	return cells, true
}
