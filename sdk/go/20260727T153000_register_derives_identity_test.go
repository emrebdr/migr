// This file is named as a migration on purpose, and holds nothing but the test below.
//
// Register reads its caller's filename and parses it with the migration grammar, so the only way
// to prove that end to end is from a file the grammar accepts. Every other test in this package
// lives in a file the grammar refuses, which is what TestRegisteringFromAFileThatIsNotAMigration
// asserts from the other side.
package migr

import "testing"

// derivedFile is this file's own name, which is the fixture the test below is built out of.
const derivedFile = "20260727T153000_register_derives_identity_test.go"

// derivedVersion is the version that filename carries, which Register must arrive at unaided.
const derivedVersion = "20260727T153000"

func TestRegisterReadsTheVersionAndFileFromTheCallSite(t *testing.T) {
	previous := registered
	t.Cleanup(func() { registered = previous })

	registered = &registry{loaded: map[string]*Registered{}}

	Register(plain{})

	found, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}

	if len(found) != 1 {
		t.Fatalf("the registry holds %d migrations, want the one just registered", len(found))
	}

	t.Log("The file is read rather than reconstructed, so the version can never disagree with")
	t.Log("what the engine discovered -- a disagreement reads as a migration that did not load.")

	if found[0].File != derivedFile {
		t.Errorf("the migration was registered from %q, want %q", found[0].File, derivedFile)
	}

	if found[0].Version != derivedVersion {
		t.Errorf("the migration was given version %q, want %q", found[0].Version, derivedVersion)
	}

	if found[0].Name != "register_derives_identity_test" {
		t.Errorf("the migration was given name %q", found[0].Name)
	}
}
