package history

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/ir"
)

func TestACompleteRecordIsAccepted(t *testing.T) {
	if err := applied(t).Validate(); err != nil {
		t.Errorf("a record of an ordinary successful run was rejected: %v", err)
	}
}

func TestTheZeroRecordIsNotAValidRun(t *testing.T) {
	var zero Record

	err := zero.Validate()
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("the zero Record validated with %v, want ErrIncomplete", err)
	}

	if !strings.Contains(err.Error(), "an unnamed migration") {
		t.Errorf("message %q does not say the record names no migration", err)
		t.Log("The zero Record has no version to name, so the message has to say so rather")
		t.Log("than reporting a migration called \"\".")
	}
}

func TestARecordMustNameItsMigrationAndTheRunItBelongsTo(t *testing.T) {
	tests := []struct {
		name   string
		damage func(r *Record)
		field  string
	}{
		{name: "no version", damage: func(r *Record) { r.Version = "  " }, field: "version"},
		{name: "no name", damage: func(r *Record) { r.Name = "" }, field: "name"},
		{name: "no direction", damage: func(r *Record) { r.Direction = "" }, field: "direction"},
		{name: "invented direction", damage: func(r *Record) { r.Direction = "sideways" }, field: "direction"},
		{name: "no batch", damage: func(r *Record) { r.Batch = 0 }, field: "batch"},
		{name: "no ordinal", damage: func(r *Record) { r.Ordinal = 0 }, field: "ordinal"},
		{name: "no timestamp", damage: func(r *Record) { r.AppliedAt = time.Time{} }, field: "applied_at"},
		{name: "negative duration", damage: func(r *Record) { r.Duration = -1 }, field: "duration_ms"},
		{name: "no checksum", damage: func(r *Record) { r.Checksum = checksum.Checksum{} }, field: "checksum"},
		{name: "no driver", damage: func(r *Record) { r.Driver = "" }, field: "driver"},
		{name: "no runtime", damage: func(r *Record) { r.Runtime = "" }, field: "runtime"},
		{name: "unstamped engine", damage: func(r *Record) { r.EngineVersion = "" }, field: "engine_version"},
		{name: "unstamped ir", damage: func(r *Record) { r.IRVersion = 0 }, field: "ir_version"},
		{name: "no outcome", damage: func(r *Record) { r.Outcome = "" }, field: "outcome"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := applied(t)
			tt.damage(&rec)

			err := rec.Validate()
			if !errors.Is(err, ErrIncomplete) {
				t.Fatalf("Validate() = %v, want ErrIncomplete", err)
			}

			var incomplete *IncompleteError
			if !errors.As(err, &incomplete) || incomplete.Field != tt.field {
				t.Errorf("Validate() blamed %v, want the %s field", err, tt.field)
				t.Log("A driver author reading this has to learn which column it may not write,")
				t.Log("not merely that the record was rejected.")
			}
		})
	}
}

func TestADirtyRunMustSayWhereItStopped(t *testing.T) {
	rec := applied(t)
	rec.Outcome = Dirty
	rec.Error = "statement 3 failed"

	err := rec.Validate()
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a dirty run with no statement index validated with %v", err)
	}

	if !strings.Contains(err.Error(), "failed rather than dirty") {
		t.Errorf("message %q does not say what that run actually was", err)
		t.Log("LastStatement is 1-based, matching the golden harness's statement numbering, so")
		t.Log("zero cannot mean both \"statement zero\" and \"none\". A run where none succeeded")
		t.Log("left the schema unchanged, which is failed; M9-T6 resumes from this index and a")
		t.Log("wrong one replays a statement that already ran.")
	}

	rec.LastStatement = 3
	if err := rec.Validate(); err != nil {
		t.Errorf("a dirty run naming its last statement was rejected: %v", err)
	}
}

func TestOnlyADirtyRunCarriesAStatementIndex(t *testing.T) {
	rec := applied(t)
	rec.LastStatement = 3

	err := rec.Validate()
	if !errors.Is(err, ErrIncomplete) {
		t.Errorf("an applied run carried a dirty run's statement index: %v", err)
		t.Log("On an applied run every statement succeeded, so the field would mean a different")
		t.Log("thing than it does on a dirty one -- and M9-T6 resumes from it.")
	}
}

func TestOnlyARunThatDidNotCompleteCarriesAnError(t *testing.T) {
	rec := applied(t)
	rec.Error = "but it succeeded"

	if err := rec.Validate(); !errors.Is(err, ErrIncomplete) {
		t.Errorf("an applied run carried an error: %v", err)
	}

	failed := applied(t)
	failed.Outcome = Failed

	err := failed.Validate()
	if !errors.Is(err, ErrIncomplete) {
		t.Errorf("a failed run carried no error: %v", err)
		t.Log("migr history prints this column, and a failed row with nothing in it tells an")
		t.Log("operator only that something went wrong, which they already knew.")
	}
}

func TestStampFillsTheVersionsACallerCannotKnow(t *testing.T) {
	var rec Record

	stamped := rec.Stamp()

	if stamped.IRVersion != ir.Version {
		t.Errorf("Stamp() left IRVersion as %d, want %d", stamped.IRVersion, ir.Version)
	}

	if stamped.EngineVersion == "" {
		t.Error("Stamp() left EngineVersion empty")
		t.Log("Both describe the build rather than the run, so a caller has no business")
		t.Log("supplying them. Validate requires both, which is what makes a driver that")
		t.Log("forgot to call Stamp fail before it writes rather than record a blank column.")
	}

	t.Log("There is deliberately no assertion that Stamp leaves its receiver alone. The value")
	t.Log("receiver makes that impossible, and switching to a pointer stops the package")
	t.Log("compiling at Record{...}.Stamp() -- so the compiler already holds it and an")
	t.Log("assertion here could never fail.")
}
