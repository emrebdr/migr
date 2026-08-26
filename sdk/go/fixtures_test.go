package migr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

// version is the migration version every fixture here registers itself under.
const version = "20260727T153000"

// file is the filename those fixtures pretend to have been registered from.
const file = version + "_add_users.go"

// errMigration is what a fixture migration returns when a test told it to fail.
var errMigration = errors.New("the migration would not run")

// plain is a migration that emits one operation and authors no down at all.
type plain struct {
	// ran records each direction this migration was asked to perform, in order.
	ran *[]string
	// fail is what Up returns, nil when it succeeds.
	fail error
	// exec is a statement it sends itself before emitting, empty when it sends none.
	exec string
	// block holds this migration until a test closes it or the engine cancels the call.
	block chan struct{}
}

// Up emits one operation, after whatever a test staged before it.
func (m plain) Up(ctx context.Context, s *Schema) error {
	if m.ran != nil {
		*m.ran = append(*m.ran, "up "+string(s.Mode()))
	}

	if m.block != nil {
		select {
		case <-m.block:
		case <-ctx.Done():
		}
	}

	if m.fail != nil {
		return m.fail
	}

	if m.exec != "" {
		if _, err := s.Exec(ctx, m.exec); err != nil {
			return err
		}
	}

	return s.Apply(ctx, Raw{Driver: "postgres", SQL: "CREATE TABLE users (id bigint)"})
}

// reversible is a migration that authors its own down.
type reversible struct{ plain }

// Down emits the operation that undoes what Up did.
func (m reversible) Down(ctx context.Context, s *Schema) error {
	if m.ran != nil {
		*m.ran = append(*m.ran, "down "+string(s.Mode()))
	}

	return s.Apply(ctx, Raw{Driver: "postgres", SQL: "DROP TABLE users"})
}

// oneWay is a migration whose author states it cannot be undone.
type oneWay struct{ plain }

// Irreversible returns the reason a rollback halts with.
func (m oneWay) Irreversible() string {
	return "the dropped rows are not recoverable; restore from a backup"
}

// checked is a migration that checks itself, which migr validate runs.
type checked struct {
	plain
	// problem is what Validate reports, nil when the migration is fine.
	problem error
}

// Validate reports what a test staged as wrong with this migration.
func (m checked) Validate() error {
	return m.problem
}

// dependent is a migration that declares the versions it must follow.
type dependent struct {
	plain
	// after are the versions this migration declares it runs after.
	after []string
}

// Dependencies returns the versions this migration declares it must follow.
func (m dependent) Dependencies() []string {
	return m.after
}

// silent declares dependencies and returns nil for them, which the wire may not carry as null.
type silent struct{ plain }

// Dependencies returns nil, which is a list this SDK has to render as an empty one.
func (m silent) Dependencies() []string {
	return nil
}

// contradictory both authors a down and declares it has none, which is not a migration.
type contradictory struct{ reversible }

// Irreversible returns a reason this migration has no business giving.
func (m contradictory) Irreversible() string {
	return "and yet it writes a Down"
}

// silentlyOneWay declares itself irreversible and gives no reason, which invariant #10 refuses.
type silentlyOneWay struct{ plain }

// Irreversible returns nothing at all, which is what makes it a registration error.
func (m silentlyOneWay) Irreversible() string {
	return ""
}

// engine is a fake engine driving one session over a pipe, from the other end of the wire.
type engine struct {
	t *testing.T
	// out is what this fake writes to the session's stdin.
	out *wire.Writer
	// in is what the session wrote back.
	in *wire.Reader
	// next is this fake's own id sequence, unrelated to the session's.
	next int64
	// done reports the status the session exited with, once it has.
	done chan int
	// raw is the session's stdin itself, for a test writing bytes no writer would produce.
	raw io.WriteCloser
}

// dial starts a session over two pipes and hands back the fake engine on the other end.
func dial(t *testing.T) *engine {
	t.Helper()

	toRuntime, fromEngine := io.Pipe()
	toEngine, fromRuntime := io.Pipe()

	s := newSession(toRuntime, fromRuntime)
	e := &engine{
		t: t, out: wire.NewWriter(fromEngine), in: wire.NewReader(toEngine),
		done: make(chan int, 1), raw: fromEngine,
	}

	go func() {
		e.done <- s.serve()
		_ = fromRuntime.Close()
	}()

	t.Cleanup(func() { _ = fromEngine.Close() })

	return e
}

// close ends the session's stdin, which §11 says ends the process.
func (e *engine) close() {
	_ = e.raw.Close()
}

// replay runs a session over a script already written, since io.Pipe blocks once a session exits.
func replay(t *testing.T, script string) ([]*wire.Message, int) {
	t.Helper()

	var out bytes.Buffer

	code := newSession(strings.NewReader(script), &out).serve()

	return drain(t, &out), code
}

// drain reads every message a replayed session wrote, which is however many it chose to write.
func drain(t *testing.T, out *bytes.Buffer) []*wire.Message {
	t.Helper()

	r := wire.NewReader(bytes.NewReader(out.Bytes()))

	var written []*wire.Message

	for {
		m, err := r.Read()
		if err != nil {
			return written
		}

		written = append(written, m)
	}
}

// line renders one message as the bytes a script stages it as.
func line(t *testing.T, m *wire.Message) string {
	t.Helper()

	encoded, err := wire.Encode(m)
	if err != nil {
		t.Fatalf("encoding a staged message: %v", err)
	}

	return string(encoded) + "\n"
}

// handshake is the initialize request a staged script opens with.
func handshake(t *testing.T) string {
	t.Helper()

	payload, err := wire.MarshalPayload(wire.InitializeParams{
		ProtocolVersions: []int{wire.Version}, IRVersion: wire.IRVersion,
	})
	if err != nil {
		t.Fatalf("encoding the handshake: %v", err)
	}

	return line(t, wire.NewRequest(1, wire.Initialize, payload))
}

// send writes one request and returns the id it must be answered under.
func (e *engine) send(method string, params any) int64 {
	e.t.Helper()

	payload, err := wire.MarshalPayload(params)
	if err != nil {
		e.t.Fatalf("encoding %s: %v", method, err)
	}

	e.next++

	if err := e.out.Write(wire.NewRequest(e.next, method, payload)); err != nil {
		e.t.Fatalf("sending %s: %v", method, err)
	}

	return e.next
}

// notify writes one notification, which nothing answers.
func (e *engine) notify(method string, params any) {
	e.t.Helper()

	payload, err := wire.MarshalPayload(params)
	if err != nil {
		e.t.Fatalf("encoding %s: %v", method, err)
	}

	if err := e.out.Write(wire.NewNotification(method, payload)); err != nil {
		e.t.Fatalf("sending %s: %v", method, err)
	}
}

// read takes the next message the session wrote, whatever it is.
func (e *engine) read() *wire.Message {
	e.t.Helper()

	m, err := e.in.Read()
	if err != nil {
		e.t.Fatalf("reading what the runtime said: %v", err)
	}

	return m
}

// answer reads messages until one answers the named id, serving every callback on the way.
func (e *engine) answer(id int64, serve func(*wire.Message) *wire.Message) *wire.Message {
	e.t.Helper()

	for {
		m := e.read()

		if m.IsResponse() {
			if got, _ := m.ID.Number(); got == id {
				return m
			}

			continue
		}

		e.reply(m, serve)
	}
}

// reply answers one callback the way a test staged, or with an empty result when it staged none.
func (e *engine) reply(m *wire.Message, serve func(*wire.Message) *wire.Message) {
	e.t.Helper()

	if m.IsNotification() {
		return
	}

	id, _ := m.ID.Number()

	answer := wire.NewResponse(id, json.RawMessage("{}"))
	if serve != nil {
		if staged := serve(m); staged != nil {
			answer = staged
		}
	}

	if err := e.out.Write(answer); err != nil {
		e.t.Fatalf("answering %s: %v", m.Method, err)
	}
}

// greet completes the handshake, which every other method needs to have happened.
func (e *engine) greet() *wire.Message {
	e.t.Helper()

	id := e.send(wire.Initialize, wire.InitializeParams{
		ProtocolVersions: []int{wire.Version},
		IRVersion:        wire.IRVersion,
		Client:           wire.Client{Name: "migr", Version: "0.1.0"},
		Migrations:       wire.MigrationsAt{Dir: "/srv/app/migrations", Language: "go"},
		Database:         wire.DatabaseAt{Name: "default", Driver: "postgres"},
	})

	return e.answer(id, nil)
}

// finish sends shutdown and returns the status the session exited with.
func (e *engine) finish() int {
	e.t.Helper()

	id := e.send(wire.Shutdown, struct{}{})
	e.answer(id, nil)

	return <-e.done
}

// staged replaces the registry for one test, so registrations do not leak between them.
func staged(t *testing.T, migrations map[string]Migration) {
	t.Helper()

	previous := registered
	t.Cleanup(func() { registered = previous })

	registered = &registry{loaded: map[string]*Registered{}}

	for filename, m := range migrations {
		registered.add(m, filename)
	}
}

// slugOf returns the slug an error response carries, which is what an SDK branches on.
func slugOf(t *testing.T, m *wire.Message) string {
	t.Helper()

	if m.Error == nil {
		t.Fatalf("the message carries no error: %+v", m)
	}

	return m.Error.Slug()
}

// reasonOf returns what data.reason names, which §7.4 requires of declared_irreversible.
func reasonOf(t *testing.T, m *wire.Message) string {
	t.Helper()

	if m.Error == nil {
		t.Fatalf("the message carries no error: %+v", m)
	}

	var data struct {
		Reason string `json:"reason"`
	}

	if err := json.Unmarshal(m.Error.Data, &data); err != nil {
		t.Fatalf("reading the error's data: %v", err)
	}

	return data.Reason
}

// decode reads a response's result into a target, failing the test rather than the session.
func decode(t *testing.T, m *wire.Message, target any) {
	t.Helper()

	if m.Error != nil {
		t.Fatalf("the call failed: %v", m.Error)
	}

	if err := wire.UnmarshalPayload(m.Result, target); err != nil {
		t.Fatalf("reading the result: %v", err)
	}
}
