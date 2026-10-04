package migr

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

// collecting is a host that keeps every operation document it was sent, the way a capture does.
type collecting struct {
	// documents are the IR documents this host recorded, in the order they were sent.
	documents []string
}

// Call records the operations and answers with the count the engine's host would have recorded.
func (c *collecting) Call(_ context.Context, method string, params, target any) error {
	if method != wire.HostSchemaApply {
		return nil
	}

	p, _ := params.(wire.SchemaApplyParams)

	for _, document := range p.Operations {
		c.documents = append(c.documents, string(document))
	}

	if recorded, ok := target.(*wire.SchemaApplyResult); ok {
		recorded.Recorded = len(p.Operations)
	}

	return nil
}

// Notify answers the one notification a migration sends.
func (c *collecting) Notify(string, any) error {
	return nil
}

func TestASessionAndANewWindowOpenTheSameWindow(t *testing.T) {
	staged(t, map[string]Migration{file: reversible{}})

	e := dial(t)
	e.greet()

	var sent []string

	id := e.send(wire.MigrationExecute, wire.ExecuteParams{
		ID: version, Direction: wire.Up, Mode: wire.ModeApply,
	})

	e.answer(id, func(m *wire.Message) *wire.Message {
		if m.Method == wire.HostSchemaApply {
			var p wire.SchemaApplyParams

			if err := wire.UnmarshalPayload(m.Params, &p); err != nil {
				t.Fatalf("reading the operations: %v", err)
			}

			for _, document := range p.Operations {
				sent = append(sent, string(document))
			}
		}

		return recorded(t, m)
	})

	e.finish()

	host := &collecting{}
	window := NewWindow(version, Up, ModeApply, host)

	if err := (reversible{}).Up(t.Context(), window.Schema()); err != nil {
		t.Fatalf("running the migration through a window of its own: %v", err)
	}

	if _, err := window.Seal(); err != nil {
		t.Fatalf("sealing: %v", err)
	}

	if !reflect.DeepEqual(sent, host.documents) {
		t.Errorf("the session emitted %v and NewWindow emitted %v", sent, host.documents)
	}

	t.Log("The session opens its window through NewWindow, so a test helper standing where the")
	t.Log("session stands is running the migration the way the engine will (M10-T6).")
}

func TestAMigrationCannotCloseTheWindowItActsThrough(t *testing.T) {
	if _, sealable := reflect.TypeOf(&Schema{}).MethodByName("Seal"); sealable {
		t.Error("*Schema can seal its own window, so a migration could emit after its call ended")
	}

	if _, sealable := reflect.TypeOf(&Window{}).MethodByName("Seal"); !sealable {
		t.Error("*Window cannot be sealed, and whoever opened one is who closes it")
	}

	t.Log("docs/protocol.md §5.1: a callback is valid in one execute. TypeScript says this with")
	t.Log("an interface extending another; Go says it by holding the Schema rather than embedding.")
}

func TestUndoneIsTheAnswerTheSessionAsksForWhenItIsAskedForADown(t *testing.T) {
	for _, c := range []struct {
		name   string
		m      Migration
		undo   Undo
		reason string
		slug   string
	}{
		{"it authors its own down", reversible{}, UndoAuthored, "", ""},
		{"it declares that it cannot be undone", oneWay{}, UndoDeclared,
			"the dropped rows are not recoverable; restore from a backup", "declared_irreversible"},
		{"the engine derives its inverse", plain{}, UndoDerived, "", "no_down_authored"},
	} {
		t.Run(c.name, func(t *testing.T) {
			undo, reason := Undone(c.m)

			if undo != c.undo || reason != c.reason {
				t.Fatalf("Undone reports %q and %q, want %q and %q", undo, reason, c.undo, c.reason)
			}

			run, refusal := direction(&Registered{Migration: c.m, File: file}, wire.Down)

			if c.slug == "" {
				if run == nil || refusal != nil {
					t.Fatalf("the session refused a migration that authors a down: %v", refusal)
				}

				return
			}

			if refusal == nil {
				t.Fatal("the session ran a down this migration does not have")
			}

			if refusal.Slug() != c.slug {
				t.Errorf("the session refused with %q, want %q", refusal.Slug(), c.slug)
			}

			if c.reason != "" {
				assertCarriesReason(t, refusal, c.reason)
			}
		})
	}

	t.Log("One answer per language: the session reads Undone, so a helper reading it cannot")
	t.Log("disagree with what the engine will be told (ADR 0068).")
}

// assertCarriesReason holds a refusal to naming the author's own reason, which §7.4 requires.
func assertCarriesReason(t *testing.T, refusal *wire.Error, want string) {
	t.Helper()

	var data struct {
		Reason string `json:"reason"`
	}

	if err := json.Unmarshal(refusal.Data, &data); err != nil {
		t.Fatalf("reading the refusal's data: %v", err)
	}

	if data.Reason != want {
		t.Errorf("the refusal carries %q, want the author's own %q", data.Reason, want)
	}
}
