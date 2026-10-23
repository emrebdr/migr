package node

import (
	"errors"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/remedy"
)

// ErrNoToolchain marks a machine with no node on PATH, which is the one thing this adapter needs.
var ErrNoToolchain = errors.New("no node was found on PATH")

// ErrOldToolchain marks a node too old to run the SDK, which is a version rather than an absence.
var ErrOldToolchain = errors.New("this node is older than @migr/sdk needs")

// ErrNoSDK marks a project no @migr/sdk resolves from, so there is no runtime to start.
var ErrNoSDK = errors.New("@migr/sdk is not installed for this project")

// ErrNoExecutable marks an @migr/sdk whose runtime is not where the package itself says it is.
var ErrNoExecutable = errors.New("@migr/sdk ships no " + executableName)

// notInstalled reports a project root node resolved no SDK from, and the command that installs one.
func notInstalled(root string) error {
	return remedy.With(fmt.Errorf("%w: node resolved no %s from %s", ErrNoSDK, sdkManifest, root),
		"run npm install @migr/sdk in "+root+", or point --config at the migr.yaml beside the package "+
			"that has it")
}

// tooOld reports a node the SDK will not run on, naming what was found and what is needed.
func tooOld(path, reported string) error {
	return remedy.With(fmt.Errorf("%w: %s is node %s, and @migr/sdk needs node %d or newer",
		ErrOldToolchain, path, reported, MinimumNode),
		fmt.Sprintf("install node %d or newer, from nodejs.org or a version manager, and put it first "+
			"on the PATH migr runs with", MinimumNode))
}

// unreadable reports a node that answered something this adapter could not read as its probe.
func unreadable(path, reported string) error {
	return remedy.With(fmt.Errorf("%s answered %q, which carries no %s line: something in this "+
		"project's NODE_OPTIONS or its own startup may be writing to stdout", path, reported, marker),
		"unset NODE_OPTIONS, or remove whatever it preloads that prints, then run the command again")
}

// toolError reports a node invocation that failed, naming what it answered rather than only its status.
func toolError(path string, output string, err error) error {
	return fmt.Errorf("asking %s about this project: %w%s", path, err, indentOutput(output))
}

// indentOutput renders a tool's output beneath the failure, or nothing when it produced none.
func indentOutput(output string) string {
	trimmed := strings.TrimRight(output, "\n")
	if trimmed == "" {
		return ""
	}

	return ":\n  " + strings.ReplaceAll(trimmed, "\n", "\n  ")
}
