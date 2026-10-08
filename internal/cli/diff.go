package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/diff"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/fold"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/schema"
	"github.com/emrebdr/migr/internal/source/golang"
	"github.com/emrebdr/migr/internal/source/typescript"
)

// defaultDiffName is what a generated migration is called when the invocation names nothing.
const defaultDiffName = "diff"

// newDiffCmd builds the `migr diff` subcommand.
func newDiffCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "diff [name]",
		Short: "Write the migration that closes the gap between this database and the migrations",
		Long: "Reads the live schema, folds every migration forward into the schema they describe,\n" +
			"and writes the migration that closes the gap between the two — in the configured\n" +
			"language, through the same writer `migr create` uses.\n\n" +
			"It takes the migration lock to read, releases it before asking anything, and changes\n" +
			"no database. It requires a settled, fully-migrated database: a diff against a project\n" +
			"half of whose migrations have not run would propose everything they do.",
		Args: argsOf(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiff(cmd, opts, nameOf(args))
		},
	}
}

// nameOf reads the name a generated migration is given, which the invocation may leave to default.
func nameOf(args []string) string {
	if len(args) == 0 {
		return defaultDiffName
	}

	return args[0]
}

// The whole command in the order it does it: read under the lock, release, then compute, ask, and
// write. A rename prompt waits on a person, and holding the migration lock across that would block
// every other migr run on the database for as long as they took to answer -- so the lock covers
// reading and not asking. A report takes no lock at all (ADR 0027) and this is not a report: it
// persists its answer into a file, so the answer has to be about a database nobody is changing.

// runDiff writes the migration that closes the gap between the live schema and the migrations.
func runDiff(cmd *cobra.Command, opts *options, name string) (err error) {
	t, err := resolve(opts)
	if err != nil {
		return err
	}

	target, err := claim(opts, t, name)
	if err != nil {
		return err
	}

	found, err := t.discovered(opts)
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

	read, err := s.readable(cmd.Context(), t, opts, found)
	if err != nil {
		return err
	}

	live, desired, err := s.schemas(cmd.Context(), opts, read)
	if err != nil {
		return err
	}

	return writeDiff(cmd, opts, t, written{live: live, desired: desired, file: target})
}

// The file is named and claimed before anything connects, so a name the grammar refuses and a
// version another migration already holds are both caught before a database is opened and before
// anybody is asked anything -- rather than after an operator has spent time at a prompt.

// claim names the migration this run will write and refuses it now rather than after the prompt.
func claim(opts *options, t *target, name string) (migration.Migration, error) {
	tmpl, err := templateFor(t.cfg.Migrations.Language)
	if err != nil {
		return migration.Migration{}, NewExitError(ExitValidation, err)
	}

	if _, known := renderers[t.cfg.Migrations.Language]; !known {
		return migration.Migration{}, NewExitError(ExitValidation,
			unwritableLanguage(t.cfg.Migrations.Language))
	}

	m, err := migration.New(opts.clock(), name, tmpl.ext)
	if err != nil {
		return migration.Migration{}, NewExitError(ExitValidation, err)
	}

	if err := checkUnclaimed(t.cfg.Migrations.Dir, m.Version); err != nil {
		return migration.Migration{}, err
	}

	return m, nil
}

// This command wires itself rather than going through attach, because it needs the migrations in
// run order as migrations and attach keeps only the hashed form -- history.File exposes no version
// on purpose, so that a pre-hashed slice cannot be permuted from here (M7-T11).

// readable is everything this command needs from the migrations: what reads them, and in what order.
type readable struct {
	// source is what each migration is asked through, which is a runtime for every language but sql.
	source engine.Source
	// ordered is the sequence the declarations and the timestamps together name.
	ordered []migration.Migration
	// files are the same migrations hashed, which is what reconciliation is given.
	files []history.File
}

// readable starts what reads this project's migrations and settles the order they fold in.
func (s *session) readable(ctx context.Context, t *target, opts *options,
	found []migration.Migration) (readable, error) {
	read, err := t.sources(ctx, opts, s.conn, found)
	if err != nil {
		return readable{}, err
	}

	s.stop = read.stop

	ordered, err := overlaid(opts, found, read.declared)
	if err != nil {
		return readable{}, err
	}

	files, err := hashed(t.fsys, ordered)
	if err != nil {
		return readable{}, err
	}

	return readable{source: read.source, ordered: ordered, files: files}, nil
}

// written is what one invocation of this command is working from, once the database has been read.
type written struct {
	// live is the schema the database holds, as this driver read it.
	live schema.Snapshot
	// desired is the schema every migration folded forward describes.
	desired schema.Snapshot
	// file is the migration this run will write, named and claimed before anything connected.
	file migration.Migration
}

// schemas reads the live schema and the one the migrations describe, under the lock and then not.
func (s *session) schemas(ctx context.Context, opts *options,
	read readable) (schema.Snapshot, schema.Snapshot, error) {
	unlock, err := s.conn.Lock(ctx)
	if err != nil {
		return schema.Snapshot{}, schema.Snapshot{}, err
	}

	live, desired, err := s.readSchemas(ctx, opts, read)

	return live, desired, errors.Join(err, unlock(context.WithoutCancel(ctx)))
}

// read folds the migrations forward and introspects the database, both while the lock is held.
func (s *session) readSchemas(ctx context.Context, opts *options,
	read readable) (schema.Snapshot, schema.Snapshot, error) {
	if err := s.checkSettled(ctx, read.files); err != nil {
		return schema.Snapshot{}, schema.Snapshot{}, err
	}

	live, err := s.conn.Introspect(ctx)
	if err != nil {
		return schema.Snapshot{}, schema.Snapshot{}, fmt.Errorf("reading the live schema: %w", err)
	}

	desired, err := s.project(ctx, read)
	if err != nil {
		return schema.Snapshot{}, schema.Snapshot{}, err
	}

	opts.log.Info("read both schemas", "live_tables", len(live.Tables),
		"desired_tables", len(desired.Tables))

	return *live, desired, nil
}

// A desired schema folded from every migration is the schema the database should have once they have
// all run, so a diff over a half-migrated project would propose everything the pending ones do and
// then duplicate them. Exit 1 rather than 2 throughout: the same command line succeeds once migr up
// has run, so it is the world that is not ready (ADR 0029).

// checkSettled refuses a database the migrations do not already describe, naming what is unsettled.
func (s *session) checkSettled(ctx context.Context, files []history.File) error {
	report, err := s.reconcile(ctx, files)
	if err != nil {
		return err
	}

	return checkStates(report)
}

// checkStates refuses every state but applied, since each of them means a different answer is wrong.
func checkStates(report history.Report) error {
	resolved, present := report.Resolved()
	if !present {
		return fmt.Errorf("this database is not the one the migrations describe: it has no "+
			"migration history table at all, so nothing says which of them it already holds. "+
			"Run `migr up` first: a diff folds every migration forward, so against a database "+
			"they have not run against it would propose everything they do: %w",
			history.ErrNoTable)
	}

	unsettled := map[history.State][]string{}

	for _, one := range resolved {
		if one.State != history.StateApplied {
			unsettled[one.State] = append(unsettled[one.State], describeUnsettled(one))
		}
	}

	for _, state := range history.States() {
		if named := unsettled[state]; len(named) > 0 {
			return unsettledError(state, named)
		}
	}

	return nil
}

// describeUnsettled names one unsettled migration, by its file where there is one and by version otherwise.
func describeUnsettled(one history.Status) string {
	if name := one.Migration.Filename(); name != "" {
		return name
	}

	return one.Version
}

// unsettledError says which migrations are not applied, and what to do about each state.
func unsettledError(state history.State, named []string) error {
	return fmt.Errorf("this database is not the one the migrations describe: %d %s (%s). %s",
		len(named), state, strings.Join(named, ", "), remedyFor(state))
}

// remedyFor names what settles each state, since a diff is only meaningful once one of them has run.
func remedyFor(state history.State) string {
	switch state {
	case history.StatePending:
		return "Run `migr up` first: a diff folds every migration forward, so against a database " +
			"they have not all run against it would propose everything they do"
	case history.StateMissing:
		return "This database recorded a migration that is not on disk, so what the project " +
			"describes cannot be folded at all -- restore the file or settle it with `migr resolve`"
	case history.StateDrifted:
		return "An applied migration's source has changed, so what was run is not what would be " +
			"folded (invariant #6)"
	case history.StateDirty:
		return "Settle it with `migr resolve` first: a database between states is not one a " +
			"schema can be read off"
	case history.StateApplied:
		return ""
	default:
		return ""
	}
}

// project folds every migration's up operations forward, which is the schema the project describes.
func (s *session) project(ctx context.Context, read readable) (schema.Snapshot, error) {
	var held schema.Snapshot

	for _, m := range read.ordered {
		emitted, err := read.source.Emit(ctx, m, ir.Up, engine.ModePlan)
		if err != nil {
			return schema.Snapshot{}, fmt.Errorf("%s: asking what it does: %w",
				m.Filename(), err)
		}

		folded, refused := fold.Forward(held, emitted.Operations)
		if len(refused) > 0 {
			return schema.Snapshot{}, unfoldableError(m, refused)
		}

		held = folded
	}

	return held, nil
}

// A project whose migrations cannot be folded is refused whole rather than diffed against a partial
// projection, because a desired schema missing whatever a statement built is one whose diff proposes
// dropping it. That makes migr diff unavailable to the .sql format, by the same property that
// already costs that format its derived down and its capability checks: every statement in one is
// ir.Raw, whose effect on the schema nothing can read.

// unfoldableError says which migration cannot be folded and why that ends the whole command.
func unfoldableError(m migration.Migration, refused []fold.Refusal) error {
	lines := make([]string, 0, len(refused))
	for _, one := range refused {
		lines = append(lines, "  "+one.Describe())
	}

	return NewExitError(ExitValidation, fmt.Errorf("%s: what the migrations describe cannot be "+
		"read off them, so there is nothing to compare this database against:\n%s\n\nA migration "+
		"whose effect migr cannot read leaves every schema after it unknowable, and diffing "+
		"against a partial reading would propose dropping whatever it built. Every .sql "+
		"migration is one of these", m.Filename(), strings.Join(lines, "\n")))
}

// writeDiff asks about the renames, closes the gap, and writes the migration that does it.
func writeDiff(cmd *cobra.Command, opts *options, t *target, w written) error {
	confirmed, err := confirmations(cmd, opts, w)
	if err != nil {
		return err
	}

	desired := fold.Comparable(w.live, w.desired)
	ops, refused := diff.Diff(w.live, desired, confirmed)

	result, err := generate(opts, t, w, ops, refused, confirmed)
	if err != nil {
		return err
	}

	return writeResult(cmd.OutOrStdout(), opts, result)
}

// A document on stdout and a prompt on stdout cannot both be stdout, and --json puts JSON logs on
// stderr too, so there is nowhere to ask. So a --json run does not ask: every candidate is reported
// unconfirmed, and a rename arrives as the drop and the create invariant #7 then gates -- which is
// the safe side, and is what ConfirmRenames's own default already is. Refusing outright would make
// this command unusable from the one place a JSON document is for.

// confirmations asks about every likely rename, and asks nothing at all where there is nowhere to ask.
func confirmations(cmd *cobra.Command, opts *options, w written) (diff.Renames, error) {
	if opts.jsonOut {
		return diff.Renames{}, nil
	}

	return ConfirmRenames(cmd.InOrStdin(), cmd.OutOrStdout(), w.live, w.desired)
}

// generate renders the operations into the configured language and writes the file they go in.
func generate(opts *options, t *target, w written, ops ir.Operations, refused []diff.Refusal,
	confirmed diff.Renames) (generated, error) {
	result := generated{
		Database:  t.name,
		Language:  t.cfg.Migrations.Language,
		Renames:   len(confirmed.Tables) + len(confirmed.Columns),
		Unmatched: unmatchedOf(w, confirmed),
		Refusals:  describedRefusals(refused),
	}

	if len(ops) == 0 {
		return result, nil
	}

	body, err := renderWith(t.cfg.Migrations.Language, w.file, ops)
	if err != nil {
		return generated{}, NewExitError(ExitValidation, err)
	}

	file, err := writeGenerated(opts, t, w.file, body)
	if err != nil {
		return generated{}, err
	}

	result.File = file
	result.Operations = kindNames(ops)

	return result, nil
}

// unmatchedOf names every rename this run proposed and nothing confirmed, which is what a drop is.
func unmatchedOf(w written, confirmed diff.Renames) []string {
	var unmatched []string

	for _, one := range diff.Candidates(w.live, w.desired, confirmed) {
		unmatched = append(unmatched, one.Describe())
	}

	return unmatched
}

// describedRefusals renders every change the differ would not propose as the lines it renders to.
func describedRefusals(refused []diff.Refusal) []string {
	lines := make([]string, 0, len(refused))
	for _, one := range refused {
		lines = append(lines, one.Describe())
	}

	return lines
}

// kindNames names the operations the generated migration carries, in the order it carries them.
func kindNames(ops ir.Operations) []ir.Kind {
	kinds := make([]ir.Kind, 0, len(ops))
	for _, op := range ops {
		kinds = append(kinds, op.Kind())
	}

	return kinds
}

// A generated migration goes through migration.New, checkUnclaimed and writeNew -- the same three
// migr create uses -- so there is one file format and one writer, and a version another file already
// claims is refused here exactly as it is there (ADR 0025, C).

// writeGenerated puts the generated body in the migration file this run claimed at the start.
func writeGenerated(opts *options, t *target, m migration.Migration, body string) (string, error) {
	path := filepath.Join(t.cfg.Migrations.Dir, m.Filename())
	if err := writeNew(path, body); err != nil {
		return "", err
	}

	opts.log.Info("wrote a generated migration", "file", path, "version", m.Version)

	return path, nil
}

// Both halves of the sql format's answer are in one refusal, because both are true and the second
// is the one an operator needs: that format cannot be written, and a project in it has no desired
// schema to write about either. Every statement in one is ir.Raw, whose effect nothing can read.

// unwritableLanguage says why a generated migration cannot be written in one language.
func unwritableLanguage(lang config.Language) error {
	reason := fmt.Sprintf("migrations.language: %s: nothing here writes a generated migration in "+
		"that language (expected one of: %s)", lang, joinLanguages(renderable()))

	if lang != config.LanguageSQL {
		return errors.New(reason)
	}

	return errors.New(reason + ". Nor would there be anything to write about: every statement in " +
		"a .sql migration reaches the database as a raw operation, whose effect on the schema " +
		"migr cannot read, so the schema those migrations describe cannot be read off them at all")
}

// renderWith writes the operations as the source of one language, refusing one that writes none.
func renderWith(lang config.Language, m migration.Migration,
	ops ir.Operations) (string, error) {
	render, known := renderers[lang]
	if !known {
		return "", unwritableLanguage(lang)
	}

	return render(m, ops)
}

// renderable names every language a generated migration can be written in, in declaration order.
func renderable() []config.Language {
	var langs []config.Language

	for _, lang := range config.Languages() {
		if _, known := renderers[lang]; known {
			langs = append(langs, lang)
		}
	}

	return langs
}

// generated is the migration this command wrote, in the form --json reports it.
type generated struct {
	// File is the path written, absent where the two schemas already agreed.
	File string `json:"file,omitempty"`
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Language is what the migration was written in, spelled as migr.yaml spells it.
	Language config.Language `json:"language"`
	// Operations are the operations it carries, in the order it carries them.
	Operations []ir.Kind `json:"operations,omitempty"`
	// Renames is how many renames were confirmed, which is how many drops did not happen.
	Renames int `json:"renames"`
	// Unmatched names every likely rename nothing confirmed, each of which is a drop and a create.
	Unmatched []string `json:"unmatched,omitempty"`
	// Refusals are the changes the differ would not propose, so the migration does not close the gap.
	Refusals []string `json:"refusals,omitempty"`
}

// A command that printed only the operations would write a migration that silently does not close
// the gap, so both of the differ's return values are rendered (ADR 0070, H).

// String renders what was written, what was not proposed, and what nothing confirmed.
func (g generated) String() string {
	var out strings.Builder

	writeDiffFile(&out, g)
	writeUnmatched(&out, g)
	writeRefusals(&out, g)

	return out.String()
}

// writeDiffFile names the migration that was written, or says the two schemas already agreed.
func writeDiffFile(out io.Writer, g generated) {
	if g.File == "" {
		fmt.Fprintf(out, "%s already holds the schema the migrations describe; nothing to write\n",
			g.Database)

		return
	}

	fmt.Fprintf(out, "wrote %s\n  %d operations: %s\n", g.File, len(g.Operations),
		joinKinds(g.Operations))

	if g.Renames > 0 {
		fmt.Fprintf(out, "  %d confirmed %s, which is %d drops that did not happen\n",
			g.Renames, plural(g.Renames, "rename", "renames"), g.Renames)
	}
}

// writeUnmatched names every likely rename nothing confirmed, each of which reached the file as a drop.
func writeUnmatched(out io.Writer, g generated) {
	if len(g.Unmatched) == 0 {
		return
	}

	fmt.Fprintf(out, "\n%d likely %s nothing confirmed, so each is a drop and a create:\n",
		len(g.Unmatched), plural(len(g.Unmatched), "rename", "renames"))

	for _, one := range g.Unmatched {
		fmt.Fprintf(out, "  ~ %s\n", one)
	}
}

// writeRefusals names every change the differ would not propose, which is a gap the file leaves open.
func writeRefusals(out io.Writer, g generated) {
	if len(g.Refusals) == 0 {
		return
	}

	fmt.Fprintf(out, "\n%d %s this migration does not close, because no operation expresses it:\n",
		len(g.Refusals), plural(len(g.Refusals), "difference", "differences"))

	for _, one := range g.Refusals {
		fmt.Fprintf(out, "  × %s\n", one)
	}

	fmt.Fprint(out, "\nSo this migration does not close the gap. `schema.Unrepresentable` lists "+
		"everything\nin this database the twenty operations cannot express, which is the whole "+
		"picture\nrather than this diff's part of it.\n")
}

// joinKinds renders the operations a generated migration carries, for a line a human reads.
func joinKinds(kinds []ir.Kind) string {
	names := make([]string, 0, len(kinds))
	for _, one := range kinds {
		names = append(names, string(one))
	}

	return strings.Join(names, ", ")
}

// renderers names what writes a generated migration in each language, which is every one but sql.
var renderers = map[config.Language]func(migration.Migration, ir.Operations) (string, error){
	config.LanguageGo:         golang.Render,
	config.LanguageTypeScript: typescript.Render,
}
