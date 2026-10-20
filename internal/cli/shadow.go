package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
)

// shadowing gives a run what a shadow-table migration changes its table through, and what it logs.
func (t *target) shadowing(options engine.Options, d driver.Driver, conn driver.Conn,
	log *slog.Logger) engine.Options {
	if shadower, shadows := conn.(driver.Shadower); shadows {
		options.Shadower = shadower
	}

	dsn := t.db.DSN.Reveal()
	options.Capture = func(ctx context.Context, req capture.Request) (capture.Stream, error) {
		return d.Capture(ctx, dsn, req)
	}
	options.Shadowed = logShadow(log)

	return options
}

// shadowsOf reports whether a session performs shadow-table migration.
func shadowsOf(conn driver.Conn) bool {
	_, shadows := conn.(driver.Shadower)

	return shadows
}

// logShadow logs each moment of a shadow-table migration, warning once as a hold begins so it never waits silently.
func logShadow(log *slog.Logger) func(engine.ShadowProgress) {
	warned := false

	return func(p engine.ShadowProgress) {
		attrs := []any{"migration", p.Migration.Filename(), "table", p.Table, "shadow", p.Shadow,
			"copied", p.Copied, "applied", p.Applied, "position", p.Position}

		if p.Resumed {
			attrs = append(attrs, "resumed", true)
		}

		switch {
		case p.Stage == history.StageCutOver:
			log.Info("cut over: the shadow table replaced the original", attrs...)
		case p.Holding && !warned:
			warned = true

			log.Warn("holding the shadow table in sync until its cutover is confirmed -- Ctrl-C "+
				"detaches and keeps it, and the next migr up carries the hold on", append(attrs,
				"behind", p.Behind)...)
		case p.Holding:
			log.Info("holding the shadow table in sync", append(attrs, "read", p.Batch, "behind", p.Behind)...)
		case p.Batch > 0:
			log.Info("applied captured changes to the shadow table", append(attrs, "read", p.Batch)...)
		default:
			log.Info("copied rows into the shadow table", attrs...)
		}
	}
}

// shadowDoc is how a migration would run through a shadow table, as a plan renders it.
type shadowDoc struct {
	// Table is the table it changes.
	Table string `json:"table,omitempty"`
	// Shadow is the copy it changes instead.
	Shadow string `json:"shadow,omitempty"`
	// Retired is the name the original keeps after the cutover.
	Retired string `json:"retired,omitempty"`
	// Capture is the server-side resource its change stream reads through.
	Capture string `json:"capture,omitempty"`
	// Stage is where a stopped run stands, which this one carries on from, absent for a fresh start.
	Stage history.Stage `json:"stage,omitempty"`
	// Position is the log position a stopped run recorded, which this one reads on from.
	Position string `json:"position,omitempty"`
	// Refusal is why migr up would refuse it before creating anything, absent where it would not.
	Refusal string `json:"refusal,omitempty"`
}

// shadowDocOf renders how a migration would run through a shadow table, and nothing where it declares none.
func shadowDocOf(planned *engine.ShadowPlan) *shadowDoc {
	if planned == nil {
		return nil
	}

	doc := &shadowDoc{Table: planned.Table, Shadow: planned.Shadow, Retired: planned.Retired,
		Capture: planned.Capture}

	if planned.Resume != nil {
		doc.Stage, doc.Position = planned.Resume.Stage, planned.Resume.Position
	}

	if planned.Refused != nil {
		doc.Refusal = planned.Refused.Error()
	}

	return doc
}

// writeShadow says how a migration runs through a shadow table, or why migr up would refuse to.
func (m compiledMigration) writeShadow(b *strings.Builder) {
	if m.Shadow == nil {
		return
	}

	if m.Shadow.Refusal != "" {
		fmt.Fprintf(b, "       ×  through a shadow table -- migr up refuses it: %s\n", m.Shadow.Refusal)

		return
	}

	fmt.Fprintf(b, "       ⧉  through a shadow table: copies %s into %s in chunks while reading its "+
		"changes from the replication log through %s, holds the copy in sync until the cutover is "+
		"confirmed, then swaps it in and keeps the original as %s\n", m.Shadow.Table, m.Shadow.Shadow,
		m.Shadow.Capture, m.Shadow.Retired)

	if m.Shadow.Stage != "" {
		fmt.Fprintf(b, "       ↻  carries on %s from log position %s, where the run that stopped "+
			"recorded it\n", m.Shadow.Stage, m.Shadow.Position)
	}
}

// shadowStatesOf reads where every shadow-table migration stands, where the history remembers it at all.
func shadowStatesOf(ctx context.Context, store history.Store) ([]history.ShadowState, error) {
	remembering, remembers := store.(history.Shadows)
	if !remembers {
		return nil, nil
	}

	states, err := remembering.ShadowStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading where a shadow-table migration stands: %w", err)
	}

	return states, nil
}

// withShadows notes where every dirty shadow-table migration stands, beside the run that left it.
func (r statusReport) withShadows(states []history.ShadowState) statusReport {
	for i, row := range r.Statuses {
		state, found := history.FindShadow(states, row.Version)
		if row.State != history.StateDirty || !found {
			continue
		}

		r.Statuses[i].Shadow = &shadowDoc{Table: state.Table, Shadow: state.Shadow, Retired: state.Retired,
			Capture: state.Capture, Stage: state.Stage, Position: state.Position}
	}

	return r
}

// describeShadowStopped says where a stopped shadow-table migration stands and the two ways on.
func describeShadowStopped(at *shadowDoc) string {
	return fmt.Sprintf("stopped %s through the shadow table %s at log position %s, which it and the "+
		"change stream %s are kept for -- migr up carries it on, and migr resolve --rolled-back "+
		"abandons it", at.Stage, at.Shadow, at.Position, at.Capture)
}

// abandonsOf renders the shadow-table migration a rollback abandons, and nothing where it abandons none.
func abandonsOf(state *history.ShadowState) *shadowDoc {
	if state == nil {
		return nil
	}

	return &shadowDoc{Table: state.Table, Shadow: state.Shadow, Retired: state.Retired, Capture: state.Capture,
		Stage: state.Stage, Position: state.Position}
}
