//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
)

// MariaDB's cutover is a holder, a renamer and a prober beside the session, and these hold it
// to what was measured on 11.4.13 before it was relied on: a writer queued behind the holder lands in
// the new table, and a rename held back by some other session's lock on the shadow is stopped rather
// than let through after writers went to the original.

// mariadbCutover is one table and its shadow on the MariaDB writing a row-format log.
type mariadbCutover struct {
	// c is the session the cutover runs on.
	c *mysqlConn
	// pool is what writers and strays write through, which is no migr session.
	pool *sql.DB
	// shadow names the table, its shadow and the retired name.
	shadow driver.Shadow
}

// newMariaDBCutover creates a keyed table carrying a json column and named checks, and a shadow holding its rows.
func newMariaDBCutover(t *testing.T) *mariadbCutover {
	t.Helper()

	c := openServer(t, mariadbBinlog, driver.Options{HistoryTable: uniqueName(t)})
	table := "mc" + digestOf(t.Name())
	s := driver.Shadow{Table: table, Shadow: engine.ShadowName(table, stagedVersion),
		Retired: engine.RetiredName(table, stagedVersion), Key: []string{"id"}}

	objects := []string{s.Table, s.Shadow, s.Retired}
	drop(t.Context(), c, objects...)
	dropLater(t, c, objects...)

	exec(t, c, "CREATE TABLE "+quote(table)+" (id int NOT NULL AUTO_INCREMENT PRIMARY KEY, n int NOT NULL, "+
		"doc json, CONSTRAINT positive CHECK (n > 0), CHECK (n < 1000000))")
	exec(t, c, "INSERT INTO "+quote(table)+" (n, doc) VALUES (1, '{}'), (2, NULL)")
	exec(t, c, "CREATE TABLE "+quote(s.Shadow)+" LIKE "+quote(table))
	exec(t, c, "INSERT INTO "+quote(s.Shadow)+" SELECT * FROM "+quote(table))

	pool, err := sql.Open("mysql", mariadbBinlog.dsnFor())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = pool.Close() })

	return &mariadbCutover{c: c, pool: pool, shadow: s}
}

// count returns how many rows a table holds.
func (mc *mariadbCutover) count(t *testing.T, table string) string {
	t.Helper()

	return scalar(t, mc.c, "SELECT COUNT(*) FROM "+quote(table))
}

func TestOnMariaDBAWriterQueuedBehindTheCutoverLandsInTheNewTable(t *testing.T) {
	mc := newMariaDBCutover(t)

	var (
		wg     sync.WaitGroup
		failed = make(chan error, 8)
	)

	err := mc.c.Cutover(t.Context(), mc.shadow, func(context.Context) error {
		for i := range 8 {
			wg.Go(func() {
				if _, err := mc.pool.ExecContext(context.Background(), "INSERT INTO "+quote(mc.shadow.Table)+
					" (n) VALUES (?)", 100+i); err != nil {
					failed <- err
				}
			})
		}

		time.Sleep(300 * time.Millisecond)

		return nil
	})
	if err != nil {
		t.Fatalf("Cutover: %v", err)
	}

	wg.Wait()
	close(failed)

	for err := range failed {
		t.Errorf("a writer queued behind the cutover failed: %v", err)
	}

	if got := mc.count(t, mc.shadow.Table); got != "10" {
		t.Errorf("the new table holds %s rows, want the 2 copied and the 8 queued writes", got)
		t.Log("Measured on 11.4.13: an ALTER TABLE ... RENAME under the lock failed every queued writer")
		t.Log("with 1146, and a rename waiting on a name before the table's let them into the original.")
	}

	if got := mc.count(t, mc.shadow.Retired); got != "2" {
		t.Errorf("the retired original holds %s rows, want the 2 it had", got)
	}

	if got := scalar(t, mc.c, "SELECT MIN(id) > 2 FROM "+quote(mc.shadow.Table)+" WHERE n >= 100"); got != "1" {
		t.Error("a queued write took an id the original had already handed out")
	}

	checks := scalar(t, mc.c, "SELECT GROUP_CONCAT(CONSTRAINT_NAME ORDER BY CONSTRAINT_NAME) FROM "+
		"information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND "+
		"CONSTRAINT_TYPE = 'CHECK'", mc.shadow.Table)
	if checks != "CONSTRAINT_1,doc,positive" {
		t.Errorf("the new table's checks are %s, want the original's names kept by LIKE", checks)
	}
}

func TestOnMariaDBARenameHeldBackByAnotherSessionsLockIsStoppedAndSwapsNothing(t *testing.T) {
	mc := newMariaDBCutover(t)
	mc.c.statementWait = time.Second

	for _, table := range []string{mc.shadow.Table, mc.shadow.Shadow} {
		exec(t, mc.c, "ALTER TABLE "+quote(table)+" MODIFY id int NOT NULL")
	}

	stray, err := mc.pool.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = stray.Close() }()

	for _, stmt := range []string{"BEGIN", "SELECT COUNT(*) FROM " + quote(mc.shadow.Shadow)} {
		if _, err := stray.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	err = mc.c.Cutover(t.Context(), mc.shadow, func(context.Context) error { return nil })
	if !errors.Is(err, driver.ErrStatementLockTimeout) || !strings.Contains(err.Error(), "not seen waiting") {
		t.Fatalf("a cutover whose rename waits behind a reader of the shadow returned %v, want a lock wait", err)
	}

	if _, err := mc.pool.ExecContext(t.Context(), "INSERT INTO "+quote(mc.shadow.Table)+" (id, n) VALUES (3, 7)"); err != nil {
		t.Errorf("a writer could not reach the table after the refused cutover: %v", err)
	}

	if _, err := stray.ExecContext(t.Context(), "COMMIT"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)

	if got := scalar(t, mc.c, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() "+
		"AND TABLE_NAME = ?", mc.shadow.Retired); got != "0" {
		t.Error("the rename ran once the reader let go, after a writer had gone to the original")
		t.Log("Without the prober the rename waits on the shadow, which sorts before the table, so nothing")
		t.Log("is pending on the table when the holder unlocks and writers are let in first.")
	}

	if got := mc.count(t, mc.shadow.Table); got != "3" {
		t.Errorf("the original holds %s rows, want its 2 and the write made after the refusal", got)
	}
}

func TestOnMariaDBAdmitsKeepsNamedChecksAndJSONAndRefusesSystemVersioning(t *testing.T) {
	c := openServer(t, mariadbBinlog, driver.Options{HistoryTable: uniqueName(t)})
	base := "am" + digestOf(t.Name())

	objects := []string{base + "_checked", base + "_versioned"}
	drop(t.Context(), c, objects...)
	dropLater(t, c, objects...)

	exec(t, c, "CREATE TABLE "+quote(base+"_checked")+" (id int PRIMARY KEY, n int, doc json, "+
		"CONSTRAINT "+quote(base+"_positive")+" CHECK (n > 0))")
	exec(t, c, "CREATE TABLE "+quote(base+"_versioned")+" (id int PRIMARY KEY, n int) WITH SYSTEM VERSIONING")

	if err := c.Admits(t.Context(), driver.Shadow{Table: base + "_checked", Key: []string{"id"}}); err != nil {
		t.Errorf("a table with a named check and a json column was refused: %v", err)
		t.Log("Measured on 11.4.13: CREATE TABLE ... LIKE keeps every check's name, json_valid's too.")
	}

	err := c.Admits(t.Context(), driver.Shadow{Table: base + "_versioned", Key: []string{"id"}})
	if err == nil || !strings.Contains(err.Error(), "SYSTEM VERSIONED") {
		t.Errorf("a system-versioned table returned %v, want it refused naming why", err)
	}
}

func TestOnMariaDBARecordedPositionIsADomainServerSequenceGTID(t *testing.T) {
	sc := newShadowCase(t, mariadbBinlog)

	var recorded string

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	_, err := engine.Apply(ctx, sc.c, sc.options(t, func(p engine.ShadowProgress) {
		if p.Copied > 0 && recorded == "" {
			recorded = p.Position
			cancel()
		}
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the stopped run returned %v, want it canceled", err)
	}

	var domain, server, sequence uint64
	if n, _ := fmt.Sscanf(strings.TrimPrefix(recorded, gtidPrefix), "%d-%d-%d", &domain, &server, &sequence); n != 3 ||
		!strings.HasPrefix(recorded, gtidPrefix) || server != 4 {
		t.Errorf("the position was recorded as %q, want gtid:<domain>-4-<seq> from server-id 4", recorded)
	}

	sc.abandon(t)
}

func TestOnMariaDBACatchUpInsideTheEnginesTransactionDoesNotHoldTheRenameBack(t *testing.T) {
	mc := newMariaDBCutover(t)

	for _, table := range []string{mc.shadow.Table, mc.shadow.Shadow} {
		exec(t, mc.c, "ALTER TABLE "+quote(table)+" MODIFY id int NOT NULL")
	}

	err := mc.c.Transact(t.Context(), func(ctx context.Context) error {
		return mc.c.Cutover(ctx, mc.shadow, func(ctx context.Context) error {
			_, err := mc.c.runner().ExecContext(ctx, "INSERT INTO "+quote(mc.shadow.Shadow)+" (id, n) VALUES (3, 3)")

			return err
		})
	})
	if err != nil {
		t.Log("The catch-up's write holds the shadow's metadata lock until its transaction ends, and the")
		t.Log("shadow sorts before the table, so the session commits before the rename is queued.")
		t.Fatalf("a cutover whose catch-up wrote the shadow inside the engine's transaction returned %v", err)
	}

	if got := mc.count(t, mc.shadow.Table); got != "3" {
		t.Errorf("the new table holds %s rows, want the 2 copied and the 1 caught up", got)
	}
}

func TestOnMariaDBTheProberReadsAnAbsentNameNobodyHoldsAsFree(t *testing.T) {
	mc := newMariaDBCutover(t)

	prober, err := mc.c.sideConn(t.Context(), mc.pool, 0)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = prober.Close() }()

	for name, want := range map[string]bool{mc.shadow.Retired: false, mc.shadow.Shadow: false} {
		if held, err := lockHeld(t.Context(), prober, name); err != nil || held != want {
			t.Errorf("lockHeld(%s) = %v (%v), want %v: an absent or idle name is no rename's", name, held, err, want)
		}
	}
}
