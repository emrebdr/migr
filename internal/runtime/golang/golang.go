package golang

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/emrebdr/migr/internal/remedy"
	"github.com/emrebdr/migr/internal/runtime"
)

// Name is the language this adapter runs, spelled as migr.yaml spells migrations.language.
const Name = "go"

// Adapter runs Go migrations by generating a runner in the user's module and building it.
type Adapter struct{}

// compile-time proof that this adapter satisfies the contract a session starts its runtime through.
var _ runtime.Runtime = (*Adapter)(nil)

// New returns the Go runtime adapter, which reaches nothing until it is asked for a Command.
func New() (runtime.Runtime, error) {
	return &Adapter{}, nil
}

// Name reports the language this adapter runs.
func (a *Adapter) Name() string {
	return Name
}

// Discover reports whether this machine has a Go toolchain, opening no process to find out.
func (a *Adapter) Discover(_ context.Context) error {
	_, err := find()

	return err
}

// Command generates the runner for one project, builds it unless one is cached, and names it.
func (a *Adapter) Command(ctx context.Context, p runtime.Project) (runtime.Command, error) {
	built, err := a.prepare(ctx, p)
	if err != nil {
		return runtime.Command{}, err
	}

	return runtime.Command{Path: built, Dir: p.Root}, nil
}

// prepare resolves the module, renders the runner, and returns the binary that runs its migrations.
func (a *Adapter) prepare(ctx context.Context, p runtime.Project) (string, error) {
	if err := p.Check(); err != nil {
		return "", err
	}

	chain, err := identify(ctx, p.Root)
	if err != nil {
		return "", err
	}

	mod, err := chain.locate(ctx, p.Root)
	if err != nil {
		return "", err
	}

	migrations, err := mod.importPath(p.Dir)
	if err != nil {
		return "", err
	}

	return buildFor(ctx, chain, mod, p.Dir, generate(migrations), migrations)
}

// buildFor returns the cached binary for these exact inputs, building one only when there is none.
func buildFor(ctx context.Context, chain toolchain, mod module, dir string,
	runner []byte, migrations string) (string, error) {
	relative, err := filepath.Rel(mod.dir, dir)
	if err != nil {
		return "", remedy.With(fmt.Errorf("%w: %s is not under %s", ErrOutsideModule, dir, mod.dir),
			outsideModule)
	}

	store := cacheIn(mod.dir, relative)

	digest, err := stamp(chain, mod, migrations, dir, runner)
	if err != nil {
		return "", err
	}

	if store.holds(digest) {
		return store.binary(), nil
	}

	if err := compile(ctx, chain, mod, store, runner); err != nil {
		return "", err
	}

	return store.binary(), store.record(digest)
}

// compile writes the runner and builds it, leaving no stamp behind if anything stops it partway.
func compile(ctx context.Context, chain toolchain, mod module, store cache, runner []byte) error {
	if err := store.prepare(); err != nil {
		return err
	}

	if err := store.invalidate(); err != nil {
		return err
	}

	if err := store.writeSource(runner); err != nil {
		return err
	}

	return store.install(func(out string) error {
		return chain.build(ctx, mod, store.pkg(), out, store.source())
	})
}
