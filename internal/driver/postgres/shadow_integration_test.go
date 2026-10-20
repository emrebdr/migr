//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
)

func TestAShadowStateIsRecordedBesideTheHistoryAndASaveKeepsItsConfirmation(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		store := session.History().(history.Shadows)

		if got, err := store.ShadowStates(t.Context()); err != nil || got != nil {
			t.Fatalf("a database with no shadow-state table answered %v, %v", got, err)
		}

		if err := session.History().Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		begun := history.ShadowState{Version: "20261020T130000", Table: "people", Shadow: "_people_new",
			Retired: "_people_old", Capture: "migr_slot", Stage: history.StageCopying, Position: "0/16B3748",
			UpdatedAt: time.Date(2026, 10, 20, 9, 30, 0, 123456000, time.UTC)}
		saved := begun
		saved.Stage, saved.Position, saved.Copied, saved.Cursor = history.StageSyncing, "0/16B3790", 7, []string{"7"}

		if err := session.Transact(t.Context(), func(ctx context.Context) error {
			if err := store.BeginShadow(ctx, begun); err != nil {
				return err
			}

			if err := store.ConfirmShadow(ctx, begun.Version); err != nil {
				return err
			}

			return store.SaveShadow(ctx, saved)
		}); err != nil {
			t.Fatalf("recording a shadow state: %v", err)
		}

		got, err := store.ShadowStates(t.Context())
		if err != nil || len(got) != 1 || got[0].Stage != history.StageSyncing || got[0].Position != "0/16B3790" ||
			!got[0].Confirmed || got[0].Copied != 7 || !got[0].UpdatedAt.Equal(begun.UpdatedAt) {
			t.Errorf("the state read back as %+v, %v", got, err)
		}

		if _, present := introspect(t, session).Table(history.ShadowTable(session.history.table)); present {
			t.Error("the snapshot holds the shadow-state table, which every migr diff would propose dropping")
		}
	})
}

func TestEachServerDeclaresLogicalDecodingAsItIsConfigured(t *testing.T) {
	want := map[string]string{"postgres16": "wal_level = logical (it is replica", "postgres17": ""}

	eachMajor(t, func(t *testing.T, session *pgConn) {
		reason := session.Capabilities().Capture().Reason()
		expected := want[t.Name()[strings.LastIndex(t.Name(), "/")+1:]]

		if expected == "" && reason != "" || expected != "" && !strings.Contains(reason, expected) {
			t.Errorf("the server declares %q, want %q", reason, expected)
		}
	})
}

func TestARoleThatMayNotReplicateIsRefusedCaptureNamingTheAttribute(t *testing.T) {
	session := scratchSession(t, 1)

	for _, sql := range []string{`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = ` +
		`'migr_capplain') THEN CREATE ROLE migr_capplain; END IF; END $$`,
		`ALTER ROLE migr_capplain LOGIN NOREPLICATION PASSWORD 'migr_capplain'`} {
		if err := session.Exec(t.Context(), driver.Statement{SQL: sql}); err != nil {
			t.Fatalf("staging the role: %v", err)
		}
	}

	dsn := dsnFor(servers[1].env, servers[1].dsn)
	plain := open(t, "postgres://migr_capplain:migr_capplain"+dsn[strings.Index(dsn, "@"):], driver.Options{})

	if reason := plain.Capabilities().Capture().Reason(); !strings.Contains(reason, "REPLICATION attribute") ||
		strings.Contains(reason, "wal_level") {
		t.Errorf("an unprivileged role on a logical server declares %q", reason)
	}
}

func TestAnAnswerThatCannotBeReadIsReadAsNotPermitted(t *testing.T) {
	conn, err := pgx.Connect(t.Context(), dsnFor(servers[1].env, servers[1].dsn))
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}

	if err := conn.Close(t.Context()); err != nil {
		t.Fatalf("closing: %v", err)
	}

	if fact := readCapture(t.Context(), conn); fact.Permitted || !strings.Contains(fact.Reason(), "could read") {
		t.Errorf("an unread answer was declared %+v", fact)
	}
}
