package migr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"

	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

// The exit statuses this runtime chooses, of which only the first is not a failure (§9).
const (
	// exitOK is what a runtime exits with after answering shutdown, and after losing its engine.
	exitOK = 0
	// exitFailed is what a runtime exits with when the channel broke under it.
	exitFailed = 1
)

// Run serves this program as a migr runtime, on stdin and stdout, until the engine is done with it.
func Run() int {
	protocol, silenced := silence()

	s := newSession(os.Stdin, protocol)
	code := s.serve()

	_ = silenced.Close()
	_ = protocol.Close()

	return code
}

// silence points the language's default print target at stderr and keeps stdout for the protocol.
func silence() (*os.File, *os.File) {
	protocol := os.Stdout
	os.Stdout = os.Stderr

	return protocol, protocol
}

// state is how far through the lifecycle §3.1 draws the session has got.
type state int

// The three states a session passes through, each refusing what the others accept.
const (
	// stateNew is before initialize, when no other request may arrive.
	stateNew state = iota
	// stateReady is after initialize, when the three methods are served.
	stateReady
	// stateClosed is after shutdown, when nothing more is answered.
	stateClosed
)

// session is one conversation with an engine: the two streams and everything in flight on them.
type session struct {
	in  *wire.Reader
	out *wire.Writer

	// mu guards the state, the id sequence, the pending table, and what is running here.
	mu sync.Mutex
	// state is how far through §3.1's lifecycle this session has got.
	state state
	// next is this side's own sequence, which §2.5 keeps unrelated to the engine's.
	next int64
	// pending are this side's own outstanding callbacks, and never the engine's requests.
	pending map[int64]chan *wire.Message
	// running are the engine's requests this side is still working on, each with its own cancel.
	running map[int64]context.CancelFunc

	// work counts the requests dispatched to a goroutine, so serve waits for them before exiting.
	work sync.WaitGroup
	// stated is the irVersion the engine stated at initialize, the newest it reads.
	stated int
}

// newSession builds a session over one pair of streams, before a single message is read.
func newSession(in io.Reader, out io.Writer) *session {
	return &session{
		in:      wire.NewReader(in),
		out:     wire.NewWriter(out),
		pending: map[int64]chan *wire.Message{},
		running: map[int64]context.CancelFunc{},
	}
}

// serve reads until the engine says shutdown, closes its stdin, or sends a line it cannot frame.
func (s *session) serve() int {
	code := s.loop()

	s.abandonAll()
	s.work.Wait()

	return code
}

// loop reads one message at a time, which is what keeps $/cancel readable while work is running.
func (s *session) loop() int {
	for {
		m, err := s.in.Read()
		if err != nil {
			return s.ended(err)
		}

		if done := s.route(m); done {
			return exitOK
		}
	}
}

// ended reports how a session finished when it was the channel rather than the engine that ended it.
func (s *session) ended(err error) int {
	if errors.Is(err, io.EOF) {
		return exitOK
	}

	if refusal := wire.Refuse(err); refusal != nil {
		_ = s.out.Write(refusal)
	}

	return exitFailed
}

// route sends a response to whoever is waiting on it and everything else to the dispatcher.
func (s *session) route(m *wire.Message) bool {
	if m.IsResponse() {
		s.deliver(m)

		return false
	}

	if m.IsNotification() {
		s.notified(m)

		return false
	}

	return s.dispatch(m)
}

// notified acts on the one notification the engine sends, which is the only one it may.
func (s *session) notified(m *wire.Message) {
	if m.Method != wire.Cancel {
		return
	}

	var p wire.CancelParams
	if err := wire.UnmarshalPayload(m.Params, &p); err != nil {
		return
	}

	s.abandon(p.ID)
}

// dispatch answers one engine request, reporting whether it was the one that ends the session.
func (s *session) dispatch(m *wire.Message) bool {
	id, _ := m.ID.Number()

	if refusal := s.admit(m.Method); refusal != nil {
		s.answer(wire.NewFailure(id, refusal))

		return false
	}

	switch m.Method {
	case wire.Initialize:
		s.answer(s.initialize(id, m.Params))
	case wire.MigrationDiscover:
		s.answer(s.discover(id))
	case wire.MigrationExecute, wire.MigrationValidate:
		s.background(id, m.Method, m.Params)
	case wire.Shutdown:
		s.answer(wire.NewResponse(id, nothing()))

		return true
	default:
		s.answer(wire.NewFailure(id, wire.NewErrorf(wire.CodeMethodNotFound,
			"%q is not a method this runtime serves", m.Method)))
	}

	return false
}

// admit refuses a method the session is in no state to answer, which §3.1 makes session_state.
func (s *session) admit(method string) *wire.Error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case s.state == stateClosed:
		return wire.NewErrorf(wire.CodeSessionState,
			"%q arrived after shutdown was answered", method)
	case s.state == stateNew && method != wire.Initialize:
		return wire.NewErrorf(wire.CodeSessionState,
			"%q arrived before initialize, which is the first message on the channel", method)
	case s.state == stateReady && method == wire.Initialize:
		return wire.NewError(wire.CodeSessionState,
			"initialize arrived twice, and this session is already open")
	}

	if method == wire.Shutdown {
		s.state = stateClosed
	}

	return nil
}

// initialize picks one of the versions the engine offered, or reports that it speaks none of them.
func (s *session) initialize(id int64, params json.RawMessage) *wire.Message {
	var p wire.InitializeParams

	if err := wire.UnmarshalPayload(params, &p); err != nil {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeInvalidParams, "%v", err))
	}

	if !slices.Contains(p.ProtocolVersions, wire.Version) {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeUnsupportedProtocol,
			"this SDK speaks protocol version %d; the engine offered %v",
			wire.Version, p.ProtocolVersions))
	}

	if p.IRVersion < 1 {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeUnsupportedIR,
			"the engine stated IR version %d, and no version below 1 exists", p.IRVersion))
	}

	s.open(p.IRVersion)

	return s.result(id, wire.InitializeResult{
		ProtocolVersion: wire.Version,
		Runtime:         wire.Component{Name: Language, Version: LanguageRuntime()},
		SDK:             wire.Component{Name: Name, Version: Version()},
	})
}

// open marks the handshake done, after which the three methods are served, and keeps the stated IR.
func (s *session) open(stated int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state = stateReady
	s.stated = stated
}

// statedIR returns the newest irVersion the engine reads, which every Apply is held to.
func (s *session) statedIR() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.stated
}

// discover reports every migration this program registered, or everything wrong with the set.
func (s *session) discover(id int64) *wire.Message {
	found, err := All()
	if err != nil {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeInternal, "%v", err))
	}

	migrations := make([]wire.Discovered, 0, len(found))
	for _, entry := range found {
		migrations = append(migrations, wire.Discovered{
			Version: entry.Version, File: entry.File,
			Dependencies: dependencies(entry.Migration),
		})
	}

	return s.result(id, wire.DiscoverResult{Migrations: migrations})
}

// background runs one request on a goroutine, so $/cancel stays readable while it works.
func (s *session) background(id int64, method string, params json.RawMessage) {
	ctx, stop := context.WithCancel(context.Background())
	s.begin(id, stop)
	s.work.Add(1)

	go func() {
		defer s.work.Done()
		defer s.finish(id)
		defer stop()

		s.answer(s.perform(ctx, id, method, params))
	}()
}

// perform runs the one request that was dispatched, answering canceled when the engine asked.
func (s *session) perform(ctx context.Context, id int64, method string,
	params json.RawMessage) *wire.Message {
	answer := s.chosen(ctx, id, method, params)

	if ctx.Err() != nil {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeCanceled,
			"%s was abandoned because the engine asked", method))
	}

	return answer
}

// chosen routes a backgrounded request to the handler that runs it.
func (s *session) chosen(ctx context.Context, id int64, method string,
	params json.RawMessage) *wire.Message {
	if method == wire.MigrationValidate {
		return s.validate(id, params)
	}

	return s.execute(ctx, id, params)
}

// execute runs one migration in one direction, in the mode this call states and no other.
func (s *session) execute(ctx context.Context, id int64, params json.RawMessage) *wire.Message {
	var p wire.ExecuteParams

	if err := wire.UnmarshalPayload(params, &p); err != nil {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeInvalidParams, "%v", err))
	}

	if err := p.Validate(); err != nil {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeInvalidParams, "%v", err))
	}

	entry, ok := Lookup(p.ID)
	if !ok {
		return wire.NewFailure(id, unknown(p.ID))
	}

	run, refusal := direction(entry, p.Direction)
	if refusal != nil {
		return wire.NewFailure(id, refusal)
	}

	return s.emit(ctx, id, entry, run, p)
}

// emit opens the window this call acts through and reports how many operations it recorded.
func (s *session) emit(ctx context.Context, id int64, entry *Registered,
	run func(context.Context, *Schema) error, p wire.ExecuteParams) *wire.Message {
	window := NewWindow(entry.Version, Direction(p.Direction), Mode(p.Mode), s)

	err := run(ctx, window.Schema())

	emitted, forgotten := window.Seal()
	if err == nil {
		err = forgotten
	}

	if err != nil {
		return wire.NewFailure(id, failed(entry, p.Direction, err))
	}

	return s.result(id, wire.ExecuteResult{Operations: emitted})
}

// direction returns what runs this migration one way, or §7.4's refusal for a down it has none of.
func direction(entry *Registered, dir wire.Direction) (func(context.Context, *Schema) error, *wire.Error) {
	if dir == wire.Up {
		return entry.Migration.Up, nil
	}

	undo, reason := Undone(entry.Migration)

	if undo == UndoDeclared {
		return nil, wire.WithReason(wire.CodeDeclaredIrreversible,
			"this migration declares that it cannot be undone", reason)
	}

	down, writes := authored(entry.Migration)
	if !writes {
		return nil, wire.NewErrorf(wire.CodeNoDownAuthored,
			"%s authors no down of its own", entry.File)
	}

	return down.Down, nil
}

// validate runs one migration's own check, which emits nothing and reaches no database.
func (s *session) validate(id int64, params json.RawMessage) *wire.Message {
	var p wire.ValidateParams

	if err := wire.UnmarshalPayload(params, &p); err != nil {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeInvalidParams, "%v", err))
	}

	if err := p.Validate(); err != nil {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeInvalidParams, "%v", err))
	}

	entry, ok := Lookup(p.ID)
	if !ok {
		return wire.NewFailure(id, unknown(p.ID))
	}

	if err := check(entry.Migration); err != nil {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeMigrationFailed,
			"%s: %v", entry.File, err))
	}

	return s.result(id, struct{}{})
}

// unknown refuses an id naming no migration this program registered.
func unknown(version string) *wire.Error {
	return wire.NewErrorf(wire.CodeUnknownMigration,
		"%v: %s", ErrUnknownMigration, version)
}

// failed reports a migration whose own code returned an error, naming an IR the engine cannot read.
func failed(entry *Registered, dir wire.Direction, err error) *wire.Error {
	if errors.Is(err, ErrNewerIR) {
		return wire.NewErrorf(wire.CodeUnsupportedIR, "%s %s: %v", entry.File, dir, err)
	}

	return wire.NewErrorf(wire.CodeMigrationFailed, "%s %s: %v", entry.File, dir, err)
}

// Call sends one callback and waits for its answer, which the read loop delivers.
func (s *session) Call(ctx context.Context, method string, params, target any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %s", ErrCanceled, method)
	}

	payload, err := wire.MarshalPayload(params)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", method, err)
	}

	id, replies, err := s.issue()
	if err != nil {
		return err
	}

	defer s.retire(id)

	if err := s.out.Write(wire.NewRequest(id, method, payload)); err != nil {
		return fmt.Errorf("sending %s: %w", method, err)
	}

	return s.await(ctx, method, replies, target)
}

// await waits for one callback's answer, or for the engine to abandon the work that made it.
func (s *session) await(ctx context.Context, method string, replies chan *wire.Message,
	target any) error {
	select {
	case answer, open := <-replies:
		if !open {
			return fmt.Errorf("%w: %s", ErrClosed, method)
		}

		if answer.Error != nil {
			return answer.Error
		}

		return wire.UnmarshalPayload(answer.Result, target)
	case <-ctx.Done():
		return fmt.Errorf("%w: %s", ErrCanceled, method)
	}
}

// Notify sends one notification, which nothing answers and nothing waits on.
func (s *session) Notify(method string, params any) error {
	payload, err := wire.MarshalPayload(params)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", method, err)
	}

	return s.out.Write(wire.NewNotification(method, payload))
}

// issue takes the next id in this side's own sequence and registers what will answer it.
func (s *session) issue() (int64, chan *wire.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pending == nil {
		return 0, nil, ErrClosed
	}

	s.next++
	replies := make(chan *wire.Message, 1)
	s.pending[s.next] = replies

	return s.next, replies, nil
}

// retire forgets a callback once its caller has stopped waiting, however that came about.
func (s *session) retire(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.pending, id)
}

// deliver hands one answer to the callback waiting on it, in this direction's own table.
func (s *session) deliver(m *wire.Message) {
	id, numbered := m.ID.Number()
	if !numbered {
		return
	}

	s.mu.Lock()
	replies, waiting := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()

	if waiting {
		replies <- m
	}
}

// begin records an engine request this side is working on, so $/cancel can reach it.
func (s *session) begin(id int64, stop context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.running[id] = stop
}

// finish forgets a request this side has answered.
func (s *session) finish(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.running, id)
}

// abandon stops the request the engine named, which its handler then answers with canceled.
func (s *session) abandon(id int64) {
	s.mu.Lock()
	stop, running := s.running[id]
	s.mu.Unlock()

	if running {
		stop()
	}
}

// abandonAll stops everything still running once the channel has closed under it.
func (s *session) abandonAll() {
	s.mu.Lock()
	stopping := make([]context.CancelFunc, 0, len(s.running))

	for _, stop := range s.running {
		stopping = append(stopping, stop)
	}

	waiting := s.pending
	s.pending = nil
	s.mu.Unlock()

	for _, replies := range waiting {
		close(replies)
	}

	for _, stop := range stopping {
		stop()
	}
}

// answer writes one response, since a request the engine is waiting on is answered or nothing is.
func (s *session) answer(m *wire.Message) {
	if m == nil {
		return
	}

	_ = s.out.Write(m)
}

// result renders one successful answer, reporting an unencodable payload as this side's own bug.
func (s *session) result(id int64, payload any) *wire.Message {
	encoded, err := wire.MarshalPayload(payload)
	if err != nil {
		return wire.NewFailure(id, wire.NewErrorf(wire.CodeInternal, "%v", err))
	}

	return wire.NewResponse(id, encoded)
}

// nothing is the empty result the answers with nothing to report carry.
func nothing() json.RawMessage {
	return json.RawMessage("{}")
}
