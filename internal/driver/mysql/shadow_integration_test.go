//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
)

func TestAShadowStateIsRecordedBesideTheHistoryAndASaveKeepsItsConfirmationOnBothServers(t *testing.T) {
	eachServer(t, func(t *testing.T, _ server, c *mysqlConn) {
		held := c.History().(mysqlHistory)
		owned := []string{held.table, held.progressTable(), held.releaseTable(), held.shadowTable()}
		dropLater(t, c, owned...)
		drop(t.Context(), c, owned...)

		if got, err := held.ShadowStates(t.Context()); err != nil || got != nil {
			t.Fatalf("a database with no shadow-state table answered %v, %v", got, err)
		}

		if err := held.Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		begun := history.ShadowState{Version: "20261020T130000", Table: "people", Shadow: "_people_new",
			Retired: "_people_old", Capture: "migr_reader", Stage: history.StageCopying,
			Position: "binlog.000002:4711", UpdatedAt: time.Date(2026, 10, 20, 9, 30, 0, 123456000, time.UTC)}
		saved := begun
		saved.Stage, saved.Position, saved.Copied = history.StageSyncing, "binlog.000002:5000", 7
		saved.Behind = 1500 * time.Millisecond

		if err := c.Transact(t.Context(), func(ctx context.Context) error {
			if err := held.BeginShadow(ctx, begun); err != nil {
				return err
			}

			if err := held.ConfirmShadow(ctx, begun.Version); err != nil {
				return err
			}

			return held.SaveShadow(ctx, saved)
		}); err != nil {
			t.Fatalf("recording a shadow state: %v", err)
		}

		got, err := held.ShadowStates(t.Context())
		if err != nil || len(got) != 1 || got[0].Stage != history.StageSyncing ||
			got[0].Position != "binlog.000002:5000" || !got[0].Confirmed || got[0].Copied != 7 || got[0].Behind != saved.Behind ||
			!got[0].UpdatedAt.Equal(begun.UpdatedAt) {
			t.Errorf("the state read back as %+v, %v", got, err)
		}

		if _, present := introspect(t, c).Table(held.shadowTable()); present {
			t.Error("the snapshot holds the shadow-state table, which every migr diff would propose dropping")
		}
	})
}

func TestEachServerDeclaresBinlogCaptureAsItIsConfigured(t *testing.T) {
	want := map[string][]string{
		"mariadb114": {"log_bin = ON", "binlog_format = ROW (it is MIXED)"},
	}

	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		fact := c.Capabilities().Capture()

		if s.name == "mysql80" && !fact.Permitted {
			t.Errorf("a ROW server whose user holds both replication grants declares %q", fact.Reason())
		}

		for _, missing := range want[s.name] {
			if !strings.Contains(fact.Reason(), missing) {
				t.Errorf("the server declares %q, which does not name %q", fact.Reason(), missing)
			}
		}
	})

	plain := openServer(t, server{name: "mysql80", dsn: "migr_plain:migr_plain@tcp(127.0.0.1:55480)/migr"},
		driver.Options{HistoryTable: uniqueName(t)})

	reason := plain.Capabilities().Capture().Reason()
	for _, missing := range []string{"REPLICATION SLAVE", "REPLICATION CLIENT"} {
		if !strings.Contains(reason, missing) {
			t.Errorf("a user without the replication grants declares %q, which does not name %q", reason, missing)
		}
	}

	if strings.Contains(reason, "binlog_format") || strings.Contains(reason, "binlog_row_value_options") {
		t.Errorf("a ROW server declares %q", reason)
	}
}

// sourceRoot is mysql80's root, which granting a privilege needs and migr never does.
const sourceRoot = "root:migr@tcp(127.0.0.1:55480)/"

// asRoot runs one statement on mysql80 as its root.
func asRoot(t *testing.T, stmt string) error {
	t.Helper()

	db, err := sql.Open("mysql", sourceRoot)
	if err != nil {
		return err
	}

	defer func() { _ = db.Close() }()

	_, err = db.ExecContext(context.WithoutCancel(t.Context()), stmt)

	return err
}

func TestAUserWithTheReplicationGrantsIsPermittedCaptureOnMySQL(t *testing.T) {
	for _, stmt := range []string{
		"CREATE USER IF NOT EXISTS 'migr_capture'@'%' IDENTIFIED BY 'migr_capture'",
		"GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'migr_capture'@'%'",
		"GRANT ALL PRIVILEGES ON `migr`.* TO 'migr_capture'@'%'",
	} {
		if err := asRoot(t, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	granted := openServer(t, server{name: "mysql80", dsn: "migr_capture:migr_capture@tcp(127.0.0.1:55480)/migr"},
		driver.Options{HistoryTable: uniqueName(t)})

	if fact := granted.Capabilities().Capture(); !fact.Permitted {
		t.Errorf("a user holding both grants on a ROW server was refused: %s", fact.Reason())
	}
}

func TestAnAnswerThatCannotBeReadIsReadAsNotPermittedOnMySQL(t *testing.T) {
	c := openServer(t, servers[0], driver.Options{HistoryTable: uniqueName(t)})

	db, err := sql.Open("mysql", sourceRoot)
	if err != nil {
		t.Fatalf("opening a pool: %v", err)
	}

	defer func() { _ = db.Close() }()

	closed, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("drawing a connection: %v", err)
	}

	if err := closed.Close(); err != nil {
		t.Fatalf("closing it: %v", err)
	}

	gone := *c
	gone.conn = closed

	if fact := readCapture(t.Context(), &gone, true, serverVersion{variant: MySQL}); fact.Permitted || !strings.Contains(fact.Reason(), "could read") {
		t.Errorf("an unread answer was declared %+v", fact)
	}
}
