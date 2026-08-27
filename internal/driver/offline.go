package driver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// ErrNoDatabase marks a database call made by a check that deliberately opened none.
var ErrNoDatabase = errors.New("no database was opened")

// Offline returns a session that refuses every database call, for a check that opens none.
func Offline(driverName string) (*OfflineConn, error) {
	caps, err := capability.NewSet(capability.Facts{Driver: driverName}, offlineRules(driverName))
	if err != nil {
		return nil, fmt.Errorf("preparing an offline session for driver %q: %w", driverName, err)
	}

	return &OfflineConn{caps: caps}, nil
}

// offlineRules refuses every operation, since a capability set is resolved against a live server.
func offlineRules(driverName string) map[ir.Kind]capability.Rule {
	reason := fmt.Sprintf("driver %q resolves its capabilities against the connected server, and %s",
		driverName, ErrNoDatabase)

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Unsupported(reason))
	}

	return rules
}

// OfflineConn is a session with nothing behind it: everything needing a server refuses by name.
type OfflineConn struct {
	// caps refuses every operation, naming the driver migr.yaml chose and why it cannot answer.
	caps capability.Set

	// mu guards what this session remembers, since callbacks arrive on the session's read loop.
	mu sync.Mutex
	// reached records that something needing a server asked, whatever it made of the refusal.
	reached bool
}

// compile-time proof that a session with no database is still one the host callbacks can reach.
var _ Conn = (*OfflineConn)(nil)

// Reached reports whether anything needing a server has asked since this session last forgot.
func (o *OfflineConn) Reached() bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.reached
}

// Forget clears what this session remembers, so the next thing asked of it stands on its own.
func (o *OfflineConn) Forget() {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.reached = false
}

// refuse records that a server was needed and reports why there is none.
func (o *OfflineConn) refuse(what string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.reached = true

	return fmt.Errorf("%s: %w", what, ErrNoDatabase)
}

// Exec refuses, since a compiled statement has no server to run against.
func (o *OfflineConn) Exec(context.Context, Statement) error {
	return o.refuse("executing a statement")
}

// Query refuses, which leaves a migration's own read unanswered rather than answered wrongly.
func (o *OfflineConn) Query(context.Context, Query) (Rows, error) {
	return Rows{}, o.refuse("reading from the database")
}

// Perform refuses, since a statement a migration sends itself reaches the database directly.
func (o *OfflineConn) Perform(context.Context, Query) (int64, error) {
	return 0, o.refuse("running a statement")
}

// Lock refuses, since there is no session for a migration lock to belong to.
func (o *OfflineConn) Lock(context.Context) (Unlock, error) {
	return nil, o.refuse("taking the migration lock")
}

// Transact refuses, since a transaction is bracketed by a session there is none of.
func (o *OfflineConn) Transact(context.Context, Work) error {
	return o.refuse("opening a transaction")
}

// Capabilities reports a set refusing every operation, and is not a server being reached.
func (o *OfflineConn) Capabilities() capability.Set {
	return o.caps
}

// History returns no store, since a check that opens no database reads no history.
func (o *OfflineConn) History() history.Store {
	return nil
}

// ServerVersion reports nothing, since no server was reached to detect a version from.
func (o *OfflineConn) ServerVersion() string {
	return ""
}

// Close releases nothing, since an offline session holds nothing.
func (o *OfflineConn) Close(context.Context) error {
	return nil
}
