package drivertest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// modulePath is the root module, which is what distinguishes it from the nested sdk/go module.
const modulePath = "module github.com/emrebdr/migr"

// goldenDir is where committed golden files live, relative to the repository root.
var goldenDir = filepath.Join("test", "golden")

// ErrNoRepository reports a working directory that sits outside this repository.
var ErrNoRepository = errors.New("no go.mod for " + modulePath + " above the working directory")

// defaultRoot returns the repository's golden root, found by walking up from the caller.
func defaultRoot() (string, error) {
	start, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locating the repository root: %w", err)
	}

	repo, err := repositoryRoot(start)
	if err != nil {
		return "", err
	}

	return filepath.Join(repo, goldenDir), nil
}

// repositoryRoot walks up from a directory to the go.mod declaring the root module.
func repositoryRoot(dir string) (string, error) {
	for {
		if declaresRootModule(filepath.Join(dir, "go.mod")) {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNoRepository
		}

		dir = parent
	}
}

// declaresRootModule reports whether a go.mod names the root module rather than the nested one.
func declaresRootModule(path string) bool {
	src, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		return false
	}

	for line := range strings.Lines(string(src)) {
		if strings.TrimSpace(line) == modulePath {
			return true
		}
	}

	return false
}
