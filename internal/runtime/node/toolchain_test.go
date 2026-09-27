package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"
)

// The probe, driven against a node this test wrote.
//
// A fake toolchain is what makes the interesting cases reachable at all: this machine has one node
// and it is the right version, so an old release, a node that fails, and a node whose stdout carries
// something else are only testable by writing one. It is also what proves Discover opens no process,
// since a fake that exits non-zero must not bother it.

// answering renders a node that reports one version and resolves the SDK to one manifest.
func answering(version, manifest string) string {
	return fmt.Sprintf("#!/bin/sh\nprintf '%s\\t%s\\t%s\\n'\n", marker, version, manifest)
}

// fake writes one node onto PATH and returns the path it was written to.
func fake(t *testing.T, script string) string {
	t.Helper()

	if stdruntime.GOOS == "windows" {
		t.Skip("this fake toolchain is a shell script, which Windows does not run")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, program)

	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the fake node: %v", err)
	}

	t.Setenv("PATH", dir)

	return path
}

func TestTheProbeReadsTheVersionAndTheResolvedManifest(t *testing.T) {
	path := fake(t, answering("24.16.0", "/srv/app/node_modules/@migr/sdk/package.json"))

	found, err := ask(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("ask: %v", err)
	}

	if found.path != path {
		t.Errorf("the probe ran %q, want the node on PATH %q", found.path, path)
	}

	if found.version.text != "24.16.0" || found.version.major != 24 {
		t.Errorf("read version %+v", found.version)
	}

	if found.manifest != "/srv/app/node_modules/@migr/sdk/package.json" {
		t.Errorf("read manifest %q", found.manifest)
	}

	if err := found.supported(); err != nil {
		t.Errorf("supported: %v", err)
	}
}

func TestTheProbeIsReadFromItsOwnLineAndNotTheFirstOne(t *testing.T) {
	fake(t, "#!/bin/sh\nprintf 'trace\\t18.0.0\\t/somewhere/else\\n'\n"+
		strings.TrimPrefix(answering("24.16.0", "/srv/app/package.json"), "#!/bin/sh\n"))

	t.Log("An ambient NODE_OPTIONS that preloads something which prints would otherwise be read")
	t.Log("as a version, which is why the probe marks its own line -- and the line above is shaped")
	t.Log("like the probe's, so counting fields is not enough to tell them apart.")

	found, err := ask(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("ask: %v", err)
	}

	if found.version.text != "24.16.0" {
		t.Errorf("read version %q from a stdout carrying another line", found.version.text)
	}
}

func TestANodeWhoseOutputCarriesNoProbeIsReported(t *testing.T) {
	path := fake(t, "#!/bin/sh\necho 'hello'\n")

	_, err := ask(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("a node that answered nothing this adapter can read was accepted")
	}

	for _, want := range []string{path, marker, "NODE_OPTIONS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not name %q: %v", want, err)
		}
	}
}

func TestANodeThatFailsIsReportedWithWhatItSaid(t *testing.T) {
	fake(t, "#!/bin/sh\necho 'something broke' >&2\nexit 7\n")

	_, err := ask(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("a node that exited 7 was accepted")
	}

	if !strings.Contains(err.Error(), "something broke") {
		t.Errorf("the failure does not carry what node said: %v", err)
	}
}

func TestAVersionBelowTheFloorIsRefusedNamingWhatToInstall(t *testing.T) {
	path := fake(t, answering("18.20.8", "/srv/app/package.json"))

	found, err := ask(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("ask: %v", err)
	}

	err = found.supported()
	if !errors.Is(err, ErrOldToolchain) {
		t.Fatalf("supported returned %v, want %v", err, ErrOldToolchain)
	}

	for _, want := range []string{path, "18.20.8", "24", "nodejs.org"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so it is not actionable: %v", want, err)
		}
	}
}

func TestAVersionIsComparedAsANumberAndNotAsAString(t *testing.T) {
	for _, reported := range []string{"24.0.0", "25.1.0", "100.0.0", "v24.16.0"} {
		found, err := versionOf(reported)
		if err != nil {
			t.Fatalf("versionOf(%q): %v", reported, err)
		}

		if err := (probed{version: found}).supported(); err != nil {
			t.Errorf("node %s was refused: %v", reported, err)
		}
	}

	t.Log("A string comparison would accept 9.x, since \"9\" sorts above \"24\".")

	for _, reported := range []string{"9.11.2", "18.20.8", "23.11.0"} {
		found, err := versionOf(reported)
		if err != nil {
			t.Fatalf("versionOf(%q): %v", reported, err)
		}

		if err := (probed{version: found}).supported(); !errors.Is(err, ErrOldToolchain) {
			t.Errorf("node %s was accepted", reported)
		}
	}
}

func TestSomethingThatIsNotAVersionIsNotReadAsOne(t *testing.T) {
	for _, reported := range []string{"", "node", "v", ".24"} {
		if _, err := versionOf(reported); err == nil {
			t.Errorf("versionOf(%q) read a version", reported)
		}
	}
}
