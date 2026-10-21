//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// cutoverTable keeps the runs a restarted hold leaves apart from every other shadow suite's.
const cutoverTable = "migr_e2e_cutover_migrations"

// unattendedTable keeps the unattended run apart from the confirmed one.
const unattendedTable = "migr_e2e_unattended_migrations"

// holder is migr up running as a process of its own, which is the only kind a test can kill.
type holder struct {
	// cmd is the process.
	cmd *exec.Cmd
	// out is what it printed on both streams, read only once it has exited.
	out bytes.Buffer
	// done delivers how it exited.
	done chan error
}

// startHolder starts migr up as a process of its own, which then holds the shadow table in sync.
func startHolder(t *testing.T, config string) *holder {
	t.Helper()

	h := &holder{cmd: exec.Command(os.Args[0], "--config", config, "up"), done: make(chan error, 1)}
	h.cmd.Env = append(os.Environ(), asBinary+"=1")
	h.cmd.Stdout, h.cmd.Stderr = &h.out, &h.out

	if err := h.cmd.Start(); err != nil {
		t.Fatalf("starting migr up: %v", err)
	}

	go func() { h.done <- h.cmd.Wait() }()

	t.Cleanup(func() { _ = h.cmd.Process.Kill() })

	return h
}

// exited waits for the process to end and returns its exit code and what it printed.
func (h *holder) exited(t *testing.T, within time.Duration) (int, string) {
	t.Helper()

	select {
	case err := <-h.done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), h.out.String()
		}

		if err != nil {
			t.Fatalf("waiting for migr up: %v", err)
		}

		return 0, h.out.String()
	case <-time.After(within):
		t.Fatalf("migr up did not exit within %s", within)

		return 0, ""
	}
}

// listedCutover is the one row migr cutover --json lists, as this suite reads it.
type listedCutover struct {
	State  string `json:"state"`
	Shadow struct {
		Behind *float64  `json:"behind_ms"`
		ReadAt time.Time `json:"read_at"`
		Held   *bool     `json:"held"`
	} `json:"shadow"`
}

// cutoverRow reads migr cutover --json, returning the one migration listed, if any is.
func cutoverRow(t *testing.T, config string) (listedCutover, bool) {
	t.Helper()

	out, err := migrReport(t, config, "--json", "cutover")
	if err != nil {
		t.Fatalf("migr cutover --json: %v\n%s", err, out)
	}

	var doc struct {
		Shadows []listedCutover `json:"shadows"`
	}

	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("migr cutover --json is not a document: %v\n%s", err, out)
	}

	if len(doc.Shadows) != 1 {
		return listedCutover{}, false
	}

	return doc.Shadows[0], true
}

// awaitReading polls migr cutover until a waiting migration's reading is held or stale, as asked, and newer than since.
func awaitReading(t *testing.T, config string, held bool, since time.Time, within time.Duration) listedCutover {
	t.Helper()

	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(time.Second) {
		row, listed := cutoverRow(t, config)
		if listed && row.State == "waiting" && row.Shadow.Held != nil && *row.Shadow.Held == held &&
			row.Shadow.Behind != nil && (row.Shadow.ReadAt.After(since) || !held) {
			return row
		}
	}

	t.Fatalf("migr cutover never reported a waiting migration whose reading is held=%v within %s", held, within)

	return listedCutover{}
}

func TestAHeldShadowTableSurvivesADetachAndAKillAndCutsOverOnlyOnceMigrCutoverConfirmsIt(t *testing.T) {
	config := shadowProjectIn(t, "mysql", mysql80DSN, cutoverTable)
	conn := mysqlSession(t, cutoverTable)

	t.Cleanup(func() {
		if err := migr(t, config, "resolve", "--rolled-back", "--reason", "a failed e2e case"); err != nil {
			t.Errorf("abandoning what a failed case left waiting: %v", err)
		}

		if err := migr(t, config, "down", "--step", "2"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}

		retireLeft(t, conn)
	})

	first := startHolder(t, config)
	awaitReading(t, config, true, time.Time{}, 3*time.Minute)

	if err := first.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupting the hold: %v", err)
	}

	if code, out := first.exited(t, time.Minute); code != 6 || !strings.Contains(out, "is waiting for its cutover") {
		t.Fatalf("a detached hold exited %d, want 6:\n%s", code, out)
	}

	if state := stateOf(t, migrOut(t, config, "status"), "index_e2e_shadow_people"); state != "waiting" {
		t.Fatalf("a detached hold reconciles to %q, want waiting", state)
	}

	restarted := time.Now()
	second := startHolder(t, config)
	awaitReading(t, config, true, restarted, 2*time.Minute)

	listed := migrOut(t, config, "cutover")
	for _, want := range []string{"held in sync through the shadow table", "trailed the log by",
		"migr cutover --confirm " + shadowVersion} {
		if !strings.Contains(listed, want) {
			t.Errorf("migr cutover does not say %q:\n%s", want, listed)
		}
	}

	if err := second.cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the hold: %v", err)
	}

	second.exited(t, time.Minute)

	awaitReading(t, config, false, time.Time{}, 2*time.Minute)

	if stale := migrOut(t, config, "cutover"); !strings.Contains(stale, "no run is holding the shadow table") {
		t.Errorf("a killed holder's reading is reported as current:\n%s", stale)
	}

	restarted = time.Now()
	third := startHolder(t, config)
	awaitReading(t, config, true, restarted, 2*time.Minute)

	confirmed := migrOut(t, config, "cutover", "--confirm", shadowVersion)
	if !strings.Contains(confirmed, "confirmed the cutover of "+shadowVersion) ||
		!strings.Contains(confirmed, "The run holding it reads the confirmation") {
		t.Errorf("the confirmation says:\n%s", confirmed)
	}

	if code, out := third.exited(t, 2*time.Minute); code != 0 || !strings.Contains(out, "cut over through a shadow table") {
		t.Fatalf("the confirmed hold exited %d:\n%s", code, out)
	}

	if state := stateOf(t, migrOut(t, config, "status"), "index_e2e_shadow_people"); state != "applied" {
		t.Errorf("the cut-over migration is %q, want applied", state)
	}

	t.Log("A hold survives two processes ending: Ctrl-C exits 6 leaving it waiting, SIGKILL leaves a")
	t.Log("reading migr cutover reports as stale, and only migr cutover --confirm ends the third.")
}

func TestUnattendedCutsOverOnMySQLWithNothingConfirmed(t *testing.T) {
	config := shadowProjectIn(t, "mysql", mysql80DSN, unattendedTable)
	conn := mysqlSession(t, unattendedTable)

	t.Cleanup(func() {
		if err := migr(t, config, "down", "--step", "2"); err != nil {
			t.Errorf("leaving the server as this suite found it: %v", err)
		}

		retireLeft(t, conn)
	})

	out := migrOut(t, config, "up", "--unattended")
	if !strings.Contains(out, "cut over unattended through a shadow table") {
		t.Errorf("an unattended up does not say it cut over unattended:\n%s", out)
	}

	if state := stateOf(t, migrOut(t, config, "status"), "index_e2e_shadow_people"); state != "applied" {
		t.Errorf("the unattended migration is %q, want applied", state)
	}
}
