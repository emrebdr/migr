package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// serverVersion is what the fake session reports, since host.info carries it to a migration.
const serverVersion = "0.0.0-conformance"

// conn is the database every fixture reaches: it answers exactly what the manifest staged.
type conn struct {
	caps capability.Set

	// mu guards everything a migration's callbacks reach, since the session serves them one at a time.
	mu sync.Mutex
	// reads are what this fixture stated it queries, answered in the order it states them.
	reads []Read
	// writes are what this fixture stated it sends itself, in the order it states them.
	writes []Write
	// queried counts the reads a migration has made, which names the next answer.
	queried int
	// performed counts the statements a migration has sent, which names the next result.
	performed int
	// problems are the disagreements this session saw, which the suite reports as its own.
	problems []string
	// questions counts every time this session was armed, which is once per question asked.
	questions int
}

// asked reports how many questions have been put to a runtime through this session.
func (c *conn) asked() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.questions
}

// newConn builds the database the fixtures reach, declaring every operation native to one driver.
func newConn(name string) (*conn, error) {
	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Native(capability.LockUnknown))
	}

	caps, err := capability.NewSet(
		capability.Facts{Driver: name, Server: serverVersion}, rules)
	if err != nil {
		return nil, fmt.Errorf("building the conformance capability set: %w", err)
	}

	return &conn{caps: caps}, nil
}

// stage arms the session for one question and forgets whatever the previous one did.
func (c *conn) stage(reads []Read, writes []Write) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.reads, c.writes = reads, writes
	c.queried, c.performed = 0, 0
	c.problems = nil
	c.questions++
}

// settled reports what this session saw, plus any staged call the migration never made.
func (c *conn) settled(label string, expectPerforms bool) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	problems := slices.Clone(c.problems)

	if c.queried != len(c.reads) {
		problems = append(problems, fmt.Sprintf("%s: the migration made %d of the %d reads this "+
			"fixture states", label, c.queried, len(c.reads)))
	}

	return append(problems, c.checkPerformed(label, expectPerforms)...)
}

// checkPerformed reports statements a run never sent, or a preview sent when it must hold them.
func (c *conn) checkPerformed(label string, expectPerforms bool) []string {
	want := len(c.writes)
	if !expectPerforms {
		want = 0
	}

	if c.performed == want {
		return nil
	}

	if !expectPerforms {
		return []string{fmt.Sprintf("%s: a preview reached the database %d times, and mode plan "+
			"records host.db.exec rather than running it", label, c.performed)}
	}

	return []string{fmt.Sprintf("%s: the migration sent %d of the %d statements this fixture "+
		"states", label, c.performed, want)}
}

// Introspect is never reached: a fixture states its reads, and a snapshot is not one of them.
func (c *conn) Introspect(context.Context) (*schema.Snapshot, error) {
	return nil, errors.New("this fixture answers no introspection")
}

// Query answers the next read this fixture staged, reporting one it never said it would make.
func (c *conn) Query(_ context.Context, q driver.Query) (driver.Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	at := c.queried
	c.queried++

	if at >= len(c.reads) {
		c.problems = append(c.problems, fmt.Sprintf("read %d is %q, and this fixture states %d "+
			"reads", at+1, q.SQL, len(c.reads)))

		return driver.Rows{}, errors.New("this fixture states no such read")
	}

	staged := c.reads[at]
	c.compare(fmt.Sprintf("read %d", at+1), staged.SQL, staged.Args, q)

	return driver.Rows{Columns: staged.Columns, Values: staged.Rows}, nil
}

// Perform runs the next statement this fixture staged, reporting one it never said it would send.
func (c *conn) Perform(_ context.Context, q driver.Query) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	at := c.performed
	c.performed++

	if at >= len(c.writes) {
		c.problems = append(c.problems, fmt.Sprintf("statement %d is %q, and this fixture states "+
			"%d statements", at+1, q.SQL, len(c.writes)))

		return 0, errors.New("this fixture states no such statement")
	}

	staged := c.writes[at]
	c.compare(fmt.Sprintf("statement %d", at+1), staged.SQL, staged.Args, q)

	return affected(staged.RowsAffected), nil
}

// compare holds what a migration actually sent to what every language is required to send.
func (c *conn) compare(label, sql string, args []string, q driver.Query) {
	if q.SQL != sql {
		c.problems = append(c.problems, fmt.Sprintf("%s: this runtime sent %q where the fixture "+
			"states %q, so two languages would not read the same thing", label, q.SQL, sql))
	}

	if !slices.Equal(q.Args, bound(args)) {
		c.problems = append(c.problems, fmt.Sprintf("%s: this runtime bound %v where the fixture "+
			"states %v", label, q.Args, bound(args)))
	}
}

// bound renders a statement's arguments, since an absent list crosses the wire as an empty one.
func bound(args []string) []string {
	if args == nil {
		return []string{}
	}

	return args
}

// affected reads a staged row count, reporting nothing changed for one that is not a number.
func affected(count string) int64 {
	var n int64

	if _, err := fmt.Sscanf(strings.TrimSpace(count), "%d", &n); err != nil {
		return 0
	}

	return n
}

// Exec reports that no fixture compiles an operation, since this suite runs none of them.
func (c *conn) Exec(context.Context, driver.Statement) error {
	return errors.New("the conformance session runs no compiled statement")
}

// Lock hands back a release that succeeds, so a caller's deferred unlock has something to call.
func (c *conn) Lock(context.Context) (driver.Unlock, error) {
	return func(context.Context) error { return nil }, nil
}

// Transact reports that no fixture opens a transaction, since this suite applies nothing.
func (c *conn) Transact(context.Context, driver.Work) error {
	return errors.New("the conformance session opens no transaction")
}

// Capabilities returns the set every fixture's host.capability.get is answered from.
func (c *conn) Capabilities() capability.Set {
	return c.caps
}

// History reports no store, since this session records nothing.
func (c *conn) History() history.Store {
	return nil
}

// ServerVersion reports what host.info tells a migration it is running against.
func (c *conn) ServerVersion() string {
	return serverVersion
}

// Close releases nothing, since this session holds nothing.
func (c *conn) Close(context.Context) error {
	return nil
}

// compile-time proof that the conformance session satisfies the contract the host callbacks reach.
var _ driver.Conn = (*conn)(nil)
