package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/runtime/host"
	"github.com/emrebdr/migr/internal/runtime/protocol"
	"github.com/emrebdr/migr/internal/version"
)

// Options are what a runtime session needs beyond the adapter that starts its process.
type Options struct {
	// Runtime is the adapter that says how to start this language, and starts nothing itself.
	Runtime Runtime
	// Project is what initialize tells the runtime about the run, and carries no secret.
	Project Project
	// Conn is the open session every database callback reaches, which the runtime never sees.
	Conn driver.Conn
	// Log is where host.log's lines and the runtime's own stderr are filed.
	Log *slog.Logger
	// Timeouts are §8.2's bounds, each defaulted from the protocol's own where migr.yaml set none.
	Timeouts Timeouts
}

// check reports what a session was not given, before it starts a process it could not talk to.
func (o Options) check() error {
	switch {
	case o.Runtime == nil:
		return ErrNoRuntime
	case o.Conn == nil:
		return ErrNoConn
	case o.Log == nil:
		return ErrNoLog
	}

	return o.Project.Check()
}

// Session is one runtime process and the conversation the engine is having with it.
type Session struct {
	opts   Options
	bounds Timeouts
	proc   *process
	in     *protocol.Writer
	out    *protocol.Reader
	server *host.Server
	errs   *tail

	// mu guards the id sequence, the pending table, and why the session ended.
	mu sync.Mutex
	// next is this side's own sequence, which §2.5 keeps unrelated to the runtime's.
	next int64
	// pending are the engine's own outstanding requests, and never a runtime's.
	pending map[int64]chan *protocol.Message
	// ended is why the session stopped, once it has.
	ended error

	// endProcess is the backstop: it kills the group even if a rung of the ladder was skipped.
	endProcess context.CancelFunc
	// done is closed when the read loop has stopped, whatever stopped it.
	done chan struct{}
	// closing makes the shutdown ladder run once however many callers ask for it.
	closing sync.Once
	// closed is what that ladder reported.
	closed error
}

// compile-time proof that a live runtime satisfies the contract the executor asks migrations through.
var _ engine.Source = (*Session)(nil)

// Start spawns the runtime, shakes hands with it, and leaves it ready to run a migration.
func Start(ctx context.Context, opts Options) (*Session, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}

	server, err := host.New(host.Options{
		Conn: opts.Conn, Database: opts.Project.Database, Log: opts.Log,
	})
	if err != nil {
		return nil, err
	}

	command, err := opts.Runtime.Command(ctx, opts.Project)
	if err != nil {
		return nil, fmt.Errorf("preparing the %s runtime: %w", opts.Runtime.Name(), err)
	}

	s, err := open(ctx, opts, server, command)
	if err != nil {
		return nil, err
	}

	return s, s.handshake(ctx)
}

// open starts the process and the two goroutines that read what it says, before any message.
func open(ctx context.Context, opts Options, server *host.Server, command Command) (*Session, error) {
	bounds := opts.Timeouts.Resolve()
	lifetime, endProcess := context.WithCancel(context.WithoutCancel(ctx))

	proc, err := spawn(lifetime, withDefaults(command, opts.Project), bounds.ShutdownGrace)
	if err != nil {
		endProcess()

		return nil, err
	}

	s := &Session{
		opts: opts, bounds: bounds, proc: proc, server: server, endProcess: endProcess,
		in:      protocol.NewWriter(proc.in),
		out:     protocol.NewReader(proc.out),
		errs:    follow(context.WithoutCancel(ctx), proc.errs, opts.Log, opts.Runtime.Name()),
		pending: make(map[int64]chan *protocol.Message),
		done:    make(chan struct{}),
	}

	go s.loop(ctx)

	return s, nil
}

// withDefaults fills what an adapter left unset, since §9 fixes the working directory rather than it.
func withDefaults(c Command, p Project) Command {
	if c.Dir == "" {
		c.Dir = p.Root
	}

	return c
}

// handshake offers every version this engine speaks and holds the runtime to picking one of them.
func (s *Session) handshake(ctx context.Context) error {
	var answered protocol.InitializeResult

	err := s.call(ctx, protocol.Initialize, s.greeting(), &answered, s.bounds.Handshake)
	if err != nil {
		return s.abort(fmt.Errorf("%w: %w", ErrHandshake, err))
	}

	if answered.ProtocolVersion != protocol.Version {
		return s.abort(fmt.Errorf(
			"%w: it answered with protocol version %d, which this engine did not offer",
			ErrProtocol, answered.ProtocolVersion))
	}

	s.opts.Log.DebugContext(ctx, "runtime ready", "runtime", answered.Runtime.Name,
		"version", answered.Runtime.Version, "sdk", answered.SDK.Name,
		"protocol", answered.ProtocolVersion)

	return nil
}

// greeting is the handshake this engine sends, which names no DSN because a runtime never connects.
func (s *Session) greeting() protocol.InitializeParams {
	return protocol.InitializeParams{
		ProtocolVersions: []int{protocol.Version},
		IRVersion:        ir.Version,
		Client:           protocol.Client{Name: version.Program, Version: version.Version},
		Migrations: protocol.MigrationsAt{
			Dir: s.opts.Project.Dir, Language: s.opts.Project.Language,
		},
		Database: protocol.DatabaseAt{
			Name: s.opts.Project.Database, Driver: s.opts.Project.Driver,
		},
	}
}

// abort ends a session that never became one, giving it the grace to go before ending it.
func (s *Session) abort(cause error) error {
	s.closing.Do(func() {
		s.settle()
		s.closed = withExit(cause, s.exited(s.proc.exit()))
	})

	return s.closed
}

// withExit adds what a process exited with, when it chose a status rather than being killed here.
func withExit(cause, exit error) error {
	var chosen *ExitError
	if !errors.As(exit, &chosen) {
		return cause
	}

	return fmt.Errorf("%w: %w", cause, exit)
}

// Close asks the runtime to stop, and ends it whether or not it agreed to.
func (s *Session) Close(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)

	s.closing.Do(func() {
		s.closed = s.stop(ctx)
	})

	return s.closed
}

// stop climbs §8.2's ladder: shutdown, the grace, SIGTERM, the grace again, then SIGKILL.
func (s *Session) stop(ctx context.Context) error {
	if s.ask(ctx) == nil && s.proc.waitFor(s.bounds.ShutdownGrace) {
		s.settle()

		return s.exited(s.proc.exit())
	}

	ended := s.proc.stop(s.bounds.ShutdownGrace)
	s.settle()

	return s.exited(ended)
}

// ask sends shutdown, unless the session has already ended and there is nothing left to ask.
func (s *Session) ask(ctx context.Context) error {
	if s.over() {
		return ErrClosed
	}

	var nothing struct{}

	return s.call(ctx, protocol.Shutdown, struct{}{}, &nothing, s.bounds.ShutdownGrace)
}

// settle waits for the runtime to go, ends whatever it left in its group, and gives the pipes back.
func (s *Session) settle() {
	s.proc.waitFor(s.bounds.ShutdownGrace)
	s.proc.kill()
	<-s.done
	s.errs.wait()
	s.proc.closePipes()
	s.endProcess()
}

// exited reports a runtime that chose a non-zero status, with the last lines it wrote, per §9.
func (s *Session) exited(cause error) error {
	var status *exec.ExitError
	if !errors.As(cause, &status) || status.ExitCode() < 0 {
		return cause
	}

	return &ExitError{
		Runtime: s.opts.Runtime.Name(), Code: status.ExitCode(), Stderr: s.errs.lines(),
	}
}

// over reports whether the read loop has already stopped, which is what ends a session.
func (s *Session) over() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// call sends one request and waits for its answer, canceling it when the caller stops waiting.
func (s *Session) call(ctx context.Context, method string, params, result any,
	bound time.Duration) error {
	payload, err := protocol.MarshalPayload(params)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", method, err)
	}

	id, replies, err := s.issue()
	if err != nil {
		return err
	}

	defer s.retire(id)

	if err := s.in.Write(protocol.NewRequest(id, method, payload)); err != nil {
		return fmt.Errorf("sending %s: %w", method, s.reasonFor(err))
	}

	answer, err := s.await(ctx, id, replies, bound)
	if err != nil {
		return err
	}

	if answer.Error != nil {
		return answer.Error
	}

	return protocol.UnmarshalPayload(answer.Result, result)
}

// notify sends one notification, which nothing answers and nothing waits on.
func (s *Session) notify(method string, params any) {
	payload, err := protocol.MarshalPayload(params)
	if err != nil {
		return
	}

	_ = s.in.Write(protocol.NewNotification(method, payload))
}

// issue takes the next id in this side's own sequence and registers what will answer it.
func (s *Session) issue() (int64, chan *protocol.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pending == nil {
		return 0, nil, ErrClosed
	}

	s.next++
	replies := make(chan *protocol.Message, 1)
	s.pending[s.next] = replies

	return s.next, replies, nil
}

// retire forgets a request once its caller has stopped waiting, however that came about.
func (s *Session) retire(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.pending, id)
}

// await waits for one answer under the bound §8.2 gives it, and none at all for migration.execute.
func (s *Session) await(ctx context.Context, id int64, replies chan *protocol.Message,
	bound time.Duration) (*protocol.Message, error) {
	if bound > protocol.NoTimeout {
		var stop context.CancelFunc

		ctx, stop = context.WithTimeout(ctx, bound)
		defer stop()
	}

	select {
	case answer := <-replies:
		return answer, nil
	case <-s.done:
		return nil, s.reason()
	case <-ctx.Done():
		return nil, s.abandon(id, replies, ctx.Err())
	}
}

// abandon cancels a request the caller stopped waiting on, and kills the runtime that ignores it.
func (s *Session) abandon(id int64, replies chan *protocol.Message, cause error) error {
	s.notify(protocol.Cancel, protocol.CancelParams{ID: id})

	timer := time.NewTimer(s.bounds.CancelGrace)
	defer timer.Stop()

	select {
	case <-replies:
		return fmt.Errorf("%w: %w", ErrCanceled, cause)
	case <-s.done:
		return fmt.Errorf("%w: %w", ErrCanceled, cause)
	case <-timer.C:
		s.proc.kill()

		return fmt.Errorf("%w: %w, and it did not answer within %s, so it was killed",
			ErrCanceled, cause, s.bounds.CancelGrace)
	}
}

// loop reads what the runtime says until something ends the session, which nothing recovers from.
func (s *Session) loop(ctx context.Context) {
	defer close(s.done)

	for {
		m, err := s.out.Read()
		if err != nil {
			s.end(err)

			return
		}

		if err := s.route(ctx, m); err != nil {
			s.end(err)

			return
		}
	}
}

// route sends a response to whoever is waiting on it and everything else to the callback server.
func (s *Session) route(ctx context.Context, m *protocol.Message) error {
	if m.IsResponse() {
		return s.deliver(m)
	}

	reply := s.server.Serve(ctx, m)
	if reply == nil {
		return nil
	}

	return s.in.Write(reply)
}

// deliver hands one answer to the request that is waiting on it, in this direction's own table.
func (s *Session) deliver(m *protocol.Message) error {
	id, numbered := m.ID.Number()
	if !numbered {
		return fmt.Errorf("%w: it sent a response with no id to answer", ErrProtocol)
	}

	s.mu.Lock()
	replies, waiting := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()

	if !waiting {
		return fmt.Errorf("%w: it answered request %d, which this engine is not waiting on -- "+
			"an id is unique within its own direction (§2.5)", ErrProtocol, id)
	}

	replies <- m

	return nil
}

// end records why the session stopped, telling the runtime first when there is anything to tell.
func (s *Session) end(cause error) {
	if refusal := protocol.Refuse(cause); refusal != nil {
		_ = s.in.Write(refusal)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.ended = cause
	s.pending = nil
}

// reason reports why the session ended, reading the end of the stream as the end it usually is.
func (s *Session) reason() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ended == nil || errors.Is(s.ended, io.EOF) {
		return fmt.Errorf("%w: the runtime closed its stdout", ErrClosed)
	}

	return s.ended
}

// reasonFor prefers why the session ended to what a write on a dead pipe happened to report.
func (s *Session) reasonFor(err error) error {
	if s.over() {
		return s.reason()
	}

	return err
}
