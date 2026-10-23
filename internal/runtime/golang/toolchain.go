package golang

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/emrebdr/migr/internal/remedy"
)

// program is the toolchain binary this adapter looks for and runs, and the only one it knows.
const program = "go"

// module is the Go module a project's migrations live in, and the import path they carry inside it.
type module struct {
	// path is the module's own import path, as go.mod declares it.
	path string
	// dir is the directory holding go.mod, which is where everything this adapter writes goes.
	dir string
}

// toolchain is one resolved go binary and the identity a built binary depends on.
type toolchain struct {
	// path is the go binary this adapter found, resolved once rather than searched for repeatedly.
	path string
	// version is what go env reports as GOVERSION, since a different compiler builds a different binary.
	version string
	// goos is the operating system the binary is built for.
	goos string
	// goarch is the architecture the binary is built for.
	goarch string
}

// find resolves the go binary without running anything, which is all Discover is allowed to do.
func find() (string, error) {
	path, err := exec.LookPath(program)
	if err != nil {
		return "", remedy.With(fmt.Errorf("%w: %w", ErrNoToolchain, err),
			"install Go and put go on the PATH migr runs with: a Go migration is built by it")
	}

	return path, nil
}

// identify asks the toolchain what it is, which is what a cached binary was built by.
func identify(ctx context.Context, root string) (toolchain, error) {
	path, err := find()
	if err != nil {
		return toolchain{}, err
	}

	reported, err := run(ctx, path, root, "env", "GOVERSION", "GOOS", "GOARCH")
	if err != nil {
		return toolchain{}, err
	}

	fields := strings.Split(reported, "\n")
	if len(fields) < 3 {
		return toolchain{}, fmt.Errorf("go env reported %q, which is not three lines", reported)
	}

	return toolchain{
		path: path, version: fields[0], goos: fields[1], goarch: fields[2],
	}, nil
}

// locate reports the module a project root sits in, which is where a runner can be built.
func (t toolchain) locate(ctx context.Context, root string) (module, error) {
	reported, err := run(ctx, t.path, root, "list", "-m", "-f", "{{.Path}}\n{{.Dir}}")
	if err != nil {
		return module{}, remedy.With(fmt.Errorf("%w: %w", ErrNoModule, err), noModule)
	}

	path, dir, found := strings.Cut(reported, "\n")
	if !found || strings.TrimSpace(path) == "" || strings.TrimSpace(dir) == "" {
		return module{}, remedy.With(fmt.Errorf("%w: go list -m reported %q", ErrNoModule, reported),
			noModule)
	}

	return module{path: strings.TrimSpace(path), dir: strings.TrimSpace(dir)}, nil
}

// importPath returns the path the generated runner imports the migrations directory under.
func (m module) importPath(dir string) (string, error) {
	relative, err := filepath.Rel(m.dir, dir)
	if err != nil {
		return "", remedy.With(fmt.Errorf("%w: %s is not under %s", ErrOutsideModule, dir, m.dir),
			outsideModule)
	}

	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", remedy.With(fmt.Errorf("%w: %s is not under %s, so module %s cannot import it",
			ErrOutsideModule, dir, m.dir, m.path), outsideModule)
	}

	if relative == "." {
		return m.path, nil
	}

	return m.path + "/" + filepath.ToSlash(relative), nil
}

// build compiles the generated runner to one path, reporting what the compiler said when it will not.
func (t toolchain) build(ctx context.Context, m module, pkg, out, source string) error {
	args := []string{"build", "-o", out, pkg}

	//nolint:gosec // t.path comes from LookPath, and out and pkg are paths this package derived
	cmd := exec.CommandContext(ctx, t.path, args...)
	cmd.Dir = m.dir

	var output bytes.Buffer

	cmd.Stdout, cmd.Stderr = &output, &output

	if err := cmd.Run(); err != nil {
		return &BuildError{Path: source, Output: output.String(), Err: err}
	}

	return nil
}

// run executes one go subcommand and returns its trimmed output, or what it failed with.
func run(ctx context.Context, path, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		return "", toolError(args, stderr.String(), err)
	}

	return strings.TrimRight(stdout.String(), "\n"), nil
}
