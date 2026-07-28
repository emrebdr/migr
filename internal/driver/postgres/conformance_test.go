package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
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

// Lock refuses: there is no server behind this session.
func (declaredConn) Lock(context.Context) (driver.Unlock, error) { return nil, errNoServer }

// Capabilities reports the declaration, which is the only thing the conformance suite reads.
func (c declaredConn) Capabilities() capability.Set { return c.caps }

// History returns the store this driver hands back until M3-T6 writes one.
func (declaredConn) History() history.Store { return pendingHistory{} }

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
