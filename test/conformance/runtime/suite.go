package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/runtime"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

// Suite is one language's runtime and one prepared project, held to what every SDK must emit.
type Suite struct {
	// Runtime is the adapter that starts the language under test.
	Runtime runtime.Runtime
	// Project is that language's expression of the fixture set, assembled and ready to run.
	Project runtime.Project

	// fixtures is the set to hold a language to, which only this package's own tests replace.
	fixtures func() ([]Fixture, error)
}

// load reads the fixture set, which is the shipped one for everyone outside this package.
func (s Suite) load() ([]Fixture, error) {
	if s.fixtures != nil {
		return s.fixtures()
	}

	return Fixtures()
}

// Run executes every fixture in both directions and both modes, and reports every disagreement.
func (s Suite) Run(t *testing.T) {
	t.Helper()

	for _, problem := range s.check(t.Context()) {
		t.Error(problem)
	}
}

// check drives one language through the whole fixture set and reports what it did not match.
func (s Suite) check(ctx context.Context) []string {
	fixtures, err := s.load()
	if err != nil {
		return []string{"the fixture set could not be read: " + err.Error()}
	}

	problems := checkCoverage(fixtures)

	if s.Runtime == nil {
		return append(problems,
			"the suite has no runtime, so there is nothing to hold to the fixtures")
	}

	session, conn, err := s.open(ctx)
	if err != nil {
		return append(problems, fmt.Sprintf("%s: the runtime never started: %v",
			s.Runtime.Name(), err))
	}

	defer func() { _ = session.Close(context.WithoutCancel(ctx)) }()

	files, found := s.reconcile(ctx, session, fixtures)
	problems = append(problems, found...)

	for _, f := range fixtures {
		file, discovered := files[f.Version]
		if !discovered {
			continue
		}

		before := conn.asked()
		problems = append(problems, s.walk(ctx, session, conn, f, file)...)
		problems = append(problems, checkAsked(f, conn.asked()-before)...)
	}

	return problems
}

// checkAsked reports a fixture that was not put every question this suite says it puts.
func checkAsked(f Fixture, asked int) []string {
	want := 2 * len(engine.Modes())
	if asked == want {
		return nil
	}

	return []string{fmt.Sprintf("%s: it was asked %d questions, and every fixture is asked %d -- "+
		"both directions, in both modes", f.Stem(), asked, want)}
}

// open starts a session over the database every fixture reaches, which stages its own answers.
func (s Suite) open(ctx context.Context) (*runtime.Session, *conn, error) {
	c, err := newConn(s.Project.Driver)
	if err != nil {
		return nil, nil, err
	}

	session, err := runtime.Start(ctx, runtime.Options{
		Runtime: s.Runtime, Project: s.Project, Conn: c,
		Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		return nil, nil, err
	}

	return session, c, nil
}

// reconcile holds what the runtime loaded to the fixture set, and reports each side's surplus.
func (s Suite) reconcile(ctx context.Context, session *runtime.Session,
	fixtures []Fixture) (map[string]migration.Migration, []string) {
	loaded, err := session.Discovered(ctx)
	if err != nil {
		return nil, []string{"the runtime could not say what it loaded: " + err.Error()}
	}

	files, problems := parseDiscovered(loaded)
	problems = append(problems, matchDiscovered(files, fixtures)...)

	problems = append(problems, matchDeclared(loaded, fixtures)...)

	problems = append(problems, matchPhases(loaded, fixtures)...)

	return files, append(problems, matchOnline(loaded, fixtures)...)
}

// matchOnline holds how each migration declared it runs to what its fixture defines.
func matchOnline(loaded []protocol.Discovered, fixtures []Fixture) []string {
	declared := make(map[string]string, len(loaded))
	for _, d := range loaded {
		declared[d.Version] = d.Online
	}

	var problems []string

	for _, f := range fixtures {
		got, reported := declared[f.Version]
		if !reported || got == f.Online {
			continue
		}

		problems = append(problems, fmt.Sprintf("%s: this runtime declared it runs online as %q and the "+
			"fixture declares %q -- the strategy is read from the migration, so every language reports "+
			"the same one (ADR 0089)", f.Stem(), got, f.Online))
	}

	return problems
}

// matchPhases holds the step each migration declared itself to what its fixture defines.
func matchPhases(loaded []protocol.Discovered, fixtures []Fixture) []string {
	declared := make(map[string]*protocol.Phase, len(loaded))
	for _, d := range loaded {
		declared[d.Version] = d.Phase
	}

	var problems []string

	for _, f := range fixtures {
		got, reported := declared[f.Version]
		if !reported || reflect.DeepEqual(got, f.Phase) {
			continue
		}

		problems = append(problems, fmt.Sprintf("%s: this runtime declared the phase %+v and the "+
			"fixture declares %+v -- a step is read from the migration, so every language reports "+
			"the same one (ADR 0087)", f.Stem(), got, f.Phase))
	}

	return problems
}

// matchDeclared holds what each migration declared to what its fixture defines, list for list.
func matchDeclared(loaded []protocol.Discovered, fixtures []Fixture) []string {
	declared := make(map[string][]string, len(loaded))
	for _, d := range loaded {
		declared[d.Version] = d.Dependencies
	}

	var problems []string

	for _, f := range fixtures {
		deps, reported := declared[f.Version]
		if !reported || slices.Equal(deps, f.Dependencies) {
			continue
		}

		problems = append(problems, fmt.Sprintf("%s: this runtime declared %v and the fixture "+
			"declares %v -- a declaration is read from the migration, so every language reports "+
			"the same list (ADR 0018)", f.Stem(), deps, f.Dependencies))
	}

	return problems
}

// matchDiscovered holds what a runtime loaded to the fixture set, reporting each side's surplus.
func matchDiscovered(files map[string]migration.Migration, fixtures []Fixture) []string {
	var problems []string

	for _, f := range fixtures {
		file, discovered := files[f.Version]
		if !discovered {
			problems = append(problems, fmt.Sprintf("%s: this runtime loaded no migration for the "+
				"fixture, so nothing holds it to emitting anything", f.Stem()))

			continue
		}

		if stem := file.Version + "_" + file.Name; stem != f.Stem() {
			problems = append(problems, fmt.Sprintf("%s: this runtime loaded it as %q, and one "+
				"fixture carries one name across every language", f.Stem(), file.Filename()))
		}
	}

	return append(problems, describeSurplus(files, fixtures)...)
}

// parseDiscovered reads each loaded migration's filename, which the grammar must already accept.
func parseDiscovered(loaded []protocol.Discovered) (map[string]migration.Migration, []string) {
	files := make(map[string]migration.Migration, len(loaded))

	var problems []string

	for _, d := range loaded {
		parsed, err := migration.Parse(d.File)
		if err != nil {
			problems = append(problems, fmt.Sprintf("this runtime loaded %q, which discovery "+
				"would refuse: %v", d.File, err))

			continue
		}

		files[d.Version] = parsed
	}

	return files, problems
}

// describeSurplus reports a migration the runtime loaded that the fixture set does not define.
func describeSurplus(files map[string]migration.Migration, fixtures []Fixture) []string {
	defined := make(map[string]bool, len(fixtures))
	for _, f := range fixtures {
		defined[f.Version] = true
	}

	var problems []string

	for version, file := range files {
		if !defined[version] {
			problems = append(problems, fmt.Sprintf("this runtime loaded %s, which no fixture "+
				"defines, so nothing states what it must emit", file.Filename()))
		}
	}

	slices.Sort(problems)

	return problems
}

// walk asks one fixture for both directions in both modes, which is four questions with one answer.
func (s Suite) walk(ctx context.Context, session *runtime.Session, c *conn,
	f Fixture, file migration.Migration) []string {
	var problems []string

	for _, mode := range engine.Modes() {
		problems = append(problems, s.emit(ctx, session, c, f, file, ir.Up, mode)...)
		problems = append(problems, s.emit(ctx, session, c, f, file, ir.Down, mode)...)
	}

	return problems
}

// emit asks one migration one question and holds every part of its answer to the fixture.
func (s Suite) emit(ctx context.Context, session *runtime.Session, c *conn,
	f Fixture, file migration.Migration, dir ir.Direction, mode engine.Mode) []string {
	label := fmt.Sprintf("%s %s/%s", f.Stem(), dir, mode)

	c.stage(staged(f, dir))

	emitted, err := session.Emit(ctx, file, dir, mode)
	settled := c.settled(label, dir == ir.Up && mode == engine.ModeApply)

	return judge(label, f, dir, mode, emitted, err, settled)
}

// judge holds one answer to everything the fixture states about it, and reaches no process.
func judge(label string, f Fixture, dir ir.Direction, mode engine.Mode,
	emitted engine.Emitted, err error, settled []string) []string {
	if err != nil {
		return checkRefused(label, f, dir, err)
	}

	if refusal := expectedRefusal(f, dir); refusal != nil {
		return []string{fmt.Sprintf("%s: it emitted %d operations, and this fixture states it "+
			"answers %s", label, len(emitted.Operations), refusal.Code)}
	}

	problems := checkOperations(label, f, dir, emitted.Operations)
	problems = append(problems, checkDirect(label, f, dir, mode, emitted.Direct)...)

	return append(problems, settled...)
}

// staged returns what the session answers for one direction, since a down states none of its own.
func staged(f Fixture, dir ir.Direction) ([]Read, []Write) {
	if dir == ir.Down {
		return nil, nil
	}

	return f.Reads, f.Writes
}

// checkOperations holds what one direction emitted to the bytes the fixture states, exactly.
func checkOperations(label string, f Fixture, dir ir.Direction, ops ir.Operations) []string {
	got, err := ir.Marshal(ops)
	if err != nil {
		return []string{fmt.Sprintf("%s: re-encoding what it emitted: %v", label, err)}
	}

	want := f.Expected(dir)
	if string(got) == string(want) {
		return nil
	}

	return []string{fmt.Sprintf("%s: the operations it emitted are not the ones this fixture "+
		"states\n got %s\nwant %s", label, got, want)}
}

// checkDirect holds the escape hatch to §6: a preview records the statements a run performs.
func checkDirect(label string, f Fixture, dir ir.Direction, mode engine.Mode,
	held []driver.Query) []string {
	want := heldBy(f, dir, mode)

	if len(held) != len(want) {
		return []string{fmt.Sprintf("%s: it held %d statements and this fixture states %d for "+
			"this mode; mode plan records host.db.exec and mode apply runs it", label,
			len(held), len(want))}
	}

	var problems []string

	for i, q := range held {
		if q.SQL != want[i].SQL || !slices.Equal(q.Args, bound(want[i].Args)) {
			problems = append(problems, fmt.Sprintf("%s: held statement %d is %q %v, and this "+
				"fixture states %q %v", label, i+1, q.SQL, q.Args, want[i].SQL,
				bound(want[i].Args)))
		}
	}

	return problems
}

// heldBy reports which statements a preview holds, which is none of them in any other case.
func heldBy(f Fixture, dir ir.Direction, mode engine.Mode) []Write {
	if dir == ir.Up && mode == engine.ModePlan {
		return f.Writes
	}

	return nil
}

// checkRefused tells §7.4's two answers from the failure a fixture never said it would produce.
func checkRefused(label string, f Fixture, dir ir.Direction, err error) []string {
	refusal := expectedRefusal(f, dir)
	if refusal == nil {
		return []string{fmt.Sprintf("%s: it failed, and this fixture states what it emits: %v",
			label, err)}
	}

	if refusal.Code == NoDownAuthored {
		return checkSentinel(label, engine.ErrNoDownAuthored, err)
	}

	return checkDeclaration(label, refusal.Reason, err)
}

// expectedRefusal reports the answer a fixture states for one direction, and nil for an emitted one.
func expectedRefusal(f Fixture, dir ir.Direction) *Refusal {
	if dir == ir.Down {
		return f.Refusal
	}

	return nil
}

// checkSentinel reports a refusal that did not come back as the one the engine branches on.
func checkSentinel(label string, want, got error) []string {
	if errors.Is(got, want) {
		return nil
	}

	return []string{fmt.Sprintf("%s: it refused with %v, and this fixture states %v", label, got, want)}
}

// checkDeclaration reports a declaration that lost the author's own reason on the way back.
func checkDeclaration(label, reason string, err error) []string {
	problems := checkSentinel(label, engine.ErrDeclaredIrreversible, err)

	var declared *engine.DeclaredIrreversibleError
	if !errors.As(err, &declared) {
		return problems
	}

	if declared.Reason != reason {
		problems = append(problems, fmt.Sprintf("%s: it halts with %q, and this fixture states "+
			"the author wrote %q", label, declared.Reason, reason))
	}

	return problems
}

// checkCoverage reports an operation no fixture emits, which no SDK is therefore held to.
func checkCoverage(fixtures []Fixture) []string {
	covered := make(map[ir.Kind]bool, len(ir.Kinds()))

	for _, f := range fixtures {
		for _, dir := range []ir.Direction{ir.Up, ir.Down} {
			for _, op := range f.Operations(dir) {
				covered[op.Kind()] = true
			}
		}
	}

	var problems []string

	for _, kind := range ir.Kinds() {
		if !covered[kind] {
			problems = append(problems, fmt.Sprintf("%s: no fixture emits this operation, so no "+
				"SDK is held to producing it", kind))
		}
	}

	return problems
}
