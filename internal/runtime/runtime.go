package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/runtime/protocol"
)

// Runtime is one language's adapter: it finds a toolchain and says how to start one process.
type Runtime interface {
	// Name is the language this adapter runs, spelled as migr.yaml spells migrations.language.
	Name() string
	// Discover reports whether this machine can run this language at all, opening no process.
	Discover(ctx context.Context) error
	// Command says how to start one runtime process, and starting it is not an adapter's to do.
	Command(ctx context.Context, p Project) (Command, error)
}

// Project is what a runtime is told about the run it was started for, and carries no secret.
type Project struct {
	// Root is the directory holding migr.yaml, which docs/protocol.md §9 makes the working directory.
	Root string
	// Dir is the migrations directory, absolute, since a runtime's working directory is not it.
	Dir string
	// Language is what migr.yaml holds in migrations.language, for a runtime serving more than one.
	Language string
	// Database is the key migr.yaml holds the target under, which is for messages alone.
	Database string
	// Driver is the engine that will compile the operations, which a migration may branch on.
	Driver string
}

// Check reports a project a runtime could not be told about, which an adapter refuses too.
func (p Project) Check() error {
	switch {
	case strings.TrimSpace(p.Root) == "":
		return ErrNoRoot
	case strings.TrimSpace(p.Dir) == "":
		return ErrNoDir
	case strings.TrimSpace(p.Language) == "":
		return ErrNoLanguage
	}

	return nil
}

// Command is how one runtime process is started, and deliberately not an exec.Cmd.
type Command struct {
	// Path is the program to run, resolved by the adapter rather than searched for here.
	Path string
	// Args are the arguments after the program name, which §9 says a runtime must not depend on.
	Args []string
	// Env is the child's whole environment, and the engine's own when an adapter leaves it unset.
	Env []string
	// Dir is the working directory, and the project root when an adapter leaves it unset.
	Dir string
}

// check reports a command nothing could be started from.
func (c Command) check() error {
	if strings.TrimSpace(c.Path) == "" {
		return ErrNoPath
	}

	return nil
}

// Timeouts are the bounds docs/protocol.md §8.2 tabulates, which migr.yaml surfaces.
type Timeouts struct {
	// Handshake bounds spawn to the initialize result, after which the runtime never started.
	Handshake time.Duration
	// Request bounds migration.discover and migration.validate, and never migration.execute.
	Request time.Duration
	// CancelGrace bounds a canceled request's answer before the guarantee becomes the kill.
	CancelGrace time.Duration
	// ShutdownGrace bounds each rung of the close ladder, from the shutdown result to SIGKILL.
	ShutdownGrace time.Duration
}

// Resolve fills every bound left unset from the protocol's own defaults, which are §8.2's.
func (t Timeouts) Resolve() Timeouts {
	return Timeouts{
		Handshake:     orDefault(t.Handshake, protocol.DefaultHandshake),
		Request:       orDefault(t.Request, protocol.DefaultRequest),
		CancelGrace:   orDefault(t.CancelGrace, protocol.DefaultCancelGrace),
		ShutdownGrace: orDefault(t.ShutdownGrace, protocol.DefaultShutdownGrace),
	}
}

// orDefault substitutes the protocol's bound for one a caller left unset, since none means forever.
func orDefault(bound, fallback time.Duration) time.Duration {
	if bound <= 0 {
		return fallback
	}

	return bound
}

// ErrNoRoot marks a project with no root, which is the working directory §9 gives every runtime.
var ErrNoRoot = errors.New("a runtime is started in the project root, and none was given")

// ErrNoDir marks a project with no migrations directory, which initialize carries absolute.
var ErrNoDir = errors.New("a runtime is told where the migrations are, and none was given")

// ErrNoLanguage marks a project naming no language, which a runtime serving several needs.
var ErrNoLanguage = errors.New("a runtime is told which language it was started for")

// ErrNoPath marks a command naming no program to run.
var ErrNoPath = errors.New("a command names the program to start")

// ErrNoRuntime marks a session opened with no adapter to start anything.
var ErrNoRuntime = errors.New("a session needs the adapter that starts its runtime")

// ErrNoConn marks a session opened with no database for its callbacks to reach.
var ErrNoConn = errors.New("a session needs an open database session for host.db.query")

// ErrNoLog marks a session opened with nowhere to file a runtime's own output.
var ErrNoLog = errors.New("a session needs a logger for host.log and for stderr")

// ErrClosed marks a call made on a session that has already ended.
var ErrClosed = errors.New("this runtime session has ended")

// ErrProtocol marks a runtime that broke the protocol, which always ends the session.
var ErrProtocol = errors.New("the runtime broke the protocol")

// ErrHandshake marks a runtime that never answered initialize.
var ErrHandshake = errors.New("the runtime never started")

// ErrCanceled marks a request abandoned because the caller asked, per §8.
var ErrCanceled = errors.New("the request was canceled")

// ExitError reports a runtime that exited when it should not have, with what it last said.
type ExitError struct {
	// Runtime is the language this process was running.
	Runtime string
	// Code is the exit status, or -1 for a process a signal ended.
	Code int
	// Stderr are the last lines the process wrote, which is the only reason stderr is buffered.
	Stderr []string
}

// Error names the exit status and the last thing the process said before it took it.
func (e *ExitError) Error() string {
	return fmt.Sprintf("the %s runtime %s%s", e.Runtime, e.ending(), e.tail())
}

// ending renders how the process finished, since a signal has no status of its own to report.
func (e *ExitError) ending() string {
	if e.Code < 0 {
		return "was ended by a signal"
	}

	return fmt.Sprintf("exited with status %d", e.Code)
}

// tail renders the last lines of stderr, indented, or nothing at all when it said nothing.
func (e *ExitError) tail() string {
	if len(e.Stderr) == 0 {
		return " and wrote nothing to stderr"
	}

	return ":\n  " + strings.Join(e.Stderr, "\n  ")
}
