package history

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/checksum"
)

func TestACorruptRowIsNotDrift(t *testing.T) {
	err := error(&CorruptError{
		Table:   "migr_migrations",
		Batch:   7,
		Ordinal: 2,
		Field:   "checksum",
		Err:     checksum.ErrMalformed,
	})

	if !errors.Is(err, ErrCorrupt) {
		t.Error("a corrupt row does not report itself as corrupt")
	}

	if !errors.Is(err, checksum.ErrMalformed) {
		t.Error("a corrupt row hides why it could not be read")
		t.Log("Unwrap returns both the sentinel and the cause, so a caller can act on \"this")
		t.Log("row is unreadable\" and still report which half of the checksum would not parse.")
	}

	if errors.Is(err, ErrIncomplete) {
		t.Error("a row that cannot be read reported itself as a record that cannot be written")
		t.Log("ADR 0009: an unknown scheme and a malformed value are different errors -- one")
		t.Log("means upgrade the CLI, the other means this row is corrupt. Neither is drift,")
		t.Log("and reporting either as exit code 4 is how operators learn to ignore exit code 4.")
	}

	for _, want := range []string{"migr_migrations", "batch 7", "ordinal 2", "checksum"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
			t.Log("An operator has to be able to find the row, since the only repair is manual.")
		}
	}
}

func TestACorruptRowWithNoCauseUnwrapsToTheSentinelAlone(t *testing.T) {
	corrupt := &CorruptError{Table: "migr_migrations", Field: "outcome"}

	if !errors.Is(error(corrupt), ErrCorrupt) {
		t.Error("a corrupt row with no underlying cause matched nothing")
	}

	for i, unwrapped := range corrupt.Unwrap() {
		if unwrapped == nil {
			t.Errorf("Unwrap()[%d] is nil", i)
			t.Log("errors.Is documents that Unwrap must not return a slice containing nil, and")
			t.Log("this asserts the slice rather than the behavior deliberately: today's errors.Is")
			t.Log("walks past a nil entry and still matches, so an assertion phrased as")
			t.Log("errors.Is(err, ErrCorrupt) passes whether the guard is there or not.")
		}
	}
}

func TestAnIncompleteRecordNamesTheFieldAndTheMigration(t *testing.T) {
	rec := applied(t)
	rec.Driver = ""

	err := rec.Validate()

	var incomplete *IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Validate() returned %v, which is not an IncompleteError", err)
	}

	if incomplete.Version != rec.Version || incomplete.Field != "driver" {
		t.Errorf("the error names migration %q field %q, want %q and \"driver\"",
			incomplete.Version, incomplete.Field, rec.Version)
	}

	if !strings.Contains(err.Error(), rec.Version) {
		t.Errorf("message %q does not name the migration (invariant #9)", err)
	}
}
