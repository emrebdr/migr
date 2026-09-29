package main

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/cli"
)

// The half of the end-to-end harness that names no engine, so both suites here can drive the CLI.
//
// It is untagged because SQLite's suite is: that engine needs no daemon (ADR 0050), so a whole
// migr up and migr down against a real database costs a t.TempDir() and belongs in make check.
// PostgreSQL's half stays behind //go:build integration, which may use what is here -- a tagged
// file sees untagged declarations, and not the other way round, which is why these moved.

// migrationField is where a printed run names its migration: after the batch and the ordinal.
const migrationField = 2

// directions are what a run's direction column may hold, which is what marks a line as a run.
var directions = []string{"up", "down"}

// migr runs the CLI the way the binary does, against the driver registry this binary carries.
func migr(t *testing.T, config string, args ...string) error {
	t.Helper()

	engines, err := drivers()
	if err != nil {
		t.Fatalf("building the driver registry: %v", err)
	}

	languages, err := runtimes()
	if err != nil {
		t.Fatalf("building the runtime registry: %v", err)
	}

	ctx := context.WithoutCancel(t.Context())

	return cli.Execute(ctx, engines, languages, append([]string{"--config", config}, args...))
}

// migrOut runs the CLI and returns what it printed, for the commands whose whole product is output.
func migrOut(t *testing.T, config string, args ...string) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("opening a pipe: %v", err)
	}

	captured := drained(t, r, args[0])

	stdout := os.Stdout
	os.Stdout = w
	runErr := migr(t, config, args...)
	os.Stdout = stdout

	if err := w.Close(); err != nil {
		t.Fatalf("closing the pipe: %v", err)
	}

	out := <-captured

	if runErr != nil {
		t.Fatalf("migr %s: %v\n%s", args[0], runErr, out)
	}

	return out
}

// drained reads the pipe while the command is still writing to it, rather than after it returns.
func drained(t *testing.T, r *os.File, command string) <-chan string {
	t.Helper()

	captured := make(chan string, 1)

	go func() {
		out, err := io.ReadAll(r)
		if err != nil {
			t.Errorf("reading what migr %s printed: %v", command, err)
		}

		captured <- string(out)
	}()

	return captured
}

// migrFailure runs the CLI expecting it to fail, and returns what it wrote to stderr.
func migrFailure(t *testing.T, config string, args ...string) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("opening a pipe: %v", err)
	}

	stderr := os.Stderr
	os.Stderr = w
	runErr := migr(t, config, args...)
	os.Stderr = stderr

	if err := w.Close(); err != nil {
		t.Fatalf("closing the pipe: %v", err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading what migr %s reported: %v", args[0], err)
	}

	if runErr == nil {
		t.Fatalf("migr %s succeeded, and this case is about how it fails", args[0])
	}

	return string(out)
}

// stateOf returns the state migr status printed for one migration, so a test asserts a row.
func stateOf(t *testing.T, out, name string) string {
	t.Helper()

	for line := range strings.Lines(out) {
		if strings.Contains(line, name) {
			return strings.Fields(line)[0]
		}
	}

	t.Fatalf("migr status printed no row for %s:\n%s", name, out)

	return ""
}

// countRuns counts the lines migr history printed, keyed by the migration and direction each names.
func countRuns(out string) map[string]int {
	counts := make(map[string]int)

	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) > migrationField+1 && slices.Contains(directions, fields[migrationField+1]) {
			counts[fields[migrationField]+" "+fields[migrationField+1]]++
		}
	}

	return counts
}
