package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
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

// Introspect refuses: there is no server behind this session.
func (declaredConn) Introspect(context.Context) (*schema.Snapshot, error) {
	return nil, errNoServer
}

// Query refuses: there is no server behind this session.
func (declaredConn) Query(context.Context, driver.Query) (driver.Rows, error) {
	return driver.Rows{}, errNoServer
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

// Seeds refuses every call, as History does.
func (declaredConn) Seeds() history.Store { return noServerHistory{} }

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

// ServerVersion reports the version the declaration was resolved against.
func (c declaredConn) ServerVersion() string { return c.caps.Server() }

// Close refuses: there is no server behind this session.
func (declaredConn) Close(context.Context) error { return errNoServer }

func TestTheConformanceSuitePassesWithoutAServer(t *testing.T) {
	set, err := declaredSet(t)
	if err != nil {
		t.Fatalf("the declaration is not a complete capability set: %v", err)
	}

	conformance.Suite{
		Driver: newDriver(t, driver.Options{}),
		Conn:   declaredConn{caps: set},
	}.Run(t)

	t.Log("rules() needs no connection, so the biconditional the suite exists to prove -- the")
	t.Log("declaration refuses exactly what Compile refuses -- is a make test and CI gate rather")
	t.Log("than one only Docker reaches. The integration run stays: it proves the declaration a")
	t.Log("live server produces, which is a different claim about a different set.")
}

// compile-time proof that the double satisfies the same contract a real session does.
var _ driver.Conn = declaredConn{}
