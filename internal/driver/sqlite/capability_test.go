package sqlite

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// at returns the library version a declaration is being asked about, with no database involved.
func at(major, minor, patch int) serverVersion {
	return serverVersion{major: major, minor: minor, patch: patch}
}

// linked returns the version of the library this binary carries, which is what a session reports.
func linked(t *testing.T) serverVersion {
	t.Helper()

	return openMemory(t).version
}

// setAt builds a capability set for one library version, which needs no connection at all.
func setAt(t *testing.T, v serverVersion) capability.Set {
	t.Helper()

	set, err := capability.NewSet(facts(v), rules(v))
	if err != nil {
		t.Fatalf("NewSet(%s): %v", v, err)
	}

	return set
}

func TestDropColumnIsDeclaredAgainstTheLibraryRatherThanAssumed(t *testing.T) {
	op := ir.DropColumn{Table: "users", Column: "nickname"}

	if verdict := setAt(t, at(3, 35, 0)).Resolve(op); !verdict.Allowed() {
		t.Errorf("3.35 refuses drop_column: %s", verdict.Reason())
	}

	refused := setAt(t, at(3, 34, 9)).Resolve(op)
	if refused.Allowed() {
		t.Fatal("3.34 allows drop_column, which SQLite has no statement for")
	}

	for _, fragment := range []string{"3.35", "3.34.9", rebuildStrategy} {
		if !strings.Contains(refused.Reason(), fragment) {
			t.Errorf("the refusal does not name %q: %s", fragment, refused.Reason())
		}
	}

	t.Log("The compiler emits the same statement either way -- it is pure by contract and has no")
	t.Log("session to ask -- so the declaration is the only place a version-dependent answer can")
	t.Log("live. That is the declaration's known asymmetry, and PostgreSQL's uuid default is the same")
	t.Log("shape in the same direction.")
}

func TestTheVersionIsAskedForRatherThanHardcoded(t *testing.T) {
	c := openMemory(t)

	if !c.version.AtLeast(dropColumnMajor, dropColumnMinor) {
		t.Fatalf("the linked library is %s", c.version)
	}

	if verdict := c.Capabilities().Resolve(ir.DropColumn{
		Table: "users", Column: "nickname",
	}); !verdict.Allowed() {
		t.Errorf("a live session refuses drop_column: %s", verdict.Reason())
	}

	if got := c.Capabilities().Server(); got != c.version.String() {
		t.Errorf("the set reports server %q and the session reports %q", got, c.version)
	}

	t.Log("For this driver the version is a fact about the build rather than about the database:")
	t.Log("modernc.org/sqlite links the library in, so AtLeast(3, 35) is true for every session")
	t.Log("this binary can open and the refusal above is unreachable until the dependency is")
	t.Log("pinned older. Invariant #13's `asked, never hardcoded` is what is being kept.")
}

func TestTheDeclarationAndTheCompilerAgreeAtEveryVersion(t *testing.T) {
	for _, version := range []serverVersion{at(3, 25, 0), at(3, 34, 9), at(3, 35, 0), at(3, 53, 4)} {
		t.Run(version.String(), func(t *testing.T) {
			set := setAt(t, version)

			for _, kind := range ir.Kinds() {
				op := representative(t, kind)
				_, err := compile(op)

				if err != nil && !errors.Is(err, driver.ErrUnsupported) &&
					!errors.Is(err, driver.ErrNeedsSession) {
					t.Errorf("%s: the compiler failed rather than answering: %v", kind, err)
				}

				want := err == nil || errors.Is(err, driver.ErrNeedsSession)
				if kind == ir.KindDropColumn {
					want = version.AtLeast(dropColumnMajor, dropColumnMinor)
				}

				if got := set.Resolve(op).Allowed(); got != want {
					t.Errorf("%s at %s: the declaration allows it (%t), want %t; the compiler said %v",
						kind, version, got, want, err)
				}
			}
		})
	}

	t.Log("drop_column below 3.35 is the one place the biconditional the conformance suite proves")
	t.Log("can break -- the compiler emits a statement the declaration refuses. It is unreachable")
	t.Log("through Open, because interpretVersion floors the library at 3.25 and modernc links")
	t.Log("3.53, so the suite runs over a live session and never meets it.")
}
