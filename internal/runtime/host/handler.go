package host

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strconv"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

// applySchema records the operations a migration performs, in both modes, per §6.
func (s *Server) applySchema(call *Call, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	var p protocol.SchemaApplyParams

	if err := protocol.UnmarshalPayload(params, &p); err != nil {
		return nil, protocol.NewErrorf(protocol.CodeInvalidOperation, "%v", err)
	}

	if err := call.record(p.Operations); err != nil {
		return nil, protocol.NewErrorf(protocol.CodeInvalidOperation, "%v", err)
	}

	return answer(protocol.SchemaApplyResult{Recorded: len(p.Operations)})
}

// query runs a migration's own read, which the driver holds to being one, in both modes.
func (s *Server) query(ctx context.Context, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	var p protocol.DBQueryParams

	if refused := parameters(params, &p); refused != nil {
		return nil, refused
	}

	rows, err := s.opts.Conn.Query(ctx, driver.Query{SQL: p.SQL, Args: p.Args})
	if err != nil {
		return nil, refuse(err)
	}

	if err := rows.Validate(); err != nil {
		return nil, protocol.NewErrorf(protocol.CodeInternal,
			"the %s driver returned a result this host cannot render: %v",
			s.opts.Conn.Capabilities().Driver(), err)
	}

	return answer(protocol.DBQueryResult{Columns: columns(rows), Rows: values(rows)})
}

// exec runs a migration's own statement, and is the one callback plan mode changes.
func (s *Server) exec(ctx context.Context, call *Call, params json.RawMessage,
) (json.RawMessage, *protocol.Error) {
	var p protocol.DBExecParams

	if refused := parameters(params, &p); refused != nil {
		return nil, refused
	}

	statement := driver.Query{SQL: p.SQL, Args: p.Args}

	if s.opts.Mode == protocol.ModePlan {
		call.hold(statement)

		return answer(protocol.DBExecResult{RowsAffected: "0", Executed: false})
	}

	affected, err := s.opts.Conn.Perform(ctx, statement)
	if err != nil {
		return nil, refuse(err)
	}

	return answer(protocol.DBExecResult{
		RowsAffected: strconv.FormatInt(affected, 10),
		Executed:     true,
	})
}

// capability answers what the target engine makes of one whole operation, on both axes.
func (s *Server) capability(params json.RawMessage) (json.RawMessage, *protocol.Error) {
	var p protocol.CapabilityGetParams

	if err := protocol.UnmarshalPayload(params, &p); err != nil {
		return nil, protocol.NewErrorf(protocol.CodeInvalidOperation, "%v", err)
	}

	if err := p.Validate(); err != nil {
		return nil, protocol.NewErrorf(protocol.CodeInvalidOperation, "%v", err)
	}

	verdict := s.opts.Conn.Capabilities().Resolve(p.Operation)

	return answer(protocol.CapabilityGetResult{
		Support:  string(verdict.Support()),
		Strategy: verdict.Strategy(),
		Reason:   verdict.Reason(),
		Lock:     string(verdict.Lock()),
	})
}

// info reports the facts about this run a migration may branch on.
func (s *Server) info() (json.RawMessage, *protocol.Error) {
	caps := s.opts.Conn.Capabilities()

	return answer(protocol.InfoResult{
		Database:         s.opts.Database,
		Driver:           caps.Driver(),
		Server:           s.opts.Conn.ServerVersion(),
		TransactionalDDL: caps.TransactionalDDL(),
		Mode:             s.opts.Mode,
	})
}

// file hands a runtime's structured line to the engine's own logger, and never refuses one.
func (s *Server) file(ctx context.Context, params json.RawMessage) {
	var p protocol.LogParams

	if err := protocol.UnmarshalPayload(params, &p); err != nil {
		s.opts.Log.WarnContext(ctx, "a runtime sent a log line that could not be read", "error", err)

		return
	}

	if err := p.Validate(); err != nil {
		s.opts.Log.WarnContext(ctx, "a runtime sent a log line this protocol version does not define",
			"error", err, "message", p.Message)

		return
	}

	s.opts.Log.Log(ctx, severity(p.Level), p.Message, fields(p.Fields)...)
}

// parameters decodes a payload and checks what it must carry, both of which are -32602.
func parameters(params json.RawMessage, target interface{ Validate() error }) *protocol.Error {
	if err := protocol.UnmarshalPayload(params, target); err != nil {
		return protocol.NewErrorf(protocol.CodeInvalidParams, "%v", err)
	}

	if err := target.Validate(); err != nil {
		return protocol.NewErrorf(protocol.CodeInvalidParams, "%v", err)
	}

	return nil
}

// refuse tells a statement the server rejected for writing from one it rejected for anything else.
func refuse(err error) *protocol.Error {
	if errors.Is(err, driver.ErrNotReadOnly) {
		return protocol.NewErrorf(protocol.CodeNotReadOnly, "%v", err)
	}

	return protocol.NewErrorf(protocol.CodeDatabase, "%v", err)
}

// answer renders a callback's result, since a payload that will not encode is the host's own bug.
func answer(v any) (json.RawMessage, *protocol.Error) {
	payload, err := protocol.MarshalPayload(v)
	if err != nil {
		return nil, protocol.NewErrorf(protocol.CodeInternal, "%v", err)
	}

	return payload, nil
}

// columns names a read's columns, rendering none as an empty list rather than as null.
func columns(rows driver.Rows) []string {
	if rows.Columns == nil {
		return []string{}
	}

	return rows.Columns
}

// values renders a read's rows, rendering none as an empty list rather than as null.
func values(rows driver.Rows) [][]*string {
	if rows.Values == nil {
		return [][]*string{}
	}

	return rows.Values
}

// severity maps a protocol level onto the engine's own, which -v thresholds against.
func severity(level protocol.Level) slog.Level {
	switch level {
	case protocol.LevelDebug:
		return slog.LevelDebug
	case protocol.LevelWarn:
		return slog.LevelWarn
	case protocol.LevelError:
		return slog.LevelError
	case protocol.LevelInfo:
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}

// fields renders a log line's attributes in key order, since map order is nobody's to depend on.
func fields(supplied map[string]string) []any {
	out := make([]any, 0, len(supplied)*2)

	for _, key := range slices.Sorted(maps.Keys(supplied)) {
		out = append(out, key, supplied[key])
	}

	return out
}
