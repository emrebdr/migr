package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
)

// shadowing gives a run what a shadow-table migration changes its table through, and what it logs.
func (t *target) shadowing(options engine.Options, d driver.Driver, conn driver.Conn,
	opts *options) engine.Options {
	if shadower, shadows := conn.(driver.Shadower); shadows {
		options.Shadower = shadower
	}

	dsn := t.db.DSN.Reveal()
	options.Capture = func(ctx context.Context, req capture.Request) (capture.Stream, error) {
		return d.Capture(ctx, dsn, req)
	}
	options.CaptureLag = t.cfg.Safety.ReplicaLagThreshold.Duration()
	options.Shadowed = logShadow(opts.log, opts.unattended)
	options.Unattended = opts.unattended

	return options
}

// shadowsOf reports whether a session performs shadow-table migration.
func shadowsOf(conn driver.Conn) bool {
	_, shadows := conn.(driver.Shadower)

	return shadows
}

// logShadow logs each moment of a shadow-table migration, warning once as a hold begins so it never waits silently.
func logShadow(log *slog.Logger, unattended bool) func(engine.ShadowProgress) {
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
		case p.Holding && !warned && unattended:
			warned = true

			log.Info("the copy is filled: cutting over as soon as it is caught up, confirming nothing "+
				"(--unattended)", append(attrs, "behind", p.Behind)...)
		case p.Holding && !warned:
			warned = true

			log.Warn("holding the shadow table in sync until its cutover is confirmed -- migr cutover "+
				"--confirm "+p.Migration.Version+" confirms it; Ctrl-C detaches and keeps it, exiting 6, "+
				"and the next migr up carries the hold on", append(attrs, "behind", p.Behind)...)
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
	// Retention says how long the server keeps the log a stopped run reads on from, absent where it says nothing.
	Retention string `json:"retention,omitempty"`
	// Unread reports a migration whose table is read only when the run reaches it, behind the ones ahead of it.
	Unread bool `json:"unread,omitempty"`
	// Stage is where a stopped run stands, which this one carries on from, absent for a fresh start.
	Stage history.Stage `json:"stage,omitempty"`
	// Position is the log position a stopped run recorded, which this one reads on from.
	Position string `json:"position,omitempty"`
	// Copied is how many rows the copy wrote, where a run recorded any.
	Copied int64 `json:"copied,omitempty"`
	// Applied is how many captured changes were written to the copy, where a run recorded any.
	Applied int64 `json:"applied,omitempty"`
	// Behind is how far the stream trailed the log at its last read, present for one waiting for its cutover.
	Behind *elapsed `json:"behind_ms,omitempty"`
	// ReadAt is when the log was last read, present for one waiting for its cutover.
	ReadAt *time.Time `json:"read_at,omitempty"`
	// Held reports a reading recent enough that a run is holding the copy in sync now.
	Held *bool `json:"held,omitempty"`
	// Confirmed reports that its cutover is confirmed and waits for the run holding it to perform it.
	Confirmed bool `json:"confirmed,omitempty"`
	// age is how long before this report the log was last read.
	age time.Duration
	// Refusal is why migr up would refuse it before creating anything, absent where it would not.
	Refusal string `json:"refusal,omitempty"`
}

// shadowDocOf renders how a migration would run through a shadow table, and nothing where it declares none.
func shadowDocOf(planned *engine.ShadowPlan) *shadowDoc {
	if planned == nil {
		return nil
	}

	doc := &shadowDoc{Table: planned.Table, Shadow: planned.Shadow, Retired: planned.Retired,
		Capture: planned.Capture, Retention: planned.Retention, Unread: planned.Unread}

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

	if m.Shadow.Unread {
		fmt.Fprintf(b, "       ⧉  %s is read when the run reaches this migration -- its key, and what "+
			"this server can copy, are judged against the schema the migrations ahead of it leave\n",
			m.Shadow.Table)
	}

	if m.Shadow.Retention != "" {
		fmt.Fprintf(b, "       ⧉  a stopped run carries on only while the server keeps its log: %s\n",
			m.Shadow.Retention)
	}

	if m.Shadow.Stage != "" {
		fmt.Fprintf(b, "       ↻  carries on %s from log position %s, where the run that stopped "+
			"recorded it\n", m.Shadow.Stage, m.Shadow.Position)
	}
}

// staleAfter is how old a hold's last reading may be before no run is taken to be holding it, six heartbeats.
const staleAfter = 6 * engine.ShadowHeartbeat

// withShadows notes where every stopped or waiting shadow-table migration stands, beside the run that left it.
func (r statusReport) withShadows(states []history.ShadowState, now time.Time) statusReport {
	for i, row := range r.Statuses {
		state, found := history.FindShadow(states, row.Version)
		if !found {
			continue
		}

		switch row.State {
		case history.StateDirty:
			r.Statuses[i].Shadow = abandonsOf(&state)
		case history.StateWaiting:
			r.Statuses[i].Shadow = waitingOf(state, now)
		case history.StateApplied, history.StatePending, history.StateMissing, history.StateDrifted:
		}
	}

	return r
}

// waitingOf renders a shadow-table migration held in sync, with the lag its holder last measured and how old that is.
func waitingOf(state history.ShadowState, now time.Time) *shadowDoc {
	doc := abandonsOf(&state)
	behind, read := elapsed(state.Behind), state.UpdatedAt.UTC()
	held := now.Sub(read) < staleAfter

	doc.Copied, doc.Applied, doc.Confirmed = state.Copied, state.Applied, state.Confirmed
	doc.Behind, doc.ReadAt, doc.Held, doc.age = &behind, &read, &held, max(now.Sub(read), 0)

	return doc
}

// describeWaiting says how current a waiting shadow table is, whether anything holds it, and what confirms it.
func describeWaiting(at *shadowDoc, version string) string {
	if at == nil || at.Behind == nil || at.Held == nil {
		return "waiting for its cutover to be confirmed -- migr cutover --confirm " + version + " confirms it"
	}

	reading := fmt.Sprintf("it trailed the log by %s when it last read it, %s ago (log position %s, "+
		"%d changes applied)", roundedSeconds(time.Duration(*at.Behind)), roundedSeconds(at.age),
		at.Position, at.Applied)

	switch {
	case !*at.Held:
		return fmt.Sprintf("waiting for its cutover, and no run is holding the shadow table %s in sync: "+
			"%s -- changes since are not applied, and migr up resumes the hold%s", at.Shadow, reading,
			confirmation(at, version))
	case at.Confirmed:
		return fmt.Sprintf("held in sync through the shadow table %s: %s -- confirmed, so the run "+
			"holding it cuts it over at its next read", at.Shadow, reading)
	default:
		return fmt.Sprintf("held in sync through the shadow table %s: %s -- migr cutover --confirm %s "+
			"swaps it in, catching up under a lock bounded by safety.lock_timeout", at.Shadow, reading,
			version)
	}
}

// confirmation says whether a waiting migration's cutover is confirmed, or what confirms it.
func confirmation(at *shadowDoc, version string) string {
	if at.Confirmed {
		return "; its cutover is confirmed, so that run performs it"
	}

	return "; migr cutover --confirm " + version + " confirms the swap"
}

// roundedSeconds renders a duration to whole seconds, the precision a lag is measured at.
func roundedSeconds(d time.Duration) string {
	return d.Round(time.Second).String()
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
