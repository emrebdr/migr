package runtime

import (
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// minimumFixtures is what M6-T9 asks for, which the set exceeds and must not fall below.
const minimumFixtures = 10

func TestEveryFixtureIsCanonicalIRAndIsNamedAsAMigration(t *testing.T) {
	fixtures, err := Fixtures()
	if err != nil {
		t.Fatalf("reading the fixture set: %v", err)
	}

	if len(fixtures) < minimumFixtures {
		t.Errorf("the set holds %d fixtures, and M6-T9 asks for at least %d",
			len(fixtures), minimumFixtures)
	}

	seen := make(map[string]bool, len(fixtures))

	for _, f := range fixtures {
		if seen[f.Version] {
			t.Errorf("%s: two fixtures claim one version, which history could not tell apart",
				f.Version)
		}

		seen[f.Version] = true

		if _, err := migration.Parse(f.Stem() + ".json"); err != nil {
			t.Errorf("%s is not named as a migration: %v", f.Stem(), err)
		}
	}

	t.Log("A fixture's bytes are what every SDK is compared against, so the loader holds the file")
	t.Log("itself to being exactly what ir.Marshal emits -- a fixture nothing could match is a")
	t.Log("fixture that fails every language.")
}

func TestTheFixtureSetCoversEveryOperation(t *testing.T) {
	fixtures, err := Fixtures()
	if err != nil {
		t.Fatalf("reading the fixture set: %v", err)
	}

	for _, problem := range checkCoverage(fixtures) {
		t.Error(problem)
	}

	t.Log("Coverage is derived from ir.Kinds() rather than from a count written down twice, so a")
	t.Log("twenty-first operation fails this suite on the day it is added.")
}

func TestEveryFixtureIsExpressedInEveryLanguageThisSetCarries(t *testing.T) {
	fixtures, err := Fixtures()
	if err != nil {
		t.Fatalf("reading the fixture set: %v", err)
	}

	for _, language := range languages(t) {
		sources, err := Sources(language)
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}

		assertExpressed(t, language, sources, fixtures)
	}
}

// assertExpressed reports a fixture one language never expresses, and a source no fixture defines.
func assertExpressed(t *testing.T, language string, sources fs.FS, fixtures []Fixture) {
	t.Helper()

	entries, err := fs.ReadDir(sources, ".")
	if err != nil {
		t.Fatalf("%s: %v", language, err)
	}

	stems := make(map[string]bool, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		stems[strings.TrimSuffix(name, path.Ext(name))] = true
	}

	for _, f := range fixtures {
		if !stems[f.Stem()] {
			t.Errorf("%s expresses no %s, so that language is not held to the fixture", language, f.Stem())
		}

		delete(stems, f.Stem())
	}

	for stem := range stems {
		t.Errorf("%s carries %s, which no fixture defines", language, stem)
	}
}

// languages lists every language this fixture set carries an expression for.
func languages(t *testing.T) []string {
	t.Helper()

	entries, err := fs.ReadDir(tree, sourceDir)
	if err != nil {
		t.Fatalf("reading the source tree: %v", err)
	}

	found := make([]string, 0, len(entries))
	for _, entry := range entries {
		found = append(found, entry.Name())
	}

	if len(found) == 0 {
		t.Fatal("no language expresses the fixtures, so passing this suite proves nothing")
	}

	return found
}

func TestSourcesRefusesALanguageThisSetDoesNotCarry(t *testing.T) {
	if _, err := Sources("cobol"); !errors.Is(err, ErrNoSources) {
		t.Errorf("Sources reported %v, want %v", err, ErrNoSources)
	}
}

func TestAFixtureStatesOneAnswerForItsDownDirection(t *testing.T) {
	for _, refused := range []struct {
		why  string
		body string
		says string
	}{
		{why: "it states both an undo and a refusal",
			body: `{"up":[{"op":"drop_table","table":"t"}],"down":[{"op":"raw","driver":"p","sql":"x"}],` +
				`"downRefusal":{"code":"no_down_authored"}}`,
			says: "cannot both be set"},
		{why: "it states neither", body: `{"up":[{"op":"drop_table","table":"t"}]}`,
			says: "says nothing about what undoes it"},
		{why: "the refusal names no answer §7.4 defines",
			body: `{"up":[{"op":"drop_table","table":"t"}],"downRefusal":{"code":"give_up"}}`,
			says: "names neither"},
		{why: "a declaration carries no reason",
			body: `{"up":[{"op":"drop_table","table":"t"}],"downRefusal":{"code":"declared_irreversible"}}`,
			says: "gives the reason"},
		{why: "a derivation carries one anyway",
			body: `{"up":[{"op":"drop_table","table":"t"}],"downRefusal":{"code":"no_down_authored",` +
				`"reason":"because"}}`,
			says: "carries a reason"},
		{why: "it emits nothing at all", body: `{"up":[],"downRefusal":{"code":"no_down_authored"}}`,
			says: "not a fixture"},
		{why: "it carries a member the format does not define",
			body: `{"up":[{"op":"drop_table","table":"t"}],"downRefusal":{"code":"no_down_authored"},` +
				`"notes":"hello"}`,
			says: "reading the fixture"},
	} {
		t.Run(refused.why, func(t *testing.T) {
			var m manifest
			if err := strictUnmarshal([]byte(refused.body), &m); err != nil {
				assertSays(t, err, refused.says)

				return
			}

			_, err := build(Fixture{}, m)
			if err == nil {
				t.Fatal("the loader accepted it")
			}

			assertSays(t, err, refused.says)
		})
	}
}

func TestAFixtureWhoseBytesAreNotTheBytesItDemandsIsRefused(t *testing.T) {
	var m manifest
	if err := strictUnmarshal(
		[]byte(`{"up":[{"table":"t","op":"drop_table"}],"downRefusal":{"code":"no_down_authored"}}`),
		&m); err != nil {
		t.Fatalf("reading: %v", err)
	}

	_, err := build(Fixture{}, m)
	if err == nil {
		t.Fatal("a fixture whose members are out of order was accepted")
	}

	assertSays(t, err, "not written the way the IR is emitted")

	t.Log("docs/ir.md §3.2: op comes first and member order is fixed, because the emitted bytes")
	t.Log("are hashed. A fixture written any other way could not be matched byte for byte.")
}

func TestASuiteWithNoRuntimeSaysSoRatherThanStartingNothing(t *testing.T) {
	problems := Suite{}.check(t.Context())

	if len(problems) != 1 || !strings.Contains(problems[0], "no runtime") {
		t.Errorf("an empty suite reported %v", problems)
	}
}

func TestCoverageReportsAnOperationNoFixtureEmits(t *testing.T) {
	problems := checkCoverage([]Fixture{{Up: ir.Operations{ir.Raw{Driver: "p", SQL: "x"}}}})

	if len(problems) != len(ir.Kinds())-1 {
		t.Errorf("a set emitting one operation left %d of %d uncovered",
			len(problems), len(ir.Kinds())-1)
	}

	if slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, "raw:") }) {
		t.Error("an operation a fixture emits was reported uncovered")
	}
}

// assertSays reports a refusal that does not carry the fragment an author needs to read.
func assertSays(t *testing.T, err error, says string) {
	t.Helper()

	if !strings.Contains(err.Error(), says) {
		t.Errorf("the refusal does not say %q: %v", says, err)
	}
}
