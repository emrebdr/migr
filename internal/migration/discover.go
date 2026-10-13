package migration

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// root is what Discover reads, since the filesystem it is handed is already rooted at the directory.
const root = "."

// Discover returns every migration a directory holds, in the order they must run.
func Discover(fsys fs.FS) ([]Migration, error) {
	return DiscoverBeside(fsys, nil)
}

// DiscoverBeside is Discover passing over the subdirectories named, and refusing every other one.
func DiscoverBeside(fsys fs.FS, beside []string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, describeUnreadable(err)
	}

	found, err := parseAll(entries, beside)
	if err != nil {
		return nil, err
	}

	return Merge(found)
}

// Merge orders migrations read from several directories as one set, refusing two that claim one version.
func Merge(found ...[]Migration) ([]Migration, error) {
	merged := slices.Concat(found...)
	slices.SortStableFunc(merged, byVersion)

	if err := checkDistinct(merged); err != nil {
		return nil, err
	}

	return merged, nil
}

// describeUnreadable separates a directory that is not there from one that could not be read.
func describeUnreadable(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w (create one with `migr init`): %w", ErrNoDirectory, err)
	}

	return fmt.Errorf("reading the migrations directory: %w", err)
}

// parseAll reads every entry the directory holds, refusing each one that is not a migration.
func parseAll(entries []fs.DirEntry, beside []string) ([]Migration, error) {
	found := make([]Migration, 0, len(entries))

	for _, entry := range entries {
		if hidden(entry) || entry.IsDir() && slices.Contains(beside, entry.Name()) {
			continue
		}

		if entry.IsDir() {
			return nil, malformed(entry.Name(),
				"it is a directory, and migrations are not discovered inside one")
		}

		parsed, err := Parse(entry.Name())
		if err != nil {
			return nil, err
		}

		found = append(found, parsed)
	}

	return found, nil
}

// hidden reports the entries a working tree leaves behind, which were never meant to be migrations.
func hidden(entry fs.DirEntry) bool {
	return strings.HasPrefix(entry.Name(), ".")
}

// byVersion orders migrations chronologically, which their fixed-width version does lexically.
func byVersion(a, b Migration) int {
	return strings.Compare(a.Version, b.Version)
}

// checkDistinct refuses two migrations claiming one version, which nothing downstream can resolve.
func checkDistinct(found []Migration) error {
	for i := 1; i < len(found); i++ {
		if found[i].Version != found[i-1].Version {
			continue
		}

		return &DuplicateError{
			Version: found[i].Version,
			Files:   []string{found[i-1].Filename(), found[i].Filename()},
		}
	}

	return nil
}
