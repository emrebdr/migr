package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// ErrCannotShadow marks a shadow-table migration refused before anything was created, naming what is missing.
var ErrCannotShadow = errors.New("this migration cannot run through a shadow table here")

// ErrAwaitingCutover marks a run that ended with a shadow-table migration held in sync, waiting for its cutover.
var ErrAwaitingCutover = errors.New("a shadow-table migration is waiting for its cutover to be confirmed")

// ErrWaiting marks a command refused because a shadow-table migration is waiting for its cutover.
var ErrWaiting = errors.New("a shadow-table migration is waiting for its cutover")

// ErrNotCutOver marks a retirement asked of a shadow-table migration whose original has not been swapped out.
var ErrNotCutOver = errors.New("this shadow-table migration has not cut over")

// errShadowing is what the row a shadow-table migration writes at its start records, until it cuts over.
var errShadowing = errors.New("running through a shadow table: the copy is kept current from the " +
	"replication log until migr cutover --confirm confirms it -- the next migr up carries it on from the " +
	"recorded log position, and migr resolve --rolled-back abandons it, dropping the shadow and " +
	"releasing the capture")

// identifierLimit is the shortest identifier limit among the engines, which every derived name fits.
const identifierLimit = 63

// hashWidth is how many hexadecimal digits of a digest a shortened name keeps.
const hashWidth = 8

// shadowPoll is how long a hold waits after reading no change, before reading the log again.
const shadowPoll = time.Second

// ShadowHeartbeat is the longest a hold goes without recording its lag, so a reader can tell a held copy from an abandoned one.
const ShadowHeartbeat = 5 * time.Second

// Capturer opens the change stream a shadow-table migration keeps its copy current from.
type Capturer func(ctx context.Context, req capture.Request) (capture.Stream, error)

// ShadowProgress is one moment of a shadow-table migration, reported as each piece of work commits.
type ShadowProgress struct {
	// Migration is the migration running through the shadow table.
	Migration migration.Migration
	// Stage is where it stands.
	Stage history.Stage
	// Table is the table it changes.
	Table string
	// Shadow is the copy it changes instead.
	Shadow string
	// Copied is how many rows the copy has written.
	Copied int64
	// Applied is how many captured changes have been written to the copy.
	Applied int64
	// Batch is how many changes the read just reported applied, zero for a copy chunk.
	Batch int
	// Position is the log position every change before it has been applied from.
	Position string
	// Behind is how far the stream trailed the log's end at its last read, as the server measures it.
	Behind time.Duration
	// Holding reports a filled copy held current while it waits for its cutover to be confirmed.
	Holding bool
	// Resumed reports a run carrying on from a stage an earlier run recorded.
	Resumed bool
}

// ShadowRun is what a shadow-table migration that cut over did.
type ShadowRun struct {
	// Table is the table it changed.
	Table string
	// Retired is the name the original keeps, until something retires it.
	Retired string
	// Copied is how many rows the copy wrote.
	Copied int64
	// Applied is how many captured changes were written to the copy.
	Applied int64
	// Unattended reports a cutover made without a confirmation, because the run was told not to wait for one.
	Unattended bool
	// Behind is how far the stream trailed the log at the read an unattended run cut over after.
	Behind time.Duration
}

// ShadowName derives the copy a migration changes a table through, unique to the migration.
func ShadowName(table, version string) string {
	return derived("_"+table+"_migr_"+strings.ToLower(version), "_new")
}

// RetiredName derives the name the original table keeps after a migration's cutover, unique to the migration.
func RetiredName(table, version string) string {
	return derived("_"+table+"_migr_"+strings.ToLower(version), "_old")
}

// CaptureName derives the server-side resource a migration's change stream reads through.
func CaptureName(table, version string) string {
	return "migr_" + strings.ToLower(version) + "_" + digest(table)
}

// derived joins a stem and a suffix, shortening the stem with a digest where the whole would not fit.
func derived(stem, suffix string) string {
	if len(stem)+len(suffix) <= identifierLimit {
		return stem + suffix
	}

	keep := identifierLimit - len(suffix) - hashWidth - 1

	return stem[:keep] + "_" + digest(stem) + suffix
}

// digest returns a short, stable hexadecimal digest of a name.
func digest(name string) string {
	sum := sha256.Sum256([]byte(name))

	return hex.EncodeToString(sum[:])[:hashWidth]
}

// shadows reports whether a migration declared that it runs through a shadow table.
func (e *executor) shadows(m migration.Migration) bool {
	return e.opts.Online[m.Version] == migration.ShadowTable
}

// shadowJob is everything one shadow-table migration runs with: what it changes, through what, and how far it got.
type shadowJob struct {
	// ready is the migration, compiled and marked shadowed.
	ready prepared
	// shadow is the table, its copy, and the name it retires under.
	shadow driver.Shadow
	// request is the change stream it reads.
	request capture.Request
	// stream is the change stream open now, which a cutover that rolled back reopens.
	stream capture.Stream
	// shadower performs the copy, the replay, and the swap.
	shadower driver.Shadower
	// store remembers where it stands.
	store history.Shadows
	// rec is the row it writes, at the ordinal it started at.
	rec history.Record
	// state is where it stands, and its zero value before it has started.
	state history.ShadowState
	// saved is the state as the store last recorded it, which a hold's heartbeat is measured from.
	saved history.ShadowState
	// marked reports that the database holds the row and the state saying it is in progress.
	marked bool
	// markedHere reports that this run wrote them, and so writes everything after one ordinal on.
	markedHere bool
	// cut reports a cutover this run recorded.
	cut bool
	// unattended reports a cutover this run made without reading a confirmation.
	unattended bool
	// cutBehind is how far the stream trailed at the read the unattended cutover followed.
	cutBehind time.Duration
	// resumed reports a run carrying on from a recorded stage.
	resumed bool
	// paused is how often and how long a pacer held its copy back.
	paused held
	// tried is how often a write was tried again after giving up on a lock.
	tried attempts
}

// row returns the row the job's next write appends, at the ordinal that write takes.
func (j *shadowJob) row() history.Record {
	rec := j.rec
	if j.markedHere {
		rec.Ordinal++
	}

	return rec
}

// shadowing returns what a shadow-table migration runs through, refusing what this run was not given.
func (e *executor) shadowing(m migration.Migration) (driver.Shadower, history.Shadows, error) {
	if reason := e.conn.Capabilities().Capture().Reason(); reason != "" {
		return nil, nil, shadowRefusal(m, reason)
	}

	if e.opts.Shadower == nil {
		return nil, nil, shadowRefusal(m, fmt.Sprintf("the %s driver does not perform shadow-table "+
			"migration", e.conn.Capabilities().Driver()))
	}

	if e.opts.Capture == nil {
		return nil, nil, shadowRefusal(m, "this run was given no way to open a change stream")
	}

	store, remembers := e.store.(history.Shadows)
	if !remembers {
		return nil, nil, shadowRefusal(m, "this session's history cannot record where a shadow-table "+
			"migration stands")
	}

	return e.opts.Shadower, store, nil
}

// shadowRefusal refuses a shadow-table migration before anything was created, saying why.
func shadowRefusal(m migration.Migration, reason string) error {
	return fmt.Errorf("%w: %s declares it runs through a shadow table, and %s -- nothing was created",
		ErrCannotShadow, m.Filename(), reason)
}

// prepareShadow checks everything a shadow-table migration needs, before anything is created on the server.
func (e *executor) prepareShadow(ctx context.Context, ready prepared, from *history.ShadowState,
) (*shadowJob, error) {
	m := ready.item.migration

	shadower, store, err := e.shadowing(m)
	if err != nil {
		return nil, err
	}

	table, err := shadowedTable(m, ready.compiled, shadower)
	if err != nil {
		return nil, err
	}

	key, err := e.shadowKey(ctx, m, table, from)
	if err != nil {
		return nil, err
	}

	shadow := driver.Shadow{Table: table, Shadow: ShadowName(table, m.Version),
		Retired: RetiredName(table, m.Version), Key: key, Ops: operationsOf(ready.compiled)}

	if from == nil {
		if err := shadower.Admits(ctx, shadow); err != nil {
			return nil, shadowRefusal(m, err.Error())
		}
	}

	job := &shadowJob{
		ready:    prepared{item: ready.item, compiled: ready.compiled.Shadow(), sum: ready.sum},
		shadower: shadower,
		store:    store,
		rec:      e.newRecord(ready),
		shadow:   shadow,
		request:  capture.Request{Name: CaptureName(table, m.Version), Tables: []string{table}},
	}

	if from != nil {
		job.state, job.saved, job.marked, job.resumed = *from, *from, true, true
		job.request.From = capture.Position(from.Position)
	}

	return job, nil
}

// shadowedTable names the one existing table every operation changes, refusing what a shadow cannot carry.
func shadowedTable(m migration.Migration, compiled plan.Plan, shadower driver.Shadower) (string, error) {
	var tables []string

	for _, step := range compiled.Steps {
		table := plan.TableOf(step.Op)

		switch step.Op.(type) {
		case ir.AddPrimaryKey, ir.DropPrimaryKey:
			table = ""
		}

		if table == "" {
			return "", shadowRefusal(m, fmt.Sprintf("operation %d (%s) is not a change to one existing "+
				"table's columns, indexes or constraints, which is all a shadow table can carry -- the "+
				"copy is matched to the original by its primary key", step.Index, step.Op.Kind()))
		}

		if err := shadower.Shadows(step.Op); err != nil {
			return "", shadowRefusal(m, fmt.Sprintf("operation %d (%s): %v", step.Index,
				step.Op.Kind(), err))
		}

		if !slices.Contains(tables, table) {
			tables = append(tables, table)
		}
	}

	switch len(tables) {
	case 0:
		return "", shadowRefusal(m, "it emits no operation to carry")
	case 1:
		return tables[0], nil
	default:
		return "", shadowRefusal(m, fmt.Sprintf("it changes %d tables (%s), and a shadow-table "+
			"migration changes one", len(tables), strings.Join(tables, ", ")))
	}
}

// shadowKey reads the table's primary key and refuses a retired name already held, for a fresh start.
func (e *executor) shadowKey(ctx context.Context, m migration.Migration, table string,
	from *history.ShadowState) ([]string, error) {
	snapshot, err := e.conn.Introspect(ctx)
	if err == nil && snapshot == nil {
		err = errNoSnapshot
	}

	if err != nil {
		return nil, fmt.Errorf("reading the table a shadow-table migration copies: %w", err)
	}

	found, present := snapshot.Table(table)
	if !present && from != nil {
		found, present = snapshot.Table(from.Shadow)
	}

	switch {
	case !present:
		return nil, shadowRefusal(m, fmt.Sprintf("%q is not in the database", table))
	case found.PrimaryKey == nil || len(found.PrimaryKey.Columns) == 0:
		return nil, shadowRefusal(m, fmt.Sprintf("%q has no primary key, which the copy pages by and "+
			"every captured change is matched by", table))
	}

	if retired := RetiredName(table, m.Version); from == nil {
		if _, held := snapshot.Table(retired); held {
			return nil, shadowRefusal(m, fmt.Sprintf("%q already exists, and it is the name the "+
				"original keeps after the cutover", retired))
		}
	}

	return found.PrimaryKey.Columns, nil
}

// shadowStates reads where every shadow-table migration stands, and none where this history keeps none.
func (e *executor) shadowStates(ctx context.Context) ([]history.ShadowState, error) {
	store, remembers := e.store.(history.Shadows)
	if !remembers {
		return nil, nil
	}

	states, err := store.ShadowStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading where a shadow-table migration stands: %w", err)
	}

	return states, nil
}

// shadowProgress is the moment a job stands at, for whoever this run was told to tell.
func (j *shadowJob) shadowProgress() ShadowProgress {
	return ShadowProgress{
		Migration: j.ready.item.migration,
		Stage:     j.state.Stage,
		Table:     j.shadow.Table,
		Shadow:    j.shadow.Shadow,
		Copied:    j.state.Copied,
		Applied:   j.state.Applied,
		Position:  j.state.Position,
		Resumed:   j.resumed,
	}
}

// reportShadow hands one moment of a shadow-table migration to whoever this run was told to tell.
func (e *executor) reportShadow(p ShadowProgress) {
	if e.opts.Shadowed != nil {
		e.opts.Shadowed(p)
	}
}

// planShadow says how a migration declaring a shadow table would run, marking its plan, and nil for any other.
func (e *executor) planShadow(ctx context.Context, item queued, dir ir.Direction, ready *prepared,
	first bool) *ShadowPlan {
	if dir != ir.Up || !e.shadows(item.migration) {
		return nil
	}

	ready.compiled = ready.compiled.Shadow()
	shadow := &ShadowPlan{Resume: item.shadow}

	if !first {
		return e.planUnread(*ready, shadow)
	}

	job, err := e.prepareShadow(ctx, *ready, item.shadow)
	if err != nil {
		shadow.Refused = err

		return shadow
	}

	shadow.Table, shadow.Shadow = job.shadow.Table, job.shadow.Shadow
	shadow.Retired, shadow.Capture = job.shadow.Retired, job.request.Name
	shadow.Retention = e.conn.Capabilities().Capture().Retention

	return shadow
}

// planUnread judges a queued-behind migration on everything but its table, which the migrations ahead of it may change.
func (e *executor) planUnread(ready prepared, shadow *ShadowPlan) *ShadowPlan {
	m := ready.item.migration

	shadower, _, err := e.shadowing(m)
	if err == nil {
		shadow.Table, err = shadowedTable(m, ready.compiled, shadower)
	}

	if err != nil {
		shadow.Refused = err

		return shadow
	}

	shadow.Shadow, shadow.Retired = ShadowName(shadow.Table, m.Version), RetiredName(shadow.Table, m.Version)
	shadow.Capture, shadow.Unread = CaptureName(shadow.Table, m.Version), true
	shadow.Retention = e.conn.Capabilities().Capture().Retention

	return shadow
}
