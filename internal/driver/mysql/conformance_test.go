package mysql

import (
	"context"
	"errors"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
	conformance "github.com/emrebdr/migr/test/conformance/driver"
)

// errNoServer refuses everything on a session the conformance suite never reaches for.
var errNoServer = errors.New("this session carries a capability declaration and no server")

// declaredConn is a session carrying the declaration Open builds, and nothing else.
type declaredConn struct {
	caps capability.Set
}

// Exec refuses: there is no server behind this session.
func (declaredConn) Exec(context.Context, driver.Statement) error { return errNoServer }

// Query refuses: there is no server behind this session.
func (declaredConn) Query(context.Context, driver.Query) (driver.Rows, error) {
	return driver.Rows{}, errNoServer
}

// Introspect refuses: there is no server behind this session.
func (declaredConn) Introspect(context.Context) (*schema.Snapshot, error) {
	return nil, errNoServer
}

// Expand refuses, and exists because the suite requires an Expander wherever Compile asks for a session.
func (declaredConn) Expand(context.Context, ir.Operation) ([]driver.Statement, error) {
	return nil, errNoServer
}

// Perform refuses: there is no server behind this session.
func (declaredConn) Perform(context.Context, driver.Query) (int64, error) { return 0, errNoServer }

// Lock refuses: there is no server behind this session.
func (declaredConn) Lock(context.Context) (driver.Unlock, error) { return nil, errNoServer }

// Transact refuses: there is no server behind this session.
func (declaredConn) Transact(context.Context, driver.Work) error { return errNoServer }

// Capabilities reports the declaration, which is the only thing the conformance suite reads.
func (c declaredConn) Capabilities() capability.Set { return c.caps }

// History refuses: there is no server behind this session for a store to read or write.
func (declaredConn) History() history.Store { return noServerHistory{} }

// ServerVersion reports the version the declaration was resolved against.
func (c declaredConn) ServerVersion() string { return c.caps.Server() }

// Close refuses: there is no server behind this session.
func (declaredConn) Close(context.Context) error { return errNoServer }

// noServerHistory refuses every call, rather than a nil no caller could have checked.
type noServerHistory struct{}

// Ensure refuses: there is no server behind this session.
func (noServerHistory) Ensure(context.Context) error { return errNoServer }

// Record refuses: there is no server behind this session.
func (noServerHistory) Record(context.Context, history.Record) error { return errNoServer }

// List refuses: there is no server behind this session.
func (noServerHistory) List(context.Context) ([]history.Record, error) { return nil, errNoServer }

// Latest refuses: there is no server behind this session.
func (noServerHistory) Latest(context.Context) (history.Record, bool, error) {
	return history.Record{}, false, errNoServer
}

// MarkFailed refuses: there is no server behind this session.
func (noServerHistory) MarkFailed(context.Context, history.Record, error) error { return errNoServer }

// MarkDirty refuses: there is no server behind this session.
func (noServerHistory) MarkDirty(context.Context, history.Record, int32, error) error {
	return errNoServer
}

// MarkResolved refuses: there is no server behind this session.
func (noServerHistory) MarkResolved(context.Context, history.Record, string) error {
	return errNoServer
}

// declaredSet builds the capability set Open would build for one server, with no server present.
func declaredSet(t *testing.T, f serverFacts) capability.Set {
	t.Helper()

	set, err := capability.NewSet(f.facts(), f.rules())
	if err != nil {
		t.Fatalf("the declaration is not a complete capability set: %v", err)
	}

	return set
}

func TestTheConformanceSuitePassesWithoutAServer(t *testing.T) {
	for _, declaration := range []serverFacts{
		{version: serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46}},
		{version: serverVersion{variant: MySQL, major: 8, minor: 4}},
		{version: serverVersion{variant: MariaDB, major: 11, minor: 4, patch: 13}},
		{version: serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46}, logBin: true},
		{version: serverVersion{variant: MySQL, major: 8, minor: 0}},
		{version: serverVersion{variant: MySQL, major: 8, minor: 0, patch: 15}},
		{version: serverVersion{variant: MySQL, major: 8, minor: 0, patch: 18}},
		{version: serverVersion{variant: MariaDB, major: 10, minor: 5}},
	} {
		t.Run(describeDeclaration(declaration), func(t *testing.T) {
			d, err := New(driver.Options{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			conformance.Suite{Driver: d, Conn: declaredConn{caps: declaredSet(t, declaration)}}.Run(t)
		})
	}

	t.Log("rules() needs no connection, so the biconditional the suite exists to prove -- the")
	t.Log("declaration refuses exactly what Compile refuses -- is a make test and CI gate rather")
	t.Log("than one only Docker reaches. Three versions, because a verdict keyed to a release is")
	t.Log("what invariant #13 asks for and one version cannot show it holding.")
	t.Log("")
	t.Log("The fourth case is the same server with its binary log on, which is the one fact besides")
	t.Log("the release a verdict is keyed to. It refuses an add_column carrying a uuid default.")
	t.Log("")
	t.Log("The last four are below this driver's version gates, and until M10-T2 they could not be")
	t.Log("here at all: the suite called every version-dependent refusal a disagreement, so these")
	t.Log("four servers produced five failures between them -- add_check and drop_check on 8.0.0,")
	t.Log("drop_check on 8.0.15 and 8.0.18, rename_column on MariaDB 10.5.0. A declaration may")
	t.Log("refuse more than Compile does, because it is resolved against a server Compile cannot")
	t.Log("see; what it may never do is promise what Compile refuses (ADR 0064).")
}

// describeDeclaration names one declaration, since two cases may differ by configuration alone.
func describeDeclaration(f serverFacts) string {
	if f.logBin {
		return f.version.String() + " with a binary log"
	}

	return f.version.String()
}

// compile-time proof that the double satisfies the same contract a real session does.
var _ driver.Conn = declaredConn{}
