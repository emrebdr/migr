//go:build integration

package postgres

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
)

// standbyName is what the compose standby calls itself in primary_conninfo.
const standbyName = "standby16"

// lagRows is how many rows the paced backfill fills, in chunks of lagChunk a lagPause apart -- long
// enough that replay_lag, which trails the writes it measures, climbs past a one-second threshold.
const (
	lagRows  = 200
	lagChunk = 25
	lagPause = 300 * time.Millisecond
)

// lagged runs a chunked backfill on postgres16 under a pacer holding chunks above the threshold.
func laggedBackfill(t *testing.T, threshold time.Duration) (engine.Run, []engine.Progress) {
	t.Helper()

	session := scratchSession(t, 0)

	for _, sql := range []string{
		`CREATE TABLE people (id int PRIMARY KEY, label varchar(16))`,
		fmt.Sprintf(`INSERT INTO people SELECT i, NULL FROM generate_series(1, %d) i`, lagRows),
	} {
		if err := session.Exec(t.Context(), driver.Statement{SQL: sql}); err != nil {
			t.Fatalf("staging the table: %v", err)
		}
	}

	opts := queueOptions(t, staged{version: "20261020T120000", name: "label_people", ops: ir.Operations{
		ir.DataBackfill{Table: "people", Column: "label", Type: probeType(t, "string(16)"),
			Value: ir.Literal("x")}}})

	var reported []engine.Progress

	opts.Planner.ChunkRows, opts.Chunker = lagChunk, session
	opts.Pacer = engine.Lagged{Steady: engine.Steady{Rows: lagChunk, Pause: lagPause},
		Reader: session, Threshold: threshold}
	opts.Progress = func(p engine.Progress) { reported = append(reported, p) }

	result, err := engine.Apply(t.Context(), session, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	return result.Runs[0], reported
}

// holds returns the throttles every held chunk was reported with, in order.
func holds(reported []engine.Progress) []string {
	var found []string

	for _, p := range reported {
		if p.Waiting {
			found = append(found, p.Throttle)
		}
	}

	return found
}

func TestTheStandbyIsReadFromThePrimary(t *testing.T) {
	lags, err := scratchSession(t, 0).ReplicaLag(t.Context())
	if err != nil {
		t.Fatalf("ReplicaLag: %v", err)
	}

	if len(lags) != 1 || !strings.HasPrefix(lags[0].Replica, standbyName) || !lags[0].Known() {
		t.Errorf("the primary reported %+v, want the compose standby, readable", lags)
	}

	if none, err := scratchSession(t, 1).ReplicaLag(t.Context()); err != nil || len(none) != 0 {
		t.Errorf("postgres17, which has no standby, reported %+v, %v", none, err)
	}
}

func TestABackfillIsHeldWhileTheStandbyTrailsAndResumesWhenItCatchesUp(t *testing.T) {
	run, reported := laggedBackfill(t, time.Second)

	found := holds(reported)
	if len(found) == 0 {
		t.Fatal("no chunk was held back, and the standby applies every write three seconds late")
	}

	for _, throttle := range found {
		if !strings.Contains(throttle, standbyName) {
			t.Errorf("a hold was reported as %q, naming no standby", throttle)
		}
	}

	if run.Written != lagRows || run.Holds != len(found) {
		t.Errorf("the run wrote %d rows over %d holds, want %d rows and the %d holds reported",
			run.Written, run.Holds, lagRows, len(found))
	}

	t.Logf("%d chunks, %d holds for %s: %v", run.Chunks, run.Holds, run.Held, found)
	t.Log("recovery_min_apply_delay holds the standby's replay three seconds back. replay_lag trails")
	t.Log("the writes it measures, climbing a second a second while they go on, so the hold starts")
	t.Log("once it passes the threshold and ends when the standby has replayed every byte.")
}

func TestABackfillUnderAThresholdTheStandbyMeetsIsNeverHeldForItsLag(t *testing.T) {
	_, reported := laggedBackfill(t, time.Minute)

	for _, throttle := range holds(reported) {
		if strings.Contains(throttle, "above safety.replica_lag_threshold") {
			t.Errorf("a chunk was held for lag under a minute: %q", throttle)
		}
	}
}

func TestAUserWhoMayNotReadTheStandbysStateReadsItsLagAsUnknown(t *testing.T) {
	session := scratchSession(t, 0)

	for _, sql := range []string{
		`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'migr_lagplain') THEN ` +
			`CREATE ROLE migr_lagplain LOGIN PASSWORD 'migr_lagplain'; END IF; END $$`,
	} {
		if err := session.Exec(t.Context(), driver.Statement{SQL: sql}); err != nil {
			t.Fatalf("staging the role: %v", err)
		}
	}

	dsn := dsnFor(servers[0].env, servers[0].dsn)
	at := strings.Index(dsn, "@")
	plain := open(t, "postgres://migr_lagplain:migr_lagplain"+dsn[at:], driver.Options{})

	lags, err := plain.(*pgConn).ReplicaLag(t.Context())
	if err != nil {
		t.Fatalf("ReplicaLag: %v", err)
	}

	if len(lags) != 1 || !errors.Is(lags[0].Err, ErrLagHidden) {
		t.Errorf("an unprivileged user read %+v, want the standby with its lag hidden", lags)
	}
}
