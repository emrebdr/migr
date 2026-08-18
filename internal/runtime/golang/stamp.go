package golang

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/emrebdr/migr/internal/version"
)

// scheme versions the stamp itself, so widening what it covers invalidates every cached binary.
const scheme = "migrgo1"

// manifests are the module files a build depends on, whose contents pin every dependency version.
var manifests = []string{"go.mod", "go.sum"}

// stamp digests everything this adapter can see that would change the binary it builds.
func stamp(t toolchain, m module, migrations, source string, runner []byte) (string, error) {
	sum := sha256.New()

	for _, field := range []string{
		scheme, version.Version, t.version, t.goos, t.goarch, m.path, migrations,
	} {
		write(sum, field, []byte(field))
	}

	write(sum, "runner", runner)

	if err := digestManifests(sum, m.dir); err != nil {
		return "", err
	}

	if err := digestDirectory(sum, source); err != nil {
		return "", err
	}

	return hex.EncodeToString(sum.Sum(nil)), nil
}

// digestManifests folds in go.mod and go.sum, which is where every dependency version is pinned.
func digestManifests(sum hash.Hash, dir string) error {
	for _, name := range manifests {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("reading %s: %w", name, err)
		}

		write(sum, name, content)
	}

	return nil
}

// digestDirectory folds in every file the migrations directory holds, in name order per invariant #8.
func digestDirectory(sum hash.Hash, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading the migrations directory: %w", err)
	}

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	slices.Sort(names)

	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}

		write(sum, name, content)
	}

	return nil
}

// write folds one named value in with its length, so two inputs cannot run together into one.
func write(sum hash.Hash, name string, content []byte) {
	framed := make([]byte, 0, 16)
	framed = binary.BigEndian.AppendUint64(framed, uint64(len(name)))
	framed = binary.BigEndian.AppendUint64(framed, uint64(len(content)))

	_, _ = sum.Write(framed)
	_, _ = sum.Write([]byte(name))
	_, _ = sum.Write(content)
}
