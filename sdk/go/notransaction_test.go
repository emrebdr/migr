package migr

import (
	"context"
	"strings"
	"testing"

	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

func TestNoTransactionMarksTheStatementJustAddedAndNoOther(t *testing.T) {
	ops := newSchema(nil, version, Up, ModeApply).
		Raw("postgres", "CREATE TABLE t (c int)").
		Raw("postgres", "CREATE INDEX CONCURRENTLY i ON t (c)").NoTransaction().
		Raw("postgres", "ANALYZE t").build.ops

	want := []string{
		`{"op":"raw","driver":"postgres","sql":"CREATE TABLE t (c int)"}`,
		`{"op":"raw","driver":"postgres","sql":"CREATE INDEX CONCURRENTLY i ON t (c)","no_transaction":true}`,
		`{"op":"raw","driver":"postgres","sql":"ANALYZE t"}`,
	}

	if len(ops) != len(want) {
		t.Fatalf("the chain built %d operations, want %d", len(ops), len(want))
	}

	for i, op := range ops {
		got, err := documentOf(op)
		if err != nil {
			t.Fatalf("operation %d is refused: %v", i+1, err)
		}

		if string(got) != want[i] {
			t.Errorf("operation %d\n got %s\nwant %s", i+1, got, want[i])
		}
	}
}

func TestOnlyARawCarryingTheFlagNeedsVersionThree(t *testing.T) {
	if got := since(&Raw{Driver: "postgres", SQL: "SELECT 1"}); got != 1 {
		t.Errorf("a plain raw needs irVersion %d, want 1", got)
	}

	if got := since(Raw{Driver: "postgres", SQL: "VACUUM", NoTransaction: true}); got != 3 {
		t.Errorf("a raw carrying no_transaction needs irVersion %d, want 3", got)
	}

	if wire.IRVersion < 3 {
		t.Errorf("this SDK emits no_transaction and states it emits up to irVersion %d", wire.IRVersion)
	}
}

// standalone is a migration whose one statement cannot run inside a transaction.
type standalone struct{}

// Up builds an index concurrently, which an engine stating irVersion 2 cannot read the flag of.
func (standalone) Up(ctx context.Context, s *Schema) error {
	return s.Raw("postgres", "CREATE INDEX CONCURRENTLY i ON t (c)").NoTransaction().Apply(ctx)
}

func TestNoTransactionAgainstAnEngineThatReadsTwoIsRefusedBeforeItIsSent(t *testing.T) {
	staged(t, map[string]Migration{file: standalone{}})

	e := dial(t)

	greeted := e.send(wire.Initialize, wire.InitializeParams{
		ProtocolVersions: []int{wire.Version}, IRVersion: 2,
		Client:     wire.Client{Name: "migr", Version: "0.0.9"},
		Migrations: wire.MigrationsAt{Dir: "/srv/app/migrations", Language: "go"},
		Database:   wire.DatabaseAt{Name: "default", Driver: "postgres"},
	})
	if answer := e.answer(greeted, nil); answer.Error != nil {
		t.Fatalf("an engine stating irVersion 2 was refused: %v", answer.Error)
	}

	sent := 0
	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	answer := e.answer(id, func(m *wire.Message) *wire.Message {
		if m.Method == wire.HostSchemaApply {
			sent++
		}

		return recorded(t, m)
	})

	if slug := slugOf(t, answer); slug != "unsupported_ir" {
		t.Errorf("the failure is %q, want unsupported_ir", slug)
	}

	for _, want := range []string{"no_transaction", "irVersion 3", "stated 2"} {
		if !strings.Contains(answer.Error.Message, want) {
			t.Errorf("the failure does not name %q: %s", want, answer.Error.Message)
		}
	}

	if sent != 0 {
		t.Errorf("the operation reached the host %d times before it was refused", sent)
		t.Log("An engine that reads 2 decodes raw strictly, so it would refuse the field as an")
		t.Log("unknown member -- pointing at the migration rather than at the CLI to upgrade.")
	}
}
