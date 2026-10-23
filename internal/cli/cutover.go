package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/remedy"
)

// errNotWaiting marks a confirmation asked of a migration that is not held in sync waiting for one.
var errNotWaiting = errors.New("this migration is not waiting for a cutover")

// errUnknownCutover marks a confirmation naming a version no migration here carries.
var errUnknownCutover = errors.New("no migration carries this version")

// newCutoverCmd builds the `migr cutover` subcommand.
func newCutoverCmd(opts *options) *cobra.Command {
	var confirm string

	cmd := &cobra.Command{
		Use:   "cutover",
		Short: "List the shadow-table migrations waiting for a cutover, or confirm one",
		Long: "Lists every shadow-table migration held in sync and waiting for its cutover, with how\n" +
			"far its copy trailed the replication log when it was last read and how long ago that\n" +
			"was. --confirm VERSION records the confirmation the run holding the copy waits for; it\n" +
			"takes no migration lock, since that run holds it for the whole hold.",
		Args: argsOf(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCutover(cmd, opts, confirm)
		},
	}

	cmd.Flags().StringVar(&confirm, "confirm", "", "confirm the cutover of the migration with this version")

	return cmd
}

// cutoverRow is one shadow-table migration that has not cut over, and how current its copy is.
type cutoverRow struct {
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in, empty when no file carries the version.
	File string `json:"file,omitempty"`
	// State is waiting once its copy is held in sync, and dirty while it is still copying.
	State history.State `json:"state"`
	// Shadow is where it stands, and for a waiting one the lag its holder last measured.
	Shadow *shadowDoc `json:"shadow"`
}

// confirmedCutover is the confirmation this invocation recorded, and the reading it was given against.
type confirmedCutover struct {
	// Version is the migration confirmed.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file,omitempty"`
	// Already reports a cutover that was confirmed before this invocation, which recorded nothing new.
	Already bool `json:"already,omitempty"`
	// Shadow is where it stood when confirmed, with the lag the confirmation was given against.
	Shadow *shadowDoc `json:"shadow"`
}

// cutoverReport is what migr cutover found, and what it confirmed where it was asked to.
type cutoverReport struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Shadows are the shadow-table migrations that have not cut over, waiting ones first.
	Shadows []cutoverRow `json:"shadows"`
	// Confirmed is the confirmation recorded, absent where none was asked for.
	Confirmed *confirmedCutover `json:"confirmed,omitempty"`
	// OrphanedSlots are the replication slots no live shadow-table migration owns, absent where there are none.
	OrphanedSlots []orphanedSlot `json:"orphaned_slots,omitempty"`
}

// runCutover lists what waits for a cutover, and records a confirmation where one is asked for.
func runCutover(cmd *cobra.Command, opts *options, confirm string) (err error) {
	t, err := resolve(opts)
	if err != nil {
		return err
	}

	found, files, err := t.files(opts)
	if err != nil {
		return err
	}

	s, err := t.connect(cmd.Context(), opts)
	if err != nil {
		return err
	}

	defer func() {
		err = errors.Join(err, s.close(context.WithoutCancel(cmd.Context())))
	}()

	report, err := s.cutovers(cmd.Context(), files, opts.clock())
	if err != nil {
		return err
	}

	if confirm != "" {
		confirmed, err := s.confirmCutover(cmd.Context(), found, report, confirm)
		if err != nil {
			return err
		}

		if report, err = s.cutovers(cmd.Context(), files, opts.clock()); err != nil {
			return err
		}

		confirmed.Shadow, report.Confirmed = confirmedNow(report, confirmed), confirmed
	}

	return writeResult(cmd.OutOrStdout(), opts, report)
}

// cutovers reads every shadow-table migration that has not cut over, and how current each copy is.
func (s *session) cutovers(ctx context.Context, files []history.File, now time.Time) (cutoverReport, error) {
	reconciled, err := s.reconcile(ctx, files)
	if err != nil {
		return cutoverReport{}, err
	}

	states, err := history.ReadShadows(ctx, s.conn.History())
	if err != nil {
		return cutoverReport{}, err
	}

	orphans, err := orphansOf(ctx, s.conn, engine.Carried(reconciled), states)
	if err != nil {
		return cutoverReport{}, err
	}

	statuses, _ := reconciled.Resolved()
	report := cutoverReport{Database: s.database, Shadows: []cutoverRow{}, OrphanedSlots: orphans}

	for _, want := range []history.State{history.StateWaiting, history.StateDirty} {
		for _, st := range statuses {
			if row, listed := cutoverRowOf(st, states, want, now); listed {
				report.Shadows = append(report.Shadows, row)
			}
		}
	}

	return report, nil
}

// cutoverRowOf renders one reconciled migration in a given state, where it has a shadow table that has not cut over.
func cutoverRowOf(st history.Status, states []history.ShadowState, want history.State,
	now time.Time) (cutoverRow, bool) {
	state, found := history.FindShadow(states, st.Version)
	if st.State != want || !found || state.Stage == history.StageCutOver {
		return cutoverRow{}, false
	}

	row := cutoverRow{Version: st.Version, File: describeFile(st.Migration), State: st.State}

	if st.State == history.StateWaiting {
		row.Shadow = waitingOf(state, now)
	} else {
		row.Shadow = abandonsOf(&state)
		row.Shadow.Copied = state.Copied
	}

	return row, true
}

// confirmCutover records the confirmation one waiting migration's holder reads, refusing any other.
func (s *session) confirmCutover(ctx context.Context, found []migration.Migration, report cutoverReport,
	version string) (*confirmedCutover, error) {
	row, listed := findCutover(report, version)
	if !listed {
		return nil, notWaitingError(found, version)
	}

	if row.State != history.StateWaiting {
		return nil, NewExitError(ExitRuntime, remedy.With(fmt.Errorf("%w: %s is still copying into "+
			"the shadow table %s (%d rows so far), and a cutover is confirmable once its copy is held in "+
			"sync", errNotWaiting, describeCutover(row), row.Shadow.Shadow, row.Shadow.Copied),
			"let migr up carry the copy on, and confirm once migr cutover lists it as waiting"))
	}

	confirmed := &confirmedCutover{Version: row.Version, File: row.File, Shadow: row.Shadow,
		Already: row.Shadow.Confirmed}

	if confirmed.Already {
		return confirmed, nil
	}

	store, remembers := s.conn.History().(history.Shadows)
	if !remembers {
		return nil, remedy.With(fmt.Errorf("this session's history cannot record a cutover "+
			"confirmation for %s", describeCutover(row)), "this is a bug in the driver, whose history "+
			"store records no confirmations: report it at https://github.com/emrebdr/migr/issues")
	}

	if err := store.ConfirmShadow(ctx, version); err != nil {
		return nil, remedy.Fallback(fmt.Errorf("confirming the cutover of %s: %w", describeCutover(row),
			err), "run migr cutover --confirm again once the database answers")
	}

	return confirmed, s.checkConfirmed(ctx, row)
}

// checkConfirmed reads the confirmation back, since an abandon between the two reads leaves nothing to confirm.
func (s *session) checkConfirmed(ctx context.Context, row cutoverRow) error {
	states, err := history.ReadShadows(ctx, s.conn.History())
	if err != nil {
		return err
	}

	state, found := history.FindShadow(states, row.Version)
	if found && state.Confirmed {
		return nil
	}

	return NewExitError(ExitRuntime, remedy.With(fmt.Errorf("%w: %s was abandoned or cut over while "+
		"it was being confirmed, so no confirmation is recorded", errNotWaiting, describeCutover(row)),
		"run migr cutover to list what still waits, and migr status for where this one stands"))
}

// findCutover returns the listed shadow-table migration with this version, where it is listed at all.
func findCutover(report cutoverReport, version string) (cutoverRow, bool) {
	for _, row := range report.Shadows {
		if row.Version == version {
			return row, true
		}
	}

	return cutoverRow{}, false
}

// notWaitingError refuses a confirmation of something no shadow table holds: a typo, or a migration in another state.
func notWaitingError(found []migration.Migration, version string) error {
	for _, m := range found {
		if m.Version == version {
			return NewExitError(ExitRuntime, remedy.With(fmt.Errorf("%w: %s has no shadow table held "+
				"in sync", errNotWaiting, m.Filename()), "run migr cutover to list what waits, and migr "+
				"status for the state this one is in"))
		}
	}

	return NewExitError(ExitValidation, remedy.With(fmt.Errorf("%w: %q", errUnknownCutover, version),
		"pass --confirm one of the versions migr cutover lists"))
}

// confirmedNow returns the confirmed migration's state as read after the confirmation, else as it was before.
func confirmedNow(report cutoverReport, confirmed *confirmedCutover) *shadowDoc {
	if row, listed := findCutover(report, confirmed.Version); listed && row.Shadow != nil {
		return row.Shadow
	}

	return confirmed.Shadow
}

// describeCutover names a listed migration by its file, falling back to its version.
func describeCutover(row cutoverRow) string {
	if row.File != "" {
		return row.File
	}

	return "version " + row.Version
}

// String renders the confirmation where one was asked for, then every migration that has not cut over.
func (r cutoverReport) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "cutover — database %q\n", r.Database)

	if r.Confirmed != nil {
		writeConfirmed(&b, r.Confirmed)
	}

	if len(r.Shadows) == 0 {
		b.WriteString("\nnothing is waiting for a cutover: no shadow-table migration is held in sync or copying.\n")
		writeOrphans(&b, r.OrphanedSlots)

		return b.String()
	}

	b.WriteString("\n")

	waiting := 0

	for _, row := range r.Shadows {
		fmt.Fprintf(&b, "  %s  %s  %s\n      %s\n", row.Version, describeCutover(row), row.State,
			describeCutoverRow(row))

		if row.State == history.StateWaiting {
			waiting++
		}
	}

	fmt.Fprintf(&b, "\n%d waiting for a cutover confirmation, %d still copying\n", waiting,
		len(r.Shadows)-waiting)
	writeOrphans(&b, r.OrphanedSlots)

	return b.String()
}

// describeCutoverRow says how current one listed migration's copy is, and what moves it on.
func describeCutoverRow(row cutoverRow) string {
	if row.State == history.StateWaiting {
		return describeWaiting(row.Shadow, row.Version)
	}

	return fmt.Sprintf("still copying into the shadow table %s: %d rows so far -- a cutover is "+
		"confirmable once the copy is held in sync", row.Shadow.Shadow, row.Shadow.Copied)
}

// writeConfirmed says what was confirmed, the reading it was confirmed against, and what performs the swap.
func writeConfirmed(b *strings.Builder, confirmed *confirmedCutover) {
	verb := "confirmed"
	if confirmed.Already {
		verb = "already confirmed"
	}

	fmt.Fprintf(b, "\n%s the cutover of %s.\n", verb, describeCutover(cutoverRow{Version: confirmed.Version,
		File: confirmed.File}))

	at := confirmed.Shadow
	if at == nil || at.Held == nil {
		return
	}

	fmt.Fprintf(b, "At its last read the copy trailed the log by %s, %s before this confirmation.\n",
		roundedSeconds(time.Duration(*at.Behind)), roundedSeconds(at.age))

	if *at.Held {
		b.WriteString("The run holding it reads the confirmation within a second, catches up under a " +
			"lock bounded by safety.lock_timeout, and swaps the shadow table in.\n")

		return
	}

	b.WriteString("No run is holding it in sync, so nothing swaps it yet: migr up resumes the hold, " +
		"catches up, and cuts it over.\n")
}
