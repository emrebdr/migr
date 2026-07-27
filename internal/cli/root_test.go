package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// runCLI executes the root command with args, returning stdout, stderr and error.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer

	cmd := newRootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err = cmd.ExecuteContext(context.Background())

	return out.String(), errOut.String(), err
}

func TestHelpListsCommandsAndGlobalFlags(t *testing.T) {
	stdout, _, err := runCLI(t, "--help")
	if err != nil {
		t.Fatalf("--help returned error: %v", err)
	}

	for _, want := range []string{"--config", "--database", "--json", "--verbose", "version"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--help output missing %q\ngot:\n%s", want, stdout)
		}
	}
}

func TestVersionPrintsHumanReadableLine(t *testing.T) {
	stdout, _, err := runCLI(t, "version")
	if err != nil {
		t.Fatalf("version returned error: %v", err)
	}

	if !strings.HasPrefix(stdout, "migr ") {
		t.Errorf("version output should start with %q, got %q", "migr ", stdout)
	}
}

func TestVersionJSONIsValidAndOnStdout(t *testing.T) {
	stdout, stderr, err := runCLI(t, "version", "--json")
	if err != nil {
		t.Fatalf("version --json returned error: %v", err)
	}

	var info struct {
		Version  string `json:"version"`
		Go       string `json:"go"`
		Platform string `json:"platform"`
	}

	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\ngot: %q", err, stdout)
	}

	if info.Version == "" || info.Go == "" || info.Platform == "" {
		t.Errorf("version JSON has empty required fields: %+v", info)
	}

	if stderr != "" {
		t.Errorf("nothing should reach stderr at default verbosity, got %q", stderr)
	}
}

func TestVerbosityGatesLogLevel(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantLog   bool
		wantLevel string
	}{
		{name: "default is quiet", args: []string{"version"}, wantLog: false},
		{name: "-v enables info", args: []string{"version", "-v"}, wantLog: true, wantLevel: "INFO"},
		{name: "-vv enables debug", args: []string{"version", "-vv"}, wantLog: true, wantLevel: "DEBUG"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, err := runCLI(t, tt.args...)
			if err != nil {
				t.Fatalf("returned error: %v", err)
			}

			if tt.wantLog && !strings.Contains(stderr, tt.wantLevel) {
				t.Errorf("expected %s log on stderr, got %q", tt.wantLevel, stderr)
			}

			if !tt.wantLog && stderr != "" {
				t.Errorf("expected no log output, got %q", stderr)
			}
		})
	}
}

func TestJSONFlagSwitchesLogFormatToo(t *testing.T) {
	_, stderr, err := runCLI(t, "version", "--json", "-v")
	if err != nil {
		t.Fatalf("returned error: %v", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if line == "" {
			continue
		}

		if !json.Valid([]byte(line)) {
			t.Errorf("log line is not valid JSON with --json: %q", line)
		}
	}
}

func TestStdoutStaysCleanWhenLogsAreOn(t *testing.T) {
	stdout, stderr, err := runCLI(t, "version", "--json", "-vv")
	if err != nil {
		t.Fatalf("returned error: %v", err)
	}

	if stderr == "" {
		t.Fatal("expected debug logs on stderr")
	}

	var discard map[string]any
	if err := json.Unmarshal([]byte(stdout), &discard); err != nil {
		t.Errorf("logs leaked into stdout, breaking the JSON document: %v\ngot: %q", err, stdout)
	}
}

func TestLevelFor(t *testing.T) {
	tests := []struct {
		verbosity int
		want      slog.Level
	}{
		{verbosity: 0, want: slog.LevelWarn},
		{verbosity: 1, want: slog.LevelInfo},
		{verbosity: 2, want: slog.LevelDebug},
		{verbosity: 9, want: slog.LevelDebug},
	}

	for _, tt := range tests {
		if got := levelFor(tt.verbosity); got != tt.want {
			t.Errorf("levelFor(%d) = %v, want %v", tt.verbosity, got, tt.want)
		}
	}
}

func TestUnknownFlagFails(t *testing.T) {
	_, _, err := runCLI(t, "--nonexistent")
	if err == nil {
		t.Fatal("expected an error for an unknown flag")
	}

	if got := ExitCode(err); got != ExitRuntime {
		t.Errorf("ExitCode = %d, want %d", got, ExitRuntime)
	}
}

func TestVersionRejectsPositionalArgs(t *testing.T) {
	if _, _, err := runCLI(t, "version", "extra"); err == nil {
		t.Fatal("expected an error for unexpected positional argument")
	}
}
