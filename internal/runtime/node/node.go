package node

import (
	"context"

	"github.com/emrebdr/migr/internal/runtime"
)

// Name is the language this adapter runs, spelled as migr.yaml spells migrations.language.
const Name = "typescript"

// Adapter runs TypeScript and JavaScript migrations through the executable @migr/sdk installs.
type Adapter struct{}

// compile-time proof that this adapter satisfies the contract a session starts its runtime through.
var _ runtime.Runtime = (*Adapter)(nil)

// New returns the Node runtime adapter, which reaches nothing until it is asked for a Command.
func New() (runtime.Runtime, error) {
	return &Adapter{}, nil
}

// Name reports the language this adapter runs.
func (a *Adapter) Name() string {
	return Name
}

// Discover reports whether this machine has node at all, opening no process to find out.
func (a *Adapter) Discover(_ context.Context) error {
	_, err := find()

	return err
}

// Command resolves node and the SDK from one project, and names the executable that runs it.
func (a *Adapter) Command(ctx context.Context, p runtime.Project) (runtime.Command, error) {
	found, err := a.resolve(ctx, p)
	if err != nil {
		return runtime.Command{}, err
	}

	return runtime.Command{Path: found.path, Args: []string{found.executable}, Dir: p.Root}, nil
}

// resolved is the node binary a project runs and the runtime it was found to have installed.
type resolved struct {
	// path is the node binary that answered the probe.
	path string
	// executable is the migr-runtime-node this project's own @migr/sdk ships.
	executable string
}

// resolve asks node what it is and where the SDK is, reporting the version before the install.
func (a *Adapter) resolve(ctx context.Context, p runtime.Project) (resolved, error) {
	if err := p.Check(); err != nil {
		return resolved{}, err
	}

	found, err := ask(ctx, p.Root)
	if err != nil {
		return resolved{}, err
	}

	if err := found.supported(); err != nil {
		return resolved{}, err
	}

	if found.manifest == "" {
		return resolved{}, notInstalled(p.Root)
	}

	executable, err := executableIn(found.manifest)
	if err != nil {
		return resolved{}, err
	}

	return resolved{path: found.path, executable: executable}, nil
}
