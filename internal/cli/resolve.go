package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// errNoRepair marks a resolution that named none of the three things it could do.
var errNoRepair = errors.New("say what to do about the migration: one of " + repairFlags)

// errSeveralRepairs marks a resolution that named more than one, which are three different outcomes.
var errSeveralRepairs = errors.New("a resolution does one of " + repairFlags + ", so it takes one")

// repairFlags lists the three modes as the flags an operator types them as.
var repairFlags = strings.Join(repairNames(), ", ")

// repairNames renders every repair as the flag that asks for it.
func repairNames() []string {
	names := make([]string, 0, len(engine.Repairs()))
	for _, mode := range engine.Repairs() {
		names = append(names, "--"+mode.String())
	}

	return names
}

// asking is what the flags asked be done, before any of it becomes an engine.Repair.
type asking struct {
	// modes is one flag per repair, in the order Repairs lists them.
	modes map[engine.RepairMode]*bool
	// reason is the operator's account of what they did, which only an asserted repair carries.
	reason string
	// dry stops the resolution after its plan, having taken the lock and changed nothing.
	dry bool
}

// register binds one flag per repair, plus the reason an asserted one records and the dry run.
func (r *asking) register(cmd *cobra.Command) {
	r.modes = make(map[engine.RepairMode]*bool, len(engine.Repairs()))

	f := cmd.Flags()

	for _, mode := range engine.Repairs() {
		r.modes[mode] = f.Bool(mode.String(), false, describeRepair(mode))
	}

	f.StringVar(&r.reason, "reason", "", "what you did by hand, recorded in the history row")
	f.BoolVar(&r.dry, "dry-run", false, "report what would be changed, and change none of it")
}

// describeRepair says what one repair does, which is the help text beside its flag.
func describeRepair(mode engine.RepairMode) string {
	switch mode {
	case engine.RepairResume:
		return "run the statements the stopped run never reached"
	case engine.RepairApplied:
		return "record that you finished the migration by hand"
	case engine.RepairRolledBack:
		return "record that you undid the migration by hand"
	}

	return "this build has no note for that repair"
}

// repair turns the flags into what the engine is asked for, refusing none of them and two of them.
func (r asking) repair() (engine.Repair, error) {
	var chosen []engine.RepairMode

	for _, mode := range engine.Repairs() {
		if *r.modes[mode] {
			chosen = append(chosen, mode)
		}
	}

	if len(chosen) == 0 {
		return engine.Repair{}, errNoRepair
	}

	if len(chosen) > 1 {
		return engine.Repair{}, errSeveralRepairs
	}

	return engine.Repair{Mode: chosen[0], Reason: r.reason, DryRun: r.dry}, nil
}

// newResolveCmd builds the `migr resolve` subcommand.
func newResolveCmd(opts *options) *cobra.Command {
	ask := &asking{}

	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Settle a migration left between states",
		Long: "Resolves the one migration a stopped run left between states, by running the\n" +
			"statements it never reached or by recording what you did about it yourself. It\n" +
			"prints what it is about to change before changing it, and --dry-run prints only.",
		Args: argsOf(cobra.NoArgs),
	}

	ask.register(cmd)

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runResolve(cmd, opts, *ask)
	}

	return cmd
}

// runResolve settles the migration this database was left between states on, or reports that none is.
func runResolve(cmd *cobra.Command, opts *options, ask asking) error {
	repair, err := ask.repair()
	if err != nil {
		return NewExitError(ExitValidation, err)
	}

	if err := repair.Check(); err != nil {
		return NewExitError(ExitValidation, err)
	}

	settled, database, err := resolveAgainst(cmd, opts, repair)
	if err != nil {
		return err
	}

	return writeResult(cmd.OutOrStdout(), opts, resolutionOf(database, repair, settled))
}

// resolveAgainst opens the database the way this repair needs it opened, and resolves against it.
func resolveAgainst(cmd *cobra.Command, opts *options, repair engine.Repair,
) (settled engine.Resolution, database string, err error) {
	s, err := openFor(cmd.Context(), opts, repair)
	if err != nil {
		return engine.Resolution{}, "", err
	}

	defer func() {
		err = errors.Join(err, s.close(context.WithoutCancel(cmd.Context())))
	}()

	settled, err = engine.Resolve(cmd.Context(), s.conn, s.options, repair)

	return settled, s.database, err
}

// openFor opens a session that can perform this repair, starting a runtime only where one is needed.
func openFor(ctx context.Context, opts *options, repair engine.Repair) (*session, error) {
	if !repair.Mode.Asserts() {
		return open(ctx, opts, engine.Scope{})
	}

	return recording(ctx, opts)
}

// recording opens a session that may write a history row and read the files, and starts no runtime.
func recording(ctx context.Context, opts *options) (*session, error) {
	t, err := resolve(opts)
	if err != nil {
		return nil, err
	}

	_, files, err := t.files(opts)
	if err != nil {
		return nil, err
	}

	s, err := t.connect(ctx, opts)
	if err != nil {
		return nil, err
	}

	s.options.Migrations = files
	s.options.Clock = opts.clock

	return s, nil
}

// resuming is one statement a resume will run, at the position the whole migration numbers it.
type resuming struct {
	// Position is the statement's place in the whole migration, counting from one.
	Position int32 `json:"position"`
	// Kind is the operation it was compiled from.
	Kind ir.Kind `json:"kind"`
	// SQL is the statement text the driver compiled.
	SQL string `json:"sql"`
}

// resolution is what a resolution found and what it did about it, in the form --json reports it.
type resolution struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Repair is which of the three this invocation was asked for.
	Repair engine.RepairMode `json:"repair"`
	// Settled reports whether a migration between states was found at all.
	Settled bool `json:"settled"`
	// Changed reports whether anything was written or run, which a dry run never does.
	Changed bool `json:"changed"`
	// Version is the migration's UTC timestamp, absent where nothing was between states.
	Version string `json:"version,omitempty"`
	// File is the filename it was found in, absent where no file carries the version.
	File string `json:"file,omitempty"`
	// Run is the run that left it between states, which every rendering of this row shares.
	Run *recordedRun `json:"run,omitempty"`
	// Direction is the direction this repair records, which a resume takes from the stopped run.
	Direction ir.Direction `json:"direction,omitempty"`
	// Statements is how many the migration compiles to, absent on a repair that compiled nothing.
	Statements int32 `json:"statements,omitempty"`
	// Resuming is one entry per statement a resume will run, in the order it will run them.
	Resuming []resuming `json:"resuming,omitempty"`
	// Checkpoint is where a resume of a migration running in chunks carries on from.
	Checkpoint *checkpoint `json:"checkpoint,omitempty"`
	// Abandons is the shadow-table migration a rollback drops the copy of and releases the capture of.
	Abandons *shadowDoc `json:"abandons,omitempty"`
	// Reason is the operator's account, on an asserted repair.
	Reason string `json:"reason,omitempty"`
	// Batch is the batch this repair's own row is recorded under.
	Batch int64 `json:"batch,omitempty"`
	// Outcome is what this repair wrote the row as, absent where nothing was written.
	Outcome history.Outcome `json:"outcome,omitempty"`
	// Environment is the one a seed resolution's database runs under, absent from a migration's.
	Environment string `json:"environment,omitempty"`
	// about is what was between states, which the zero value reads as a migration.
	about subject
}

// resolutionOf renders what a resolution found and did, which is a third rendering of one history row.
func resolutionOf(database string, repair engine.Repair, settled engine.Resolution) resolution {
	if settled.Version == "" {
		return resolution{Database: database, Repair: repair.Mode}
	}

	return resolution{
		Database:   database,
		Repair:     settled.Mode,
		Settled:    true,
		Changed:    settled.Ran,
		Version:    settled.Version,
		File:       describeFile(settled.Migration),
		Run:        runOf(settled.Stopped),
		Direction:  settled.Direction,
		Statements: settled.Statements,
		Resuming:   resumingStatements(settled),
		Checkpoint: checkpointOf(settled.Checkpoint),
		Abandons:   abandonsOf(settled.Abandons),
		Reason:     settled.Reason,
		Batch:      settled.Batch,
		Outcome:    writtenOutcome(settled),
	}
}

// writtenOutcome names what this repair recorded, and nothing where it recorded nothing.
func writtenOutcome(settled engine.Resolution) history.Outcome {
	if !settled.Ran {
		return ""
	}

	if settled.Mode.Asserts() {
		return history.Resolved
	}

	return history.Applied
}

// resumingStatements lists the statements a resume will run, numbered as the whole migration numbers them.
func resumingStatements(settled engine.Resolution) []resuming {
	if settled.Mode.Asserts() || settled.Checkpoint != nil || settled.Pending() == 0 {
		return nil
	}

	stmts := settled.Plan.Statements()
	rows := make([]resuming, 0, settled.Pending())

	for position := settled.Stopped.LastStatement + 1; position <= settled.Statements; position++ {
		op, _ := settled.Plan.At(position)
		rows = append(rows, resuming{
			Position: position, Kind: kindOf(op), SQL: stmts[position-1].SQL,
		})
	}

	return rows
}

// String renders what was between states, what this repair will change about it, and what it changed.
func (r resolution) String() string {
	var b strings.Builder

	about := r.about.or()

	fmt.Fprintf(&b, "%sresolve — database %q%s, %s\n", about.command, r.Database,
		describeEnvironment(r.Environment), r.Repair)

	if !r.Settled {
		fmt.Fprintf(&b, "\nno %s here is between states, so there is nothing to resolve.\n",
			about.noun)

		return b.String()
	}

	fmt.Fprintf(&b, "\n  %s\n", describeResolved(r))
	fmt.Fprintf(&b, "  stopped after statement %d of the %s run, %s\n",
		r.Run.LastStatement, r.Run.Direction, r.Run.AppliedAt.Format(stamp))
	r.writeIntent(&b)
	r.writeOutcome(&b)

	return b.String()
}

// describeResolved names the migration this resolution is about, by version where no file carries it.
func describeResolved(r resolution) string {
	if r.File == "" {
		return "version " + r.Version
	}

	return r.File
}

// writeIntent says what this repair is about to change, which is the half an operator reads first.
func (r resolution) writeIntent(b *strings.Builder) {
	if r.Abandons != nil {
		fmt.Fprintf(b, "\n  will release the change stream %s and drop the shadow table %s, leaving %s as "+
			"it is,\n  then record it as %s in batch %d: %s\n", r.Abandons.Capture, r.Abandons.Shadow,
			r.Abandons.Table, describeRecorded(r.Repair), r.Batch, r.Reason)

		return
	}

	if r.Repair.Asserts() {
		fmt.Fprintf(b, "\n  will record it as %s in batch %d, running nothing: %s\n",
			describeRecorded(r.Repair), r.Batch, r.Reason)

		return
	}

	if r.Checkpoint != nil {
		fmt.Fprintf(b, "\n  will carry operation %d on in chunks after key (%s), where %s and %s "+
			"had\n  committed, then record the %s run in batch %d\n", r.Checkpoint.Operation,
			strings.Join(r.Checkpoint.Cursor, ", "), describeChunks(r.Checkpoint.Chunks),
			describeWritten(r.Checkpoint.Written), r.Direction, r.Batch)

		return
	}

	if len(r.Resuming) == 0 {
		fmt.Fprintf(b, "\n  every one of its %d statements had already run, so this records the run "+
			"the\n  bookkeeping never got to and executes nothing\n", r.Statements)

		return
	}

	fmt.Fprintf(b, "\n  will run %s of %d, then record the %s run in batch %d\n\n",
		describeResuming(r.Resuming), r.Statements, r.Direction, r.Batch)

	for _, stmt := range r.Resuming {
		stmt.write(b)
	}
}

// write renders one statement a resume will run, under the position the whole migration gives it.
func (s resuming) write(b *strings.Builder) {
	fmt.Fprintf(b, "       %d  %s\n%s\n", s.Position, s.Kind, indented(s.SQL))
}

// describeResuming names the range of statements a resume will run, as a range or as the one it is.
func describeResuming(rows []resuming) string {
	if len(rows) == 1 {
		return fmt.Sprintf("statement %d", rows[0].Position)
	}

	return fmt.Sprintf("statements %d through %d", rows[0].Position, rows[len(rows)-1].Position)
}

// describeRecorded says which way an asserted repair leaves the migration, in an operator's words.
func describeRecorded(mode engine.RepairMode) string {
	if mode == engine.RepairRolledBack {
		return "rolled back"
	}

	return "applied"
}

// writeOutcome says what this invocation actually did, which a dry run says it did not.
func (r resolution) writeOutcome(b *strings.Builder) {
	if !r.Changed {
		fmt.Fprintf(b, "\nnothing was changed and nothing was recorded. Run it without --dry-run "+
			"to resolve this.\n")

		return
	}

	fmt.Fprintf(b, "\nrecorded as %s in batch %d. This %s is no longer between states.\n",
		r.Outcome, r.Batch, r.about.or().noun)
}
