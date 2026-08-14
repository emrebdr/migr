package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

// Options are what a server needs to answer the callbacks a migration makes.
type Options struct {
	// Conn is the session every database callback reaches, already opened.
	Conn driver.Conn
	// Database is the key migr.yaml holds the target under, which host.info reports.
	Database string
	// Log is where host.log's lines are filed.
	Log *slog.Logger
}

// check reports what a server was not given, before a runtime asks it anything.
func (o Options) check() error {
	switch {
	case o.Conn == nil:
		return ErrNoConn
	case o.Database == "":
		return ErrNoDatabase
	case o.Log == nil:
		return ErrNoLog
	}

	return nil
}

// Server answers the callbacks a runtime makes, for one engine request at a time.
type Server struct {
	opts Options
	mu   sync.Mutex
	call *Call
}

// New builds a host callback server, refusing one that could not answer.
func New(opts Options) (*Server, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}

	return &Server{opts: opts}, nil
}

// Serve answers one callback, and returns nothing for a notification, which is never answered.
func (s *Server) Serve(ctx context.Context, m *protocol.Message) *protocol.Message {
	if m == nil || m.IsResponse() {
		return nil
	}

	if m.IsNotification() {
		if m.Method == protocol.HostLog {
			s.file(ctx, m.Params)
		}

		return nil
	}

	id, _ := m.ID.Number()

	result, failed := s.answer(ctx, m.Method, m.Params)
	if failed != nil {
		return protocol.NewFailure(id, failed)
	}

	return protocol.NewResponse(id, result)
}

// answer routes one request to the callback that serves it, or refuses it in the callers' terms.
func (s *Server) answer(ctx context.Context, method string, params json.RawMessage,
) (json.RawMessage, *protocol.Error) {
	if !slices.Contains(protocol.HostMethods(), method) {
		return nil, protocol.NewErrorf(protocol.CodeMethodNotFound,
			"%q is not a method this protocol version serves", method)
	}

	call, refused := s.window(method)
	if refused != nil {
		return nil, refused
	}

	switch method {
	case protocol.HostSchemaApply:
		return s.applySchema(call, params)
	case protocol.HostDBQuery:
		return s.query(ctx, params)
	case protocol.HostDBExec:
		return s.exec(ctx, call, params)
	case protocol.HostCapabilityGet:
		return s.capability(params)
	case protocol.HostInfo:
		return s.info(call)
	case protocol.HostLog:
		return nil, protocol.NewError(protocol.CodeInvalidRequest,
			"host.log is a notification, and this one carries an id to answer")
	default:
		return nil, protocol.NewErrorf(protocol.CodeMethodNotFound,
			"%q is not a method this host serves", method)
	}
}

// window returns the call in flight, refusing a callback that arrived outside one.
func (s *Server) window(method string) (*Call, *protocol.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.call == nil {
		return nil, protocol.NewErrorf(protocol.CodeSessionState,
			"%s arrived with no engine request in flight", method)
	}

	if s.call.recorder == nil && changes(method) {
		return nil, protocol.NewErrorf(protocol.CodeSessionState,
			"%s arrived during the validation of %s, which changes nothing",
			method, s.call.migration)
	}

	return s.call, nil
}

// changes reports whether a callback would alter something, which a validation window forbids.
func changes(method string) bool {
	return method == protocol.HostSchemaApply || method == protocol.HostDBExec
}

// open makes a call the one in flight, refusing a second while one is still open.
func (s *Server) open(call *Call) (*Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.call != nil {
		return nil, fmt.Errorf("%w: %s is still in flight", ErrCallOpen, s.call.migration)
	}

	s.call = call

	return call, nil
}
