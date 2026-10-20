package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capture"
)

func TestLogicalDecodingIsPermittedOnlyWithEverySettingAndPrivilegeInPlace(t *testing.T) {
	if got := (captureAnswer{level: logicalLevel, replicates: true, free: 3}).capture(); !got.Permitted {
		t.Errorf("a logical server with a replicating role and free slots was refused: %+v", got)
	}

	for name, tc := range map[string]struct {
		answer captureAnswer
		names  string
	}{
		"wal_level":  {captureAnswer{level: "replica", replicates: true, free: 3}, "wal_level = logical (it is replica"},
		"role":       {captureAnswer{level: logicalLevel, free: 3}, "REPLICATION attribute"},
		"slots used": {captureAnswer{level: logicalLevel, replicates: true}, "free replication slot"},
	} {
		t.Run(name, func(t *testing.T) {
			got := tc.answer.capture()
			if got.Permitted || !strings.Contains(got.Reason(), tc.names) {
				t.Errorf("the answer %+v was read as %+v, want a refusal naming %q", tc.answer, got, tc.names)
			}
		})
	}
}

func TestEveryMissingPrerequisiteIsNamedAtOnce(t *testing.T) {
	if got := (captureAnswer{level: "minimal"}).capture(); len(got.Missing) != 3 {
		t.Errorf("a server lacking all three was read as missing %v", got.Missing)
	}
}

func TestThePostgresDriverOpensNoChangeStreamYet(t *testing.T) {
	_, err := (&pgDriver{}).Capture(context.Background(), "", capture.Request{})
	if !errors.Is(err, capture.ErrUnsupported) || !strings.Contains(err.Error(), "logical decoding") {
		t.Errorf("Capture returned %v", err)
	}
}
