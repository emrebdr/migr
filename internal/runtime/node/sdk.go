package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// sdkManifest is the file the probe resolves, and one of the two paths @migr/sdk exports at all.
const sdkManifest = "@migr/sdk/package.json"

// executableName is the runtime @migr/sdk installs, whose location the package itself states.
const executableName = "migr-runtime-node"

// manifest is the part of @migr/sdk's package.json this adapter reads, and no more of it.
type manifest struct {
	// Bin maps each installed executable to the file it runs, in either shape npm accepts.
	Bin json.RawMessage `json:"bin"`
	// Engines is what the package requires of its runtime, which a test holds MinimumNode to.
	Engines struct {
		// Node is the semver range @migr/sdk's own engines field states.
		Node string `json:"node"`
	} `json:"engines"`
}

// executableIn returns the runtime the package at one manifest declares, or why there is none.
func executableIn(path string) (string, error) {
	held, err := manifestAt(path)
	if err != nil {
		return "", err
	}

	named := held.executable()
	if named == "" {
		return "", fmt.Errorf("%w: the package at %s declares no %s to run",
			ErrNoExecutable, filepath.Dir(path), executableName)
	}

	at := filepath.Join(filepath.Dir(path), filepath.FromSlash(named))

	if _, err := os.Stat(at); err != nil {
		return "", fmt.Errorf("%w: the package at %s names %s, which is not there -- an installed "+
			"@migr/sdk ships it, and a linked one needs `npm run build` first",
			ErrNoExecutable, filepath.Dir(path), named)
	}

	return at, nil
}

// manifestAt reads one package.json, wrapping only the half whose own error does not name the file.
func manifestAt(path string) (manifest, error) {
	read, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}

	var held manifest

	if err := json.Unmarshal(read, &held); err != nil {
		return manifest{}, fmt.Errorf("reading %s: %w", path, err)
	}

	return held, nil
}

// executable returns the file this manifest's bin maps the runtime to, and nothing for the other shape.
func (m manifest) executable() string {
	var named map[string]string

	if err := json.Unmarshal(m.Bin, &named); err != nil {
		return ""
	}

	return named[executableName]
}
