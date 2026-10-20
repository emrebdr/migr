package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// textOf is a pointer to a text column value, as a row scans it.
func textOf(s string) *string { return &s }

// number is a pointer to a numeric column value.
func number(f float64) *float64 { return &f }

func TestAStandbyIsReadInTheOrderTheMeasurementsDecided(t *testing.T) {
	for name, c := range map[string]struct {
		row    standby
		behind time.Duration
		err    error
	}{
		"streaming and behind": {row: standby{name: "s1", state: textOf("streaming"), lag: number(2.4),
			pending: number(283104)}, behind: 2400 * time.Millisecond},
		"caught up with a stale lag": {row: standby{name: "s1", state: textOf("streaming"), lag: number(2.8),
			pending: number(0)}},
		"caught up with no lag": {row: standby{name: "s1", state: textOf("streaming"), pending: number(0)}},
		"behind with no lag yet": {row: standby{name: "s1", state: textOf("streaming"),
			pending: number(1024)}, err: ErrLagUnmeasured},
		"hidden from this user": {row: standby{name: "s1"}, err: ErrLagHidden},
	} {
		got := c.row.read()

		if c.err != nil {
			if !errors.Is(got.Err, c.err) {
				t.Errorf("%s: read %+v, want %v", name, got, c.err)
			}

			continue
		}

		if got.Err != nil || got.Behind != c.behind {
			t.Errorf("%s: read %+v, want %s behind", name, got, c.behind)
		}
	}

	t.Log("Measured on 16: replay_lag kept its last value, 2.8s, with nothing left to replay, so a")
	t.Log("standby with no WAL outstanding is caught up whatever replay_lag says.")
}

func TestAStandbyThatIsNotStreamingIsUnknown(t *testing.T) {
	got := standby{name: "s1", state: textOf("catchup"), pending: number(0)}.read()
	if got.Known() || !strings.Contains(got.Err.Error(), "catchup") {
		t.Errorf("a standby in catchup read %+v, want unknown naming its state", got)
	}
}

func TestAStandbyIsNamedAsPrimaryConninfoCallsIt(t *testing.T) {
	if got := (standby{name: "standby16", addr: "172.18.0.3"}).named(); got != "standby16 (172.18.0.3)" {
		t.Errorf("named %q", got)
	}

	if got := (standby{addr: "172.18.0.3"}).named(); got != "172.18.0.3" {
		t.Errorf("named %q", got)
	}
}

func TestReplicasAreRefusedSincePostgreSQLReadsItsStandbysFromThePrimary(t *testing.T) {
	if _, err := New(driver.Options{Replicas: []string{"postgres://elsewhere"}}); !errors.Is(err, ErrReplicas) {
		t.Errorf("New returned %v, want ErrReplicas", err)
	}
}
