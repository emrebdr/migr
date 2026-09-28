package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/source/sql"
	"github.com/emrebdr/migr/internal/validate"
)

// noDatabase is what every validation run says for itself, since the claim is the command's point.
const noDatabase = "no database was opened"

// newValidateCmd builds the `migr validate` subcommand.
func newValidateCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check every migration without connecting to anything",
		Long: "Reads every migration, compiles it for the configured engine, and checks that\n" +
			"something can undo it — reporting every problem it finds rather than stopping at\n" +
			"the first. It opens no database, so it runs anywhere the migrations do.",
		Args: argsOf(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runValidate(cmd, opts)
		},
	}
}

// runValidate checks every migration and reports what is wrong with them, connecting to nothing.
func runValidate(cmd *cobra.Command, opts *options) (err error) {
	t, err := resolve(opts)
	if err != nil {
		return err
	}

	found, err := t.discovered(opts)
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

	d, err := t.compiler(opts)
	if err != nil {
		return err
	}

	report, err := validate.Run(cmd.Context(), validate.Options{
		Migrations: ordered,
		Source:     read.source,
		Planner:    plan.Planner{Compiler: d},
	})
	if err != nil {
		return err
	}

	return finish(cmd, opts, validationOf(t.name, t.db.Driver, t.cfg.Migrations.Language, report))
}

// finish prints a validation report, and fails when the report is what a validation failure is.
func finish(cmd *cobra.Command, opts *options, result validation) error {
	if err := writeResult(cmd.OutOrStdout(), opts, result); err != nil {
		return err
	}

	if len(result.Problems) == 0 {
		return nil
	}

	return NewExitError(ExitValidation, errValidationFailed)
}

// fault is one problem a validation run found, in the form --json reports it.
type fault struct {
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Check is what found the problem.
	Check validate.Check `json:"check"`
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
	// Language is what this project's migrations are written in, which is how a remedy is phrased.
	Language config.Language `json:"language"`
	// Checked is how many migrations were looked at.
	Checked int `json:"migrations"`
	// Problems is everything found wrong, in the order the checks found it.
	Problems []fault `json:"problems"`
	// Unchecked is every half nothing could judge, which is not the same as one that passed.
	Unchecked []unchecked `json:"unchecked,omitempty"`
}

// validationOf renders what a validation run found against one project.
func validationOf(database, driverName string, lang config.Language,
	report validate.Report) validation {
	problems := report.Problems()

	found := make([]fault, 0, len(problems))
	for _, problem := range problems {
		found = append(found, faultOf(problem))
	}

	return validation{
		Database:  database,
		Driver:    driverName,
		Language:  lang,
		Checked:   report.Checked(),
		Problems:  found,
		Unchecked: uncheckedOf(report.Unchecked()),
	}
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
		Direction: problem.Direction,
		Operation: problem.Operation,
		Kind:      problem.Kind,
		Detail:    problem.Detail,
	}
}

// String renders every problem under the migration it belongs to, then what the run adds up to.
func (v validation) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "validate — database %q (%s), %s\n", v.Database, v.Driver,
		describeMigrations(v.Checked))

	v.writeUnchecked(&b)

	if len(v.Problems) == 0 {
		fmt.Fprintf(&b, "\nno problems found. %s\n", v.describeDatabase())

		return b.String()
	}

	v.writeProblems(&b)

	fmt.Fprintf(&b, "\n%s in %s. %s\n", describeProblems(len(v.Problems)),
		describeMigrations(v.faulted()), v.describeDatabase())

	if remedy := v.describeRemedy(); remedy != "" {
		fmt.Fprintf(&b, "\n%s\n", remedy)
	}

	return b.String()
}

// describeDatabase says what opening no database cost this run, which for most projects is nothing.
func (v validation) describeDatabase() string {
	if len(v.Unchecked) == 0 {
		return noDatabase + "."
	}

	return fmt.Sprintf("%s, so %s could not be made.", noDatabase,
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
	if v.Language != config.LanguageSQL || !v.found(validate.CheckReverse) {
		return ""
	}

	return fmt.Sprintf("A %s migration reaches the database as a raw operation, whose effect migr\n"+
		"cannot read, so nothing can invert it: write a %q section, or declare\n"+
		"%s <why it cannot be undone>.", sql.Ext, sql.MarkerDown, sql.MarkerIrreversible)
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

		fmt.Fprintf(b, "    %-*s  %s%s\n", widest(v.Problems, checkOfFault), problem.Check,
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

// checkOfFault names what found one problem.
func checkOfFault(problem fault) string { return problem.Check.String() }

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
