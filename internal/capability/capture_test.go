package capability

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestTheZeroCaptureFactPermitsNothingAndSaysSo(t *testing.T) {
	var unread Capture

	if unread.Permitted || !strings.Contains(unread.Reason(), "read as not permitted") {
		t.Errorf("the zero fact reads permitted %v, reason %q", unread.Permitted, unread.Reason())
	}
}

func TestACaptureRefusalNamesEverythingTheServerLacks(t *testing.T) {
	reason := Lacks("wal_level = logical", "a free replication slot").Reason()

	for _, want := range []string{"wal_level = logical", "a free replication slot"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the refusal %q does not name %q", reason, want)
		}
	}

	if Permits().Reason() != "" {
		t.Error("a server that permits capture is given a reason to refuse it")
	}
}

func TestASetReportsTheCaptureFactItWasBuiltWith(t *testing.T) {
	rules := make(map[ir.Kind]Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = Fixed(Native(LockFree))
	}

	set, err := NewSet(Facts{Driver: "test", Capture: Lacks("log_bin = ON")}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	if got := set.Capture(); got.Permitted || len(got.Missing) != 1 || got.Missing[0] != "log_bin = ON" {
		t.Errorf("the set reports %+v", got)
	}
}
