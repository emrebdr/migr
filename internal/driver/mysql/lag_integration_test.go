//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
)

// The compose source and replica the lag tests use, and the database the replica replicates alone.
const (
	lagSourceDSN  = "migr:migr@tcp(127.0.0.1:55480)/migr_lag"
	lagReplicaDSN = "migr:migr@tcp(127.0.0.1:55490)/migr_lag"
	replicaRoot   = "root:migr@tcp(127.0.0.1:55490)/"
	replicaAddr   = "127.0.0.1:55490"
)

// lagged paces chunks of 25 rows 300ms apart, long enough for the replica to trail a second behind.
const (
	lagRows  = 200
	lagChunk = 25
	lagPause = 300 * time.Millisecond
)

// replicaDSN is the replica's connection string, preferring the environment override.
func replicaDSN() string {
	if override := os.Getenv("MIGR_TEST_MYSQL80_REPLICA_DSN"); override != "" {
		return override
	}

	return lagReplicaDSN
}

// sourceWithReplica opens mysql80's lag database with the given replicas configured.
func sourceWithReplica(t *testing.T, replicas ...string) *mysqlConn {
	t.Helper()

	dsn := lagSourceDSN
	if override := os.Getenv("MIGR_TEST_MYSQL80_LAG_DSN"); override != "" {
		dsn = override
	}

	return openServer(t, server{name: "mysql80", dsn: dsn}, driver.Options{HistoryTable: uniqueName(t),
		Replicas: replicas})
}

// onReplica runs one statement on the replica as its root, which only these tests ever need.
func onReplica(t *testing.T, sqlText string) {
	t.Helper()

	db, err := sql.Open("mysql", replicaRoot)
	if err != nil {
		t.Fatalf("opening the replica: %v", err)
	}

	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(context.WithoutCancel(t.Context()), sqlText); err != nil {
		t.Fatalf("%s on the replica: %v", sqlText, err)
	}
}

// laggedBackfill fills a table on the source in chunks paced by the replica's lag, and reports the holds.
func laggedBackfill(t *testing.T, threshold time.Duration) (engine.Run, []string) {
	t.Helper()

	c := sourceWithReplica(t, replicaDSN())
	table := uniqueName(t) + "_t"
	fresh(t, c, table)

	exec(t, c, fmt.Sprintf("CREATE TABLE %s (`id` int PRIMARY KEY, `label` varchar(16) NULL)", quote(table)))
	exec(t, c, fmt.Sprintf("INSERT INTO %s (`id`) SELECT `n` FROM (WITH RECURSIVE s(n) AS (SELECT 1 "+
		"UNION ALL SELECT n + 1 FROM s WHERE n < %d) SELECT n FROM s) AS ns", quote(table), lagRows))

	opts := staged(t, ir.Operations{ir.DataBackfill{Table: table, Column: "label",
		Type: mustType(t, "string(16)"), Value: ir.Literal("x")}})

	var found []string

	opts.Planner.ChunkRows, opts.Chunker = lagChunk, c
	opts.Pacer = engine.Lagged{Steady: engine.Steady{Rows: lagChunk, Pause: lagPause}, Reader: c,
		Threshold: threshold}
	opts.Progress = func(p engine.Progress) {
		if p.Waiting {
			found = append(found, p.Throttle)
		}
	}

	result, err := engine.Apply(t.Context(), c, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	return result.Runs[0], found
}

func TestTheReplicaIsReadOnTheReplica(t *testing.T) {
	lags, err := sourceWithReplica(t, replicaDSN()).ReplicaLag(t.Context())
	if err != nil || len(lags) != 1 || !lags[0].Known() || lags[0].Replica != replicaAddr {
		t.Errorf("the source read %+v, %v, want the compose replica, readable", lags, err)
	}
}

func TestABackfillIsHeldWhileTheReplicaTrailsAndResumesWhenItCatchesUp(t *testing.T) {
	run, found := laggedBackfill(t, time.Second)

	if len(found) == 0 {
		t.Fatal("no chunk was held back, and the replica applies every write three seconds late")
	}

	for _, throttle := range found {
		if !strings.Contains(throttle, replicaAddr) {
			t.Errorf("a hold was reported as %q, naming no replica", throttle)
		}
	}

	if run.Written != lagRows {
		t.Errorf("the run wrote %d rows, want all %d once the replica caught up", run.Written, lagRows)
	}

	t.Logf("%d chunks, %d holds for %s: %v", run.Chunks, run.Holds, run.Held, found)
}

func TestABackfillUnderAThresholdTheReplicaMeetsIsNeverHeld(t *testing.T) {
	if _, found := laggedBackfill(t, time.Minute); len(found) != 0 {
		t.Errorf("chunks were held under a minute's threshold: %v", found)
	}
}

func TestAStoppedReplicaReadsAsUnknown(t *testing.T) {
	c := sourceWithReplica(t, replicaDSN())

	onReplica(t, "STOP REPLICA SQL_THREAD")
	t.Cleanup(func() { onReplica(t, "START REPLICA SQL_THREAD") })

	lags, err := c.ReplicaLag(t.Context())
	if err != nil || len(lags) != 1 || !errors.Is(lags[0].Err, ErrReplicationStopped) ||
		!strings.Contains(lags[0].Err.Error(), "SQL thread: No") {
		t.Errorf("a stopped replica read %+v, %v, want replication stopped", lags, err)
	}
}

func TestAnUnreachableReplicaReadsAsUnknownNamingItsAddressAlone(t *testing.T) {
	c := sourceWithReplica(t, "migr:s3cret@tcp(127.0.0.1:1)/migr_lag")

	lags, err := c.ReplicaLag(t.Context())
	if err != nil || len(lags) != 1 || lags[0].Known() || lags[0].Replica != "127.0.0.1:1" {
		t.Fatalf("an unreachable replica read %+v, %v", lags, err)
	}

	if strings.Contains(lags[0].Err.Error(), "s3cret") {
		t.Errorf("the reason carries the password: %v", lags[0].Err)
	}
}

func TestAUserWithoutReplicationClientReadsAsUnknown(t *testing.T) {
	onReplica(t, "CREATE USER IF NOT EXISTS 'migr_lagplain'@'%' IDENTIFIED BY 'migr_lagplain'")

	c := sourceWithReplica(t, "migr_lagplain:migr_lagplain@tcp("+replicaAddr+")/")

	lags, err := c.ReplicaLag(t.Context())
	if err != nil || len(lags) != 1 || !errors.Is(lags[0].Err, ErrNoReplicaGrant) {
		t.Errorf("a user without the grant read %+v, %v, want ErrNoReplicaGrant", lags, err)
	}
}
