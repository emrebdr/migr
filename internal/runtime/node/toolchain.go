package node

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/emrebdr/migr/internal/remedy"
)

// program is the toolchain binary this adapter looks for and runs, and the only one it knows.
const program = "node"

// MinimumNode is the major release @migr/sdk's engines.node requires, which a test holds it to.
const MinimumNode = 24

// marker opens the one line of the probe's output this adapter reads, whatever else reached stdout.
const marker = "migr-node"

// probe is the script node answers both of Command's questions with, written so it cannot throw.
//
// Resolution is Node's own rather than an imitation of it, which is the choice the Go adapter makes
// when it asks `go list -m` instead of parsing go.mod: this is right about a workspace whose
// node_modules is hoisted above the project, about pnpm's symlinks, and about every layout a package
// manager invents next.
var probe = fmt.Sprintf(`const at = (() => {
  try { return require.resolve(%q); } catch { return ""; }
})();

process.stdout.write([%q, process.versions.node, at].join("\t") + "\n");
`, sdkManifest, marker)

// version is one node release, as it reports itself rather than as its command line spells it.
type version struct {
	// text is what node answered, which a message about an old one names verbatim.
	text string
	// major is the number the floor is compared against, since engines.node names a major.
	major int
}

// probed is what one node answered: which binary it was, what it is, and where it found the SDK.
type probed struct {
	// path is the node binary that answered, which is what a command starts.
	path string
	// version is the release it reported itself as.
	version version
	// manifest is the @migr/sdk package.json it resolved, empty when it resolved none.
	manifest string
}

// find resolves the node binary without running anything, which is all Discover is allowed to do.
func find() (string, error) {
	path, err := exec.LookPath(program)
	if err != nil {
		return "", remedy.With(fmt.Errorf("%w: %w", ErrNoToolchain, err),
			fmt.Sprintf("install node %d or newer and put it on the PATH migr runs with", MinimumNode))
	}

	return path, nil
}

// ask runs the one probe from a project root, since that is what the resolution is relative to.
func ask(ctx context.Context, root string) (probed, error) {
	path, err := find()
	if err != nil {
		return probed{}, err
	}

	reported, err := run(ctx, path, root, "-e", probe)
	if err != nil {
		return probed{}, err
	}

	answered, err := marked(reported)
	if err != nil {
		return probed{}, unreadable(path, reported)
	}

	found, err := versionOf(answered[0])
	if err != nil {
		return probed{}, fmt.Errorf("%s reported version %q: %w", path, answered[0], err)
	}

	return probed{path: path, version: found, manifest: answered[1]}, nil
}

// supported reports whether this node is new enough to run the SDK, naming the floor when it is not.
func (p probed) supported() error {
	if p.version.major >= MinimumNode {
		return nil
	}

	return tooOld(p.path, p.version.text)
}

// marked returns the probe's own two fields, ignoring every other line that reached stdout.
func marked(reported string) ([]string, error) {
	for _, line := range strings.Split(reported, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) == 3 && fields[0] == marker {
			return fields[1:], nil
		}
	}

	return nil, fmt.Errorf("no %s line", marker)
}

// versionOf reads what node reported itself as, tolerating the leading v its command line prints.
func versionOf(reported string) (version, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(reported), "v")

	major, _, _ := strings.Cut(trimmed, ".")

	found, err := strconv.Atoi(major)
	if err != nil {
		return version{}, fmt.Errorf("%q does not open with a release number", reported)
	}

	return version{text: trimmed, major: found}, nil
}

// run executes node once and returns its trimmed output, or what it failed with.
func run(ctx context.Context, path, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		return "", toolError(path, stderr.String(), err)
	}

	return strings.TrimRight(stdout.String(), "\n"), nil
}
