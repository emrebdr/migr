package golang

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

// stamped digests one set of inputs, failing the test rather than returning what went wrong.
func stamped(t *testing.T, chain toolchain, dir string, runner []byte) string {
	t.Helper()

	digest, err := stamp(chain, module{path: "example.com/app", dir: filepath.Dir(dir)},
		"example.com/app/migrations", dir, runner)
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}

	return digest
}

// migrationsIn stages a migrations directory holding one file, and returns its path.
func migrationsIn(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "migrations")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("making the migrations directory: %v", err)
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("staging %s: %v", name, err)
		}
	}

	return dir
}

// hostChain is a toolchain identity to stamp against, since these tests run no go command.
var hostChain = toolchain{path: "/usr/bin/go", version: "go1.26.3", goos: "darwin", goarch: "arm64"}

func TestTheGeneratedRunnerIsInTheStamp(t *testing.T) {
	dir := migrationsIn(t, map[string]string{"20260727T153000_add_users.go": "package migrations\n"})

	t.Log("The runner is derived from the module path, which is stamped already -- but the")
	t.Log("template is not, and changing SDKPath must not hand back a binary built before it.")

	if stamped(t, hostChain, dir, []byte("one")) == stamped(t, hostChain, dir, []byte("two")) {
		t.Error("two different runners stamped the same, so a template change would reuse a binary")
	}
}

func TestTheToolchainsIdentityIsInTheStamp(t *testing.T) {
	dir := migrationsIn(t, map[string]string{"20260727T153000_add_users.go": "package migrations\n"})
	runner := []byte("package main\n")

	other := hostChain
	other.version = "go1.27.0"

	if stamped(t, hostChain, dir, runner) == stamped(t, other, dir, runner) {
		t.Error("two toolchains stamped the same; a different compiler builds a different binary")
	}

	elsewhere := hostChain
	elsewhere.goarch = "amd64"

	if stamped(t, hostChain, dir, runner) == stamped(t, elsewhere, dir, runner) {
		t.Error("two architectures stamped the same, so a mounted .migr would run the wrong binary")
	}
}

func TestTwoDifferentInputsCannotFoldIntoOneDigest(t *testing.T) {
	first, second := sha256.New(), sha256.New()

	write(first, "ab", []byte("c"))
	write(second, "a", []byte("bc"))

	t.Log("Unframed, both fold the bytes 'abc' in and collide: a file renamed with one byte")
	t.Log("moved into its contents would stamp the same and hand back a stale binary.")

	if bytes.Equal(first.Sum(nil), second.Sum(nil)) {
		t.Error("the digest is not framed, so two different inputs run together into one")
	}
}

func TestADirectoryInTheMigrationsDirectoryDoesNotBreakTheStamp(t *testing.T) {
	dir := migrationsIn(t, map[string]string{"20260727T153000_add_users.go": "package migrations\n"})

	if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o750); err != nil {
		t.Fatalf("staging a subdirectory: %v", err)
	}

	t.Log("Discovery refuses a subdirectory before Command is ever reached, so this never")
	t.Log("happens in a run -- but reading one as a file would fail with a confusing error.")

	if _, err := stamp(hostChain, module{path: "example.com/app", dir: filepath.Dir(dir)},
		"example.com/app/migrations", dir, []byte("package main\n")); err != nil {
		t.Errorf("stamp: %v", err)
	}
}
