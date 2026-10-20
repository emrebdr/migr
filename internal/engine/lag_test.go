package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// lagging answers each read with the next scripted reading, the last repeated once it runs out.
type lagging struct {
	// script is each read's answer in turn.
	script [][]driver.Lag
	// err is what every read reports instead, nil where reads succeed.
	err error
	// reads counts how often the lag was read.
	reads int
}

// ReplicaLag returns the next scripted reading.
func (l *lagging) ReplicaLag(context.Context) ([]driver.Lag, error) {
	l.reads++

	if l.err != nil {
		return nil, l.err
	}

	if len(l.script) == 0 {
		return nil, nil
	}

	return l.script[min(l.reads, len(l.script))-1], nil
}

// behind is one replica's reading.
func behind(name string, d time.Duration) driver.Lag {
	return driver.Lag{Replica: name, Behind: d}
}

// lagged is a pacer holding chunks of ten back above a one-second threshold, over a scripted reader.
func lagged(reader driver.LagReader) Lagged {
	return Lagged{Steady: Steady{Rows: 10, Pause: 100 * time.Millisecond}, Reader: reader,
		Threshold: time.Second}
}

func TestReplicasThatKeepUpArePacedSteadily(t *testing.T) {
	for name, reading := range map[string][]driver.Lag{
		"no replicas":     nil,
		"under":           {behind("standby1", 500*time.Millisecond)},
		"at the boundary": {behind("standby1", time.Second)},
	} {
		got, err := lagged(&lagging{script: [][]driver.Lag{reading}}).Pace(t.Context(), Progress{Chunks: 1})
		if err != nil || got.Hold || got.Rows != 10 || got.Throttle != "" {
			t.Errorf("%s: paced %+v (%v), want a steady chunk of 10", name, got, err)
		}
	}
}

func TestAReplicaTooFarBehindHoldsTheChunkAndNamesTheWorstOne(t *testing.T) {
	reader := &lagging{script: [][]driver.Lag{{behind("standby1", 2*time.Second),
		behind("standby2", 7*time.Second), behind("standby3", 0)}}}

	got, err := lagged(reader).Pace(t.Context(), Progress{})
	if err != nil || !got.Hold || got.Wait != lagPoll {
		t.Fatalf("paced %+v (%v), want a hold polling every %s", got, err, lagPoll)
	}

	for _, want := range []string{"7s on standby2", "safety.replica_lag_threshold (1s)"} {
		if !strings.Contains(got.Throttle, want) {
			t.Errorf("the throttle %q does not say %q", got.Throttle, want)
		}
	}
}

func TestALagNobodyCouldReadHoldsTheChunk(t *testing.T) {
	for name, reader := range map[string]*lagging{
		"one replica unreadable": {script: [][]driver.Lag{{behind("standby1", 0),
			{Replica: "10.0.0.5:3306", Err: errors.New("replication is stopped")}}}},
		"the read itself failed": {err: errors.New("connection reset")},
	} {
		got, err := lagged(reader).Pace(t.Context(), Progress{})
		if err != nil || !got.Hold || !strings.Contains(got.Throttle, "replica lag unknown") {
			t.Errorf("%s: paced %+v (%v), want a hold saying the lag is unknown", name, got, err)
		}
	}

	t.Log("Throttling exists to protect the replicas, so one migr cannot read is one it must assume")
	t.Log("is too far behind; the wait is bounded by safety.migration_timeout and Ctrl-C.")
}

func TestAHeldChunkWaitsReadsAgainAndRunsOnceTheReplicaCatchesUp(t *testing.T) {
	opts, _, conn, table := chunked(t, 25, 10)

	reader := &lagging{script: [][]driver.Lag{
		{behind("standby1", 0)},
		{behind("standby1", 4*time.Second)},
		{behind("standby1", 3*time.Second)},
		{behind("standby1", 0)},
	}}
	slept := &waits{}

	var reported []Progress

	opts.Pacer, opts.Sleep = lagged(reader), slept.sleep
	opts.Progress = func(p Progress) { reported = append(reported, p) }

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(table.slices) != 3 {
		t.Fatalf("%d chunks ran, want all three once the replica caught up", len(table.slices))
	}

	held := 0
	for _, p := range reported {
		if p.Waiting {
			held++

			if !strings.Contains(p.Throttle, "standby1") {
				t.Errorf("a hold was reported as %q, naming no replica", p.Throttle)
			}
		}
	}

	if held != 2 || reader.reads < 4 {
		t.Errorf("%d holds were reported over %d reads, want two holds and a read after each", held,
			reader.reads)
	}

	if run := result.Runs[0]; run.Holds != 2 || run.Held != 2*lagPoll {
		t.Errorf("the run counts %d holds for %s, want 2 for %s", run.Holds, run.Held, 2*lagPoll)
	}

	t.Log("A held chunk is asked about again after every poll, so it resumes the moment a reading")
	t.Log("is under the threshold, and the pacer keeps no state of its own to get wrong.")
}

func TestAHoldEndsWhenTheRunIsCanceled(t *testing.T) {
	opts, _, conn, _ := chunked(t, 25, 10)

	ctx, stop := context.WithCancel(t.Context())
	defer stop()

	slept := &waits{cancel: 2, stop: stop}
	opts.Pacer = lagged(&lagging{script: [][]driver.Lag{{behind("standby1", time.Minute)}}})
	opts.Sleep = slept.sleep

	if _, err := Apply(ctx, conn, opts); !errors.Is(err, context.Canceled) ||
		!strings.Contains(err.Error(), "holding chunk 1") {
		t.Errorf("Apply returned %v, want the hold ended by the cancel and named", err)
	}
}
