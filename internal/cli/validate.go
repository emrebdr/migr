package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/rule"
	"github.com/emrebdr/migr/internal/source/sql"
	"github.com/emrebdr/migr/internal/validate"
)

// noDatabase is what an offline validation run says for itself, since the claim is the command's point.
const noDatabase = "no database was opened"

// missedCheck is what an offline run owes about the checks it could not make (M10-T3).
//
// A capability verdict is resolved inside Open, so a run with no connection cannot ask what this
// server will refuse -- and reporting "no problems found" without saying so would be a pass
// it did not earn. The reference check owes the same sentence for its own reason: neither of
// its two bases is sound alone, so a run with one of them judges no references at all. The remedy
// is in the sentence, since the flag is what closes both gaps.
const missedCheck = "Neither what this server refuses nor the references the migrations make was " +
	"checked;\n--connect asks the one migr.yaml names."

// newValidateCmd builds the `migr validate` subcommand.
func newValidateCmd(opts *options) *cobra.Command {
	var required bool

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check every migration without connecting to anything",
		Long: "Reads every migration, compiles it for the configured engine, and checks that\n" +
			"something can undo it — reporting every problem it finds rather than stopping at\n" +
			"the first. It opens no database, so it runs anywhere the migrations do.",
		Args: argsOf(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runValidate(cmd, opts, required)
		},
	}

	registerAllowDestructive(cmd, opts)
	registerConnect(cmd, opts)
	registerRequireSafe(cmd, &required)

	return cmd
}

// runValidate checks every migration and reports what is wrong with them, connecting only if asked.
func runValidate(cmd *cobra.Command, opts *options, required bool) (err error) {
	opts.connect = opts.connect || required

	t, err := resolve(opts)
	if err != nil {
		return err
	}

	found, err := t.discovered(opts)
	if err != nil {
		return err
	}

	answered, err := t.declared(cmd.Context(), opts)
	if err != nil {
		return err
	}

	read, err := t.offlineSource(cmd.Context(), opts, found)
	if err != nil {
		return err
	}

	defer func() {
		err = errors.Join(err, read.stop(context.WithoutCancel(cmd.Context())))
	}()

	ordered, err := overlaid(opts, found, read.declared)
	if err != nil {
		return err
	}

	if _, err := gatesOf(ordered, read.phases); err != nil {
		return err
	}

	d, err := t.compiler(opts)
	if err != nil {
		return err
	}

	rules, err := rulesFor(t)
	if err != nil {
		return err
	}

	report, err := validate.Run(cmd.Context(), validate.Options{
		Migrations:   ordered,
		Source:       read.source,
		Planner:      t.validating(d, answered, required),
		Allow:        allowanceFor(t, opts),
		Capabilities: answered.caps,
		Schema:       answered.live,
		Rules:        rules,
		Ceiling:      ceilingFor(t, required),
		Online:       read.online,
		Shadows:      answered.shadows,
	})
	if err != nil {
		return err
	}

	return finish(cmd, opts, validationOf(t, answered, report))
}

// finish prints a validation report, and fails when the report is what a validation failure is.
func finish(cmd *cobra.Command, opts *options, result validation) error {
	if err := writeResult(cmd.OutOrStdout(), opts, result); err != nil {
		return err
	}

	if len(result.Problems) == 0 {
		return nil
	}

	if over := overCeiling(result.Problems); over == len(result.Problems) {
		return fmt.Errorf("%w: %s safety.max_lock_impact, each listed above", engine.ErrLockImpact,
			describeExceeding(over))
	}

	return NewExitError(ExitValidation, errValidationFailed)
}

// A broken migration is the argument being wrong, so it is fixed first and exits 2 whatever else the
// report holds; exit 5 is reserved for a report whose only problems are operations over the ceiling,
// which is the one thing a CI job gating on lock impact needs to tell from a validation failure.

// overCeiling counts the problems the lock check found.
func overCeiling(problems []fault) int {
	n := 0

	for _, p := range problems {
		if p.Check == validate.CheckLock {
			n++
		}
	}

	return n
}

// validating compiles for a validation run, rewriting as the session would where the lock check reads it.
func (t *target) validating(d driver.Compiler, answered asked, required bool) plan.Planner {
	if !required {
		return plan.Planner{Compiler: d}
	}

	return plan.Planner{Compiler: d, Rewriter: answered.rewriter, Rewrites: t.cfg.Safety.Enabled()}
}

// ceilingFor is the ceiling a run gates on, and nothing for one that was not asked to.
func ceilingFor(t *target, required bool) capability.LockImpact {
	if !required {
		return ""
	}

	return t.cfg.Safety.MaxLockImpact
}

// fault is one problem a validation run found, in the form --json reports it.
type fault struct {
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Check is what found the problem.
	Check validate.Check `json:"check"`
	// Rule is the lint rule that found it, absent for a check that is not a rule.
	Rule rule.ID `json:"rule,omitempty"`
	// Direction is which of the migration's two halves the problem is in.
	Direction ir.Direction `json:"direction"`
	// Operation is its place in that half, counting from one, absent when the problem is not one.
	Operation int32 `json:"operation,omitempty"`
	// Kind is the operation at fault, absent when the problem is not about one.
	Kind ir.Kind `json:"kind,omitempty"`
	// Detail states what is wrong, in terms its author can act on.
	Detail string `json:"detail"`
}

// unchecked is one half of one migration a run could not judge, in the form --json reports it.
type unchecked struct {
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Direction is which of the migration's two halves went unchecked.
	Direction ir.Direction `json:"direction"`
	// Operation is the one operation this names, counting from one, and absent for a whole half.
	Operation int32 `json:"operation,omitempty"`
	// Kind is that operation, and is absent where a whole half went unchecked.
	Kind ir.Kind `json:"kind,omitempty"`
	// Reason states what stopped the check, in terms an operator can act on.
	Reason string `json:"reason"`
}

// validation is what one validation run found, in the form --json reports it.
type validation struct {
	// Database is the name migr.yaml holds the target under, whose driver the compiling was for.
	Database string `json:"database"`
	// Driver is the engine every migration was compiled for.
	Driver string `json:"driver"`
	// Connected reports whether the checks needing the target database were made at all.
	Connected bool `json:"connected"`
	// Server is the version those checks were resolved against, absent where none was reached.
	Server string `json:"server,omitempty"`
	// Language is what this project's migrations are written in, which is how a remedy is phrased.
	Language config.Language `json:"language"`
	// Checked is how many migrations were looked at.
	Checked int `json:"migrations"`
	// Problems is everything found wrong, in the order the checks found it.
	Problems []fault `json:"problems"`
	// Warnings is everything a rule set to warn found, which is judged and fails nothing.
	Warnings []fault `json:"warnings,omitempty"`
	// Unchecked is every half nothing could judge, which is not the same as one that passed.
	Unchecked []unchecked `json:"unchecked,omitempty"`
	// Projections names what the references were judged against, so no problems is not nothing read.
	Projections []string `json:"projections,omitempty"`
	// InvalidIndexes are the indexes a failed concurrent build or drop left INVALID, which --connect reads.
	InvalidIndexes []invalidIndex `json:"invalid_indexes,omitempty"`
}

// validationOf renders what a validation run found against one project.
func validationOf(t *target, answered asked, report validate.Report) validation {
	return validation{
		Database:    t.name,
		Driver:      t.db.Driver,
		Connected:   answered.caps != nil,
		Server:      serverOf(answered.caps),
		Language:    t.cfg.Migrations.Language,
		Checked:     report.Checked(),
		Problems:    faultsOf(report.Problems()),
		Warnings:    faultsOf(report.Warnings()),
		Unchecked:   uncheckedOf(report.Unchecked()),
		Projections: report.Projections(),

		InvalidIndexes: answered.invalid,
	}
}

// faultsOf renders one list of findings, allocating for an empty one so problems is [] not null.
func faultsOf(problems []validate.Problem) []fault {
	found := make([]fault, 0, len(problems))
	for _, problem := range problems {
		found = append(found, faultOf(problem))
	}

	return found
}

// serverOf names the server a declaration was resolved against, and nothing where none answered.
func serverOf(caps *capability.Set) string {
	if caps == nil {
		return ""
	}

	return caps.Server()
}

// uncheckedOf renders every half a run could not judge, naming the migration each belongs to.
func uncheckedOf(skipped []validate.Unchecked) []unchecked {
	if len(skipped) == 0 {
		return nil
	}

	out := make([]unchecked, 0, len(skipped))
	for _, s := range skipped {
		out = append(out, unchecked{
			Version:   s.Migration.Version,
			File:      s.Migration.Filename(),
			Direction: s.Direction,
			Operation: s.Operation,
			Kind:      s.Kind,
			Reason:    s.Reason,
		})
	}

	return out
}

// faultOf renders one problem, naming the migration it belongs to rather than repeating it in detail.
func faultOf(problem validate.Problem) fault {
	return fault{
		Version:   problem.Migration.Version,
		File:      problem.Migration.Filename(),
		Check:     problem.Check,
		Rule:      problem.Rule,
		Direction: problem.Direction,
		Operation: problem.Operation,
		Kind:      problem.Kind,
		Detail:    problem.Detail,
	}
}

// String renders every problem under the migration it belongs to, what the run adds up to, then any INVALID index.
func (v validation) String() string {
	var b strings.Builder

	b.WriteString(v.body())
	writeInvalidIndexes(&b, v.InvalidIndexes)

	return b.String()
}

// body renders every problem under the migration it belongs to, then what the run adds up to.
func (v validation) body() string {
	var b strings.Builder

	fmt.Fprintf(&b, "validate — database %q (%s), %s\n", v.Database, v.Driver,
		describeMigrations(v.Checked))

	v.writeUnchecked(&b)

	if len(v.Problems) == 0 {
		v.writeWarnings(&b)
		fmt.Fprintf(&b, "\nno problems found%s. %s\n", v.describeWarned(), v.describeDatabase())
		v.writeRemedy(&b)

		return b.String()
	}

	v.writeProblems(&b)
	v.writeWarnings(&b)

	fmt.Fprintf(&b, "\n%s in %s%s. %s\n", describeProblems(len(v.Problems)),
		describeMigrations(v.faulted()), v.describeWarned(), v.describeDatabase())

	v.writeRemedy(&b)

	return b.String()
}

// writeRemedy prints how this project answers what the checks found, where it can say.
func (v validation) writeRemedy(b *strings.Builder) {
	if remedy := v.describeRemedy(); remedy != "" {
		fmt.Fprintf(b, "\n%s\n", remedy)
	}
}

// describeWarned counts the findings that fail nothing, and says nothing where there are none.
func (v validation) describeWarned() string {
	switch len(v.Warnings) {
	case 0:
		return ""
	case 1:
		return ", 1 warning"
	default:
		return fmt.Sprintf(", %d warnings", len(v.Warnings))
	}
}

// writeWarnings lists what a rule set to warn found, beneath the problems, since it fails nothing.
func (v validation) writeWarnings(b *strings.Builder) {
	if len(v.Warnings) == 0 {
		return
	}

	fmt.Fprintf(b, "\n  warnings — %s\n", strings.TrimPrefix(v.describeWarned(), ", "))

	for _, warning := range v.Warnings {
		fmt.Fprintf(b, "    %s %s %s%s\n", warning.File, warning.Rule, describeWhere(warning),
			warning.Detail)
	}
}

// describeDatabase says what this run asked a database, and what it asked nothing at all.
func (v validation) describeDatabase() string {
	if !v.Connected {
		return fmt.Sprintf("%s%s. %s%s", noDatabase, v.describeUnmade("so"), missedCheck,
			v.describeProjections())
	}

	return fmt.Sprintf("%s%s.%s", v.describeServer(), v.describeUnmade("and"),
		v.describeProjections())
}

// A reference is reported only where every projection refused it, so naming them is what keeps "no
// problems found" from reading as "nothing was projected".

// describeProjections names what the references were judged against, and nothing where none were.
func (v validation) describeProjections() string {
	if len(v.Projections) == 0 {
		return ""
	}

	return fmt.Sprintf("\nReferences were projected from %s, and are\nreported only where every "+
		"one of them refused.", strings.Join(v.Projections, " and "))
}

// describeServer says which server the capability check was resolved against, naming the driver otherwise.
func (v validation) describeServer() string {
	if v.Server == "" {
		return fmt.Sprintf("every operation was checked against driver %q", v.Driver)
	}

	return fmt.Sprintf("every operation was checked against server %s", v.Server)
}

// describeUnmade counts the checks nothing could make, which is a migration's own reads and nothing else.
func (v validation) describeUnmade(conjunction string) string {
	if len(v.Unchecked) == 0 {
		return ""
	}

	return fmt.Sprintf(", %s %s could not be made", conjunction,
		describeUnchecked(len(v.Unchecked)))
}

// writeUnchecked lists every half nothing could judge, above the problems, since it qualifies them.
func (v validation) writeUnchecked(b *strings.Builder) {
	if len(v.Unchecked) == 0 {
		return
	}

	fmt.Fprintf(b, "\n  not checked — %s\n", describeUnchecked(len(v.Unchecked)))

	for _, skipped := range v.Unchecked {
		fmt.Fprintf(b, "    %s %s: %s\n", skipped.File, skipped.where(), skipped.Reason)
	}
}

// where names the half this entry covers, and the one operation within it where it covers only one.
func (u unchecked) where() string {
	if u.Operation == 0 {
		return string(u.Direction)
	}

	return fmt.Sprintf("%s operation %d (%s)", u.Direction, u.Operation, u.Kind)
}

// describeUnchecked counts the checks a run could not make, which is halves and operations both.
func describeUnchecked(n int) string {
	if n == 1 {
		return "1 check"
	}

	return fmt.Sprintf("%d checks", n)
}

// describeRemedy says how this project's own format answers a problem, since a check cannot know.
func (v validation) describeRemedy() string {
	remedies := make([]string, 0, 3)

	for _, remedy := range []string{v.describeReverseRemedy(), v.describeDestructiveRemedy(),
		v.describeLintRemedy()} {
		if remedy != "" {
			remedies = append(remedies, remedy)
		}
	}

	return strings.Join(remedies, "\n\n")
}

// describeLintRemedy says how a project changes what a rule is worth, naming the rules that fired.
func (v validation) describeLintRemedy() string {
	fired := v.ruled()
	if len(fired) == 0 {
		return ""
	}

	lines := make([]string, 0, len(fired))
	for _, id := range fired {
		lines = append(lines, "      "+id+": warn")
	}

	return fmt.Sprintf("A lint rule is worth what this project says it is. Set one to %s in "+
		"migr.yaml:\n\n  validation:\n    rules:\n%s", rule.JoinSeverities(),
		strings.Join(lines, "\n"))
}

// ruled returns the rules that failed this run, each named once, in the order they found something.
func (v validation) ruled() []string {
	var fired []string

	for _, found := range v.Problems {
		if found.Rule == "" || slices.Contains(fired, found.Rule.String()) {
			continue
		}

		fired = append(fired, found.Rule.String())
	}

	return fired
}

// describeReverseRemedy says how a SQL migration answers the question of what undoes it.
func (v validation) describeReverseRemedy() string {
	if v.Language != config.LanguageSQL || !v.found(validate.CheckReverse) {
		return ""
	}

	return fmt.Sprintf("A %s migration reaches the database as a raw operation, whose effect migr\n"+
		"cannot read, so nothing can invert it: write a %q section, or declare\n"+
		"%s <why it cannot be undone>.", sql.Ext, sql.MarkerDown, sql.MarkerIrreversible)
}

// describeDestructiveRemedy says how a project permits what it means to destroy, naming the kinds.
func (v validation) describeDestructiveRemedy() string {
	needed := v.destroying()
	if len(needed) == 0 {
		return ""
	}

	return fmt.Sprintf("%smigr up refuses these. Permit them for one run with --allow-destructive, "+
		"or for\nthis project by naming their kinds in migr.yaml:\n\n  validation:\n"+
		"    allow_destructive: [%s]", v.describeRawFormat(), strings.Join(needed, ", "))
}

// describeRawFormat says why a SQL project meets this for every migration it has, not for some.
func (v validation) describeRawFormat() string {
	if v.Language != config.LanguageSQL {
		return ""
	}

	return fmt.Sprintf("Every statement in a %s migration reaches the database as a raw operation, "+
		"whose\neffect migr cannot read -- so a SQL project permits that kind once or runs "+
		"nothing.\n\n", sql.Ext)
}

// destroying returns the kinds the destructive check found, each named once, in the order found.
func (v validation) destroying() []string {
	var needed []string

	for _, problem := range v.Problems {
		if problem.Check != validate.CheckDestructive || slices.Contains(needed, problem.Kind.String()) {
			continue
		}

		needed = append(needed, problem.Kind.String())
	}

	return needed
}

// found reports whether any problem came from one check.
func (v validation) found(check validate.Check) bool {
	for _, problem := range v.Problems {
		if problem.Check == check {
			return true
		}
	}

	return false
}

// writeProblems renders each migration's problems beneath its filename, in the order they were found.
func (v validation) writeProblems(b *strings.Builder) {
	previous := ""

	for _, problem := range v.Problems {
		if problem.File != previous {
			previous = problem.File

			fmt.Fprintf(b, "\n  %s\n", problem.File)
		}

		fmt.Fprintf(b, "    %-*s  %s%s\n", widest(v.Problems, labelOfFault), labelOfFault(problem),
			describeWhere(problem), problem.Detail)
	}
}

// faulted returns how many distinct migrations carry a problem, walking the ordered list.
func (v validation) faulted() int {
	total, previous := 0, ""

	for _, problem := range v.Problems {
		if problem.File != previous {
			previous = problem.File
			total++
		}
	}

	return total
}

// labelOfFault names what found one problem, which for a lint problem is the rule rather than lint.
func labelOfFault(problem fault) string {
	if problem.Rule != "" {
		return problem.Rule.String()
	}

	return problem.Check.String()
}

// describeWhere names the operation a problem sits at, and nothing for one that is not about one.
func describeWhere(problem fault) string {
	if problem.Operation == 0 {
		return ""
	}

	return fmt.Sprintf("%s operation %d (%s): ", problem.Direction, problem.Operation, problem.Kind)
}

// describeProblems counts problems with the right noun.
func describeProblems(n int) string {
	if n == 1 {
		return "1 problem"
	}

	return fmt.Sprintf("%d problems", n)
}
