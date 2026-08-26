package golang

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/runtime"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

// marker is written over a built binary, so a rebuild is observable as the marker being gone.
const marker = "this is not a binary"

// fixture copies the fixture module and the stub SDK it replaces onto a fresh temporary tree.
func fixture(t *testing.T) runtime.Project {
	t.Helper()

	root := filepath.Join(t.TempDir(), "project")
	copyTree(t, filepath.Join("testdata", "project"), root)
	pointAtTheSDK(t, root)

	return projectAt(root)
}

// pointAtTheSDK rewrites the fixture's replace directive to where this repository keeps sdk/go.
func pointAtTheSDK(t *testing.T, root string) {
	t.Helper()

	sdk, err := filepath.Abs(filepath.Join("..", "..", "..", "sdk", "go"))
	if err != nil {
		t.Fatalf("resolving the SDK: %v", err)
	}

	manifest := filepath.Join(root, "go.mod")

	content, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("reading the fixture's go.mod: %v", err)
	}

	rewritten := strings.Replace(string(content),
		"=> ../../../../../sdk/go", "=> "+sdk, 1)

	if rewritten == string(content) {
		t.Fatalf("the fixture's go.mod carries no replace to rewrite:\n%s", content)
	}

	if err := os.WriteFile(manifest, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("writing the fixture's go.mod: %v", err)
	}
}

// projectAt describes one copied fixture the way a session would describe it to an adapter.
func projectAt(root string) runtime.Project {
	return runtime.Project{
		Root:     root,
		Dir:      filepath.Join(root, "migrations"),
		Language: Name,
		Database: "default",
		Driver:   "postgres",
	}
}

// copyTree copies one testdata directory, since a build writes into the module it builds.
func copyTree(t *testing.T, from, to string) {
	t.Helper()

	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatalf("reading %s: %v", from, err)
	}

	if err := os.MkdirAll(to, 0o750); err != nil {
		t.Fatalf("making %s: %v", to, err)
	}

	for _, entry := range entries {
		source, target := filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name())

		if entry.IsDir() {
			copyTree(t, source, target)

			continue
		}

		copyFile(t, source, target)
	}
}

// copyFile copies one fixture file, keeping it writable so a test can edit what it copied.
func copyFile(t *testing.T, from, to string) {
	t.Helper()

	content, err := os.ReadFile(from)
	if err != nil {
		t.Fatalf("reading %s: %v", from, err)
	}

	if err := os.WriteFile(to, content, 0o600); err != nil {
		t.Fatalf("writing %s: %v", to, err)
	}
}

// commandFor asks the adapter for one project's runtime command, failing the test when it refuses.
func commandFor(t *testing.T, p runtime.Project) runtime.Command {
	t.Helper()

	command, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	built, err := command.Command(t.Context(), p)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	return built
}

// discovered starts what the adapter built, shakes hands with it, and reports what it loaded.
func discovered(t *testing.T, built runtime.Command) []string {
	t.Helper()

	child := newChild(t, built)
	defer child.stop()

	child.call(t, protocol.Initialize, protocol.InitializeParams{
		ProtocolVersions: []int{protocol.Version}, IRVersion: ir.Version,
		Client:     protocol.Client{Name: "migr", Version: "test"},
		Migrations: protocol.MigrationsAt{Dir: built.Dir, Language: Name},
		Database:   protocol.DatabaseAt{Name: "default", Driver: "postgres"},
	})

	var found protocol.DiscoverResult

	decode(t, child.call(t, protocol.MigrationDiscover, struct{}{}), &found)

	versions := make([]string, 0, len(found.Migrations))
	for _, m := range found.Migrations {
		versions = append(versions, m.Version)
	}

	child.call(t, protocol.Shutdown, struct{}{})

	if err := child.cmd.Wait(); err != nil {
		t.Errorf("the runtime exited with %v, and §9 says it exits 0 after shutdown", err)
	}

	return versions
}

// child is one built runner running as a process, with the two pipes the protocol needs.
type child struct {
	cmd  *exec.Cmd
	in   *protocol.Writer
	out  *protocol.Reader
	next int64
	stop func()
}

// newChild starts the built binary with its stdin and stdout connected, and nothing else.
func newChild(t *testing.T, built runtime.Command) *child {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), built.Path)
	cmd.Dir = built.Dir
	cmd.Stderr = os.Stderr

	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("opening the runtime's stdin: %v", err)
	}

	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("opening the runtime's stdout: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", built.Path, err)
	}

	return &child{
		cmd: cmd, in: protocol.NewWriter(in), out: protocol.NewReader(out),
		stop: func() { _ = in.Close() },
	}
}

// call sends one request and reads what came back, failing the test on anything else.
func (c *child) call(t *testing.T, method string, params any) *protocol.Message {
	t.Helper()

	payload, err := protocol.MarshalPayload(params)
	if err != nil {
		t.Fatalf("encoding %s: %v", method, err)
	}

	c.next++

	if err := c.in.Write(protocol.NewRequest(c.next, method, payload)); err != nil {
		t.Fatalf("sending %s: %v", method, err)
	}

	answer, err := c.out.Read()
	if err != nil {
		t.Fatalf("reading the answer to %s: %v", method, err)
	}

	if answer.Error != nil {
		t.Fatalf("%s failed: %v", method, answer.Error)
	}

	return answer
}

// decode reads a response's result into a target, failing the test rather than the runtime.
func decode(t *testing.T, m *protocol.Message, target any) {
	t.Helper()

	if err := protocol.UnmarshalPayload(m.Result, target); err != nil {
		t.Fatalf("reading the result: %v", err)
	}
}

// spoil overwrites a built binary, so the next Command reveals whether it rebuilt or reused.
func spoil(t *testing.T, built runtime.Command) {
	t.Helper()

	if err := os.WriteFile(built.Path, []byte(marker), 0o600); err != nil {
		t.Fatalf("spoiling %s: %v", built.Path, err)
	}
}

// spoiled reports whether the binary is still the marker, which means nothing rebuilt it.
func spoiled(t *testing.T, built runtime.Command) bool {
	t.Helper()

	content, err := os.ReadFile(built.Path)
	if err != nil {
		t.Fatalf("reading %s: %v", built.Path, err)
	}

	return string(content) == marker
}

// edit rewrites one file inside a copied fixture, which is how a test changes an input.
func edit(t *testing.T, p runtime.Project, relative, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(p.Root, relative), []byte(content), 0o600); err != nil {
		t.Fatalf("editing %s: %v", relative, err)
	}
}

// addTeams is a second fixture migration, for the case where a file is added rather than edited.
const addTeams = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddTeams{}) }

type AddTeams struct{}

func (m AddTeams) Up(ctx context.Context, s *migr.Schema) error {
	return s.Apply(ctx, migr.Raw{Driver: "postgres", SQL: "CREATE TABLE teams (id bigint)"})
}
`

// editedUsers is the first fixture migration with its body changed and its identity untouched.
const editedUsers = `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddUsers{}) }

type AddUsers struct{}

func (m AddUsers) Up(ctx context.Context, s *migr.Schema) error {
	return s.Apply(ctx, migr.Raw{Driver: "postgres", SQL: "CREATE TABLE people (id bigint)"})
}
`
