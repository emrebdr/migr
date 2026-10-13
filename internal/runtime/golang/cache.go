package golang

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// workDir is the directory migr keeps its own build products in, inside the user's module.
const workDir = ".migr"

// buildDir is where this adapter's generated runner and its binary live, under workDir.
const buildDir = "go"

// runnerFile is the built binary a Command points at.
const runnerFile = "runner"

// sourceFile is the generated main, at a stable path so a compile error names one an editor opens.
const sourceFile = "main.go"

// stampFile records what the binary beside it was built from, and is written last.
const stampFile = "stamp"

// ignoreFile keeps migr's build products out of the user's commits, from inside the directory itself.
const ignoreFile = ".gitignore"

// ignoreAll is what that file holds, which ignores everything beneath it including itself.
const ignoreAll = "*\n"

// moduleRoot names the build directory of a migrations directory that is the module's own root.
const moduleRoot = "_module"

// pathJoint stands for a separator in a directory's name, since each one builds in one flat directory.
const pathJoint = "__"

// cache is the directory this adapter builds one migrations directory's runner in.
type cache struct {
	// dir is the build directory itself, inside the module rather than beside it.
	dir string
	// work is migr's own directory above it, which the ignore file is written into.
	work string
}

// cacheIn returns the build directory for one migrations directory, creating nothing until it builds.
func cacheIn(moduleDir, relative string) cache {
	work := filepath.Join(moduleDir, workDir)

	return cache{dir: filepath.Join(work, buildDir, cacheName(relative)), work: work}
}

// cacheName renders a directory's place in its module as one name, so two never share a runner.
func cacheName(relative string) string {
	if relative == "" || relative == "." {
		return moduleRoot
	}

	return strings.ReplaceAll(filepath.ToSlash(relative), "/", pathJoint)
}

// source is the generated runner's path, which is what a build failure names.
func (c cache) source() string {
	return filepath.Join(c.dir, sourceFile)
}

// binary is the built runner's path, which is what a Command points at.
func (c cache) binary() string {
	return filepath.Join(c.dir, runnerFile)
}

// pkg is the package pattern go build is pointed at, relative to the module it runs in.
func (c cache) pkg() string {
	return "./" + filepath.ToSlash(filepath.Join(workDir, buildDir, filepath.Base(c.dir)))
}

// holds reports whether a binary built from this exact stamp is already sitting here.
func (c cache) holds(want string) bool {
	recorded, err := os.ReadFile(filepath.Join(c.dir, stampFile))
	if err != nil || string(recorded) != want {
		return false
	}

	info, err := os.Stat(c.binary())

	return err == nil && info.Mode().IsRegular()
}

// prepare makes the build directory and keeps whatever it holds out of the user's commits.
func (c cache) prepare() error {
	if err := os.MkdirAll(c.dir, 0o750); err != nil {
		return fmt.Errorf("making %s: %w", c.dir, err)
	}

	return writeIgnore(c.work)
}

// writeIgnore writes the ignore file if there is none, and never touches one somebody edited.
func writeIgnore(dir string) error {
	path := filepath.Join(dir, ignoreFile)

	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	if err := os.WriteFile(path, []byte(ignoreAll), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

// record writes the stamp last, so a stamp is never newer than the binary it describes.
func (c cache) record(digest string) error {
	path := filepath.Join(c.dir, stampFile)

	if err := os.WriteFile(path, []byte(digest), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

// invalidate drops the stamp before a build starts, so an interrupted one is never read as a hit.
func (c cache) invalidate() error {
	err := os.Remove(filepath.Join(c.dir, stampFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clearing %s: %w", stampFile, err)
	}

	return nil
}

// writeSource puts the generated runner where go build will find it.
func (c cache) writeSource(runner []byte) error {
	if err := os.WriteFile(c.source(), runner, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", c.source(), err)
	}

	return nil
}

// install builds to a name of its own and renames it into place, never over a binary being run.
func (c cache) install(build func(out string) error) error {
	reserved, err := os.CreateTemp(c.dir, runnerFile+"-*")
	if err != nil {
		return fmt.Errorf("reserving a name to build into: %w", err)
	}

	out := reserved.Name()

	defer func() { _ = os.Remove(out) }()

	if err := reserved.Close(); err != nil {
		return fmt.Errorf("reserving a name to build into: %w", err)
	}

	if err := build(out); err != nil {
		return err
	}

	if err := os.Rename(out, c.binary()); err != nil {
		return fmt.Errorf("installing the built runner: %w", err)
	}

	return nil
}
