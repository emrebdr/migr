package golang

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoToolchain marks a machine with no go on PATH, which is the one thing this adapter needs.
var ErrNoToolchain = errors.New("no go toolchain was found on PATH")

// ErrNoModule marks a project root that is not inside a Go module, so nothing can be built in it.
var ErrNoModule = errors.New("this project is not inside a Go module")

// ErrOutsideModule marks a migrations directory the module cannot import, having no path to it.
var ErrOutsideModule = errors.New("the migrations directory is outside the Go module")

// BuildError reports a runner the toolchain refused to build, with what it said about it.
type BuildError struct {
	// Path is the generated file the compiler was pointed at, which is stable across builds.
	Path string
	// Output is what go build wrote, which names the migration file that will not compile.
	Output string
	// Err is the failure the toolchain exited with.
	Err error
}

// Error names the generated runner and hands back what the compiler said about the module.
func (e *BuildError) Error() string {
	return fmt.Sprintf("building the go runner at %s: %v%s", e.Path, e.Err, e.details())
}

// Unwrap reports what the toolchain failed with, for a caller matching on an exit status.
func (e *BuildError) Unwrap() error {
	return e.Err
}

// details renders the compiler's own output, indented, or nothing when it said nothing at all.
func (e *BuildError) details() string {
	trimmed := strings.TrimRight(e.Output, "\n")
	if trimmed == "" {
		return ""
	}

	return ":\n  " + strings.ReplaceAll(trimmed, "\n", "\n  ")
}

// toolError reports a go invocation that failed, naming what was asked and what it answered.
func toolError(args []string, output string, err error) error {
	return fmt.Errorf("go %s: %w%s", strings.Join(args, " "), err, indentOutput(output))
}

// indentOutput renders a tool's output beneath the failure, or nothing when it produced none.
func indentOutput(output string) string {
	trimmed := strings.TrimRight(output, "\n")
	if trimmed == "" {
		return ""
	}

	return ":\n  " + strings.ReplaceAll(trimmed, "\n", "\n  ")
}
