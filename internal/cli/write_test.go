package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteNewRefusesToReplaceAFileThatIsAlreadyThere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migr.yaml")

	if err := writeNew(path, "first\n"); err != nil {
		t.Fatalf("writeNew: %v", err)
	}

	if err := writeNew(path, "second\n"); err == nil {
		t.Fatal("writeNew replaced a file that was already there")
	}

	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}

	if string(kept) != "first\n" {
		t.Errorf("the file now holds %q, want the bytes that were there first", kept)
	}

	t.Log("init and create both check before they write, so this is the guard against the gap")
	t.Log("between the two -- a second migr, or a person, creating the same file in between.")
}
