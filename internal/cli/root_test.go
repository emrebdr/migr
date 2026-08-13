package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// runCLI executes the root command with args, returning stdout, stderr and error.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer

	opts := &options{}

	cmd := rootCmd(opts)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err = execute(context.Background(), cmd, opts)

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

func TestACommandLineNobodyCanTypeIsAValidationFailure(t *testing.T) {
	for _, args := range [][]string{
		{"--nonexistent"},
		{"status", "--nonexistent"},
		{"nonsense"},
		{"version", "extra"},
		{"create"},
	} {
		_, _, err := runCLI(t, args...)
		if err == nil {
			t.Fatalf("migr %v succeeded", args)
		}

		if got := ExitCode(err); got != ExitValidation {
			t.Errorf("migr %v exited %d, want %d", args, got, ExitValidation)
			t.Log("A flag that does not exist, a command that does not exist, and the wrong number")
			t.Log("of arguments are the plainest cases of the rule in doc.go: the argument is wrong,")
			t.Log("and the operator retypes it. Cobra reports all three, and an untagged error")
			t.Log("resolves to 1, which is the code for a world that is not ready.")
		}
	}
}

// repairing names the flags that would bypass checksum verification, which invariant #6 forbids.
var repairing = []string{
	"repair",
	"baseline",
	"skip-checksum",
	"ignore-drift",
	"allow-drift",
	"no-verify-checksum",
}

// clobbering names the flags that would let a command replace a file it did not write.
var clobbering = []string{"force", "overwrite", "replace"}

// visitFlags calls fn for every flag every command registers, persistent and local alike.
func visitFlags(fn func(cmd *cobra.Command, f *pflag.Flag)) {
	for _, cmd := range append(newRootCmd().Commands(), newRootCmd()) {
		for _, flags := range []*pflag.FlagSet{cmd.PersistentFlags(), cmd.Flags()} {
			flags.VisitAll(func(f *pflag.Flag) { fn(cmd, f) })
		}
	}
}

func TestNoFlagOverwritesAFileMigrDidNotWrite(t *testing.T) {
	visitFlags(func(cmd *cobra.Command, f *pflag.Flag) {
		if !slices.Contains(clobbering, f.Name) {
			return
		}

		t.Errorf("migr %s registers --%s", cmd.Name(), f.Name)
		t.Log("Every file migr writes is created with O_EXCL, and init refuses a config that is")
		t.Log("already there. A flag that turns that off replaces a file naming somebody's")
		t.Log("databases, or a migration whose checksum is already recorded, and neither is")
		t.Log("recoverable from the tool that did it.")
	})
}

func TestNoFlagRepairsARecordedChecksum(t *testing.T) {
	visitFlags(func(cmd *cobra.Command, f *pflag.Flag) {
		if !slices.Contains(repairing, f.Name) {
			return
		}

		t.Errorf("migr %s registers --%s", cmd.Name(), f.Name)
		t.Log("Invariant #6: applied migrations are immutable, a checksum mismatch is a")
		t.Log("hard error, and any repair path must be an explicit, separately-named")
		t.Log("command that says exactly what it is changing -- never a flag on up or")
		t.Log("down, where it silently rewrites what the next run compares against.")
	})
}
