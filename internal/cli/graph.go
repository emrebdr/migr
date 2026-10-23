package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/remedy"
	"github.com/emrebdr/migr/internal/validate"
	"github.com/emrebdr/migr/internal/visual"
)

// drawing is one of the three things migr graph renders to.
type drawing string

// The renderings migr graph knows, in the order its flag names them.
const (
	// drawingText is lines for a terminal, and the default.
	drawingText drawing = "text"
	// drawingMermaid is a Mermaid diagram, which GitHub and most markdown sites draw.
	drawingMermaid drawing = "mermaid"
	// drawingDOT is a Graphviz digraph.
	drawingDOT drawing = "dot"
)

// drawings lists every rendering, in the order a refusal names them.
var drawings = []drawing{drawingText, drawingMermaid, drawingDOT}

// errBothRenderings refuses --json beside --format, since one of the two would be ignored.
var errBothRenderings = errors.New("--json and --format each choose what is written, so a graph takes one")

// graphing is what migr graph was asked to draw.
type graphing struct {
	// schema draws the schema each migration builds, rather than the order they run in.
	schema bool
	// to stops the schema at this version, empty for every migration.
	to string
	// format is the rendering, as the flag spelled it.
	format string
}

// newGraphCmd builds the `migr graph` subcommand.
func newGraphCmd(opts *options) *cobra.Command {
	g := &graphing{}

	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Draw the migrations' dependency DAG, or the schema they build one migration at a time",
		Long: "Draws every migration in run order with the dependencies and the deploy steps they\n" +
			"declare. With --schema, draws what each migration changes in the schema instead, folded\n" +
			"forward from nothing, and --format mermaid or dot draws the schema the last of them\n" +
			"leaves, or the one at --to, as an entity-relationship diagram. It opens no database and\n" +
			"takes no lock: everything is read from the migrations, as migr validate reads them.",
		Args: argsOf(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGraph(cmd, opts, *g)
		},
	}

	cmd.Flags().BoolVar(&g.schema, "schema", false, "draw the schema each migration builds")
	cmd.Flags().StringVar(&g.to, "to", "", "with --schema, stop at this version")
	cmd.Flags().StringVar(&g.format, "format", string(drawingText), "text, mermaid or dot")

	return cmd
}

// drawing checks what migr graph was asked for before anything is read, refusing a combination it cannot draw.
func (g graphing) drawing(cmd *cobra.Command, opts *options) (drawing, error) {
	if opts.jsonOut && cmd.Flags().Changed("format") {
		return "", NewExitError(ExitValidation, remedy.With(errBothRenderings, "pass --json or --format, not both"))
	}

	if g.to != "" && !g.schema {
		return "", NewExitError(ExitValidation, remedy.With(
			errors.New("--to stops the schema at a version, and without --schema there is no schema drawn"),
			"add --schema, or drop --to: the dependency DAG always draws every migration"))
	}

	chosen := drawing(g.format)
	if !slices.Contains(drawings, chosen) {
		return "", NewExitError(ExitValidation, remedy.With(fmt.Errorf("--format %q is not a rendering migr "+
			"graph knows", g.format), "pass --format text, mermaid or dot"))
	}

	return chosen, nil
}

// runGraph draws this project's migrations, reading them over sessions that open no database.
func runGraph(cmd *cobra.Command, opts *options, g graphing) (err error) {
	chosen, err := g.drawing(cmd, opts)
	if err != nil {
		return err
	}

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

	gates, err := gatesOf(ordered, read.phases)
	if err != nil {
		return err
	}

	if !g.schema {
		dag := visual.DAG(ordered, migration.Declarations{Dependencies: read.declared, Phases: read.phases,
			Online: read.online}, gates)

		return drawGraph(cmd.OutOrStdout(), opts, chosen, dag)
	}

	through, err := upTo(ordered, g.to)
	if err != nil {
		return err
	}

	reads, err := readUps(cmd.Context(), read.source, through)
	if err != nil {
		return err
	}

	return drawEvolution(cmd.OutOrStdout(), opts, chosen, visual.Evolve(reads))
}

// upTo returns the migrations up to and including the one --to names, all of them where it names none.
func upTo(ordered []migration.Migration, to string) ([]migration.Migration, error) {
	if to == "" {
		return ordered, nil
	}

	at := slices.IndexFunc(ordered, func(m migration.Migration) bool { return m.Version == to })
	if at < 0 {
		versions := make([]string, 0, len(ordered))
		for _, m := range ordered {
			versions = append(versions, m.Version)
		}

		return nil, NewExitError(ExitValidation, remedy.With(fmt.Errorf("--to %q names no migration in this "+
			"project, which holds %s", to, describeVersionList(versions)),
			"pass --to one of the versions above, as migr status prints them"))
	}

	return ordered[:at+1], nil
}

// describeVersionList names the versions a project holds, which a refusal of --to lists.
func describeVersionList(versions []string) string {
	if len(versions) == 0 {
		return "no migrations at all"
	}

	return strings.Join(versions, ", ")
}

// readUps asks each migration for its up half as a question, noting one that reaches the database.
func readUps(ctx context.Context, source engine.Source, ordered []migration.Migration) ([]visual.Read, error) {
	reads := make([]visual.Read, 0, len(ordered))

	for _, m := range ordered {
		emitted, err := source.Emit(ctx, m, ir.Up, engine.ModePlan)

		switch {
		case errors.Is(err, validate.ErrNotCheckable):
			reads = append(reads, visual.Read{Migration: m,
				Unread: "it reaches the database, and migr graph opens none"})
		case err != nil:
			return nil, fmt.Errorf("%s: asking what it does: %w", m.Filename(), err)
		default:
			reads = append(reads, visual.Read{Migration: m, Operations: emitted.Operations,
				Direct: len(emitted.Direct)})
		}
	}

	return reads, nil
}

// drawGraph writes the dependency DAG in the rendering asked for.
func drawGraph(w io.Writer, opts *options, chosen drawing, dag visual.Graph) error {
	if opts.jsonOut {
		return writeJSON(w, dag)
	}

	switch chosen {
	case drawingMermaid:
		return writeText(w, dag.Mermaid())
	case drawingDOT:
		return writeText(w, dag.DOT())
	default:
		return writeText(w, dag.Text())
	}
}

// drawEvolution writes the schema's evolution, or the schema it leaves as a diagram.
func drawEvolution(w io.Writer, opts *options, chosen drawing, e visual.Evolution) error {
	if opts.jsonOut {
		return writeJSON(w, e)
	}

	if chosen == drawingText {
		return writeText(w, e.Text())
	}

	if !e.Known {
		return unknowableError(e)
	}

	if chosen == drawingMermaid {
		return writeText(w, visual.MermaidER(e.Schema))
	}

	return writeText(w, visual.DOTER(e.Schema))
}

// unknowableError refuses a diagram of a schema the migrations cannot say, naming the one that ended it.
func unknowableError(e visual.Evolution) error {
	last := e.Steps[len(e.Steps)-1].Filename

	next := fmt.Sprintf("pass --to a version before %s, or run migr graph --schema to see each "+
		"migration's operations as text", e.EndedBy.Filename)
	if e.EndedBy.Filename == e.Steps[0].Filename {
		next = "run migr graph --schema to see each migration's operations as text: the first migration " +
			"ends the projection, so no moment of this schema can be drawn"
	}

	return remedy.With(fmt.Errorf("the schema after %s cannot be read from the migrations, so there is no "+
		"schema to draw -- %s: %s", last, e.EndedBy.Filename, e.EndedBy.Ended), next)
}

// writeText writes a rendering as it is.
func writeText(w io.Writer, text string) error {
	if _, err := io.WriteString(w, text); err != nil {
		return fmt.Errorf("writing result: %w", err)
	}

	return nil
}
