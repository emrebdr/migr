package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// JSONRPC is the only value the jsonrpc member ever carries, and both sides check it.
const JSONRPC = "2.0"

// Message is one JSON-RPC 2.0 message: a request, a notification, or a response.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      ID              `json:"id,omitzero"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// NewRequest builds a request: a method, the id its sender waits on, and its parameters.
func NewRequest(id int64, method string, params json.RawMessage) *Message {
	return &Message{JSONRPC: JSONRPC, ID: NewID(id), Method: method, Params: params}
}

// NewNotification builds a notification, which only the methods Notifies accepts may be.
func NewNotification(method string, params json.RawMessage) *Message {
	return &Message{JSONRPC: JSONRPC, Method: method, Params: params}
}

// NewResponse builds the successful answer to a request.
func NewResponse(id int64, result json.RawMessage) *Message {
	return &Message{JSONRPC: JSONRPC, ID: NewID(id), Result: result}
}

// NewFailure builds the failed answer to a request.
func NewFailure(id int64, e *Error) *Message {
	return &Message{JSONRPC: JSONRPC, ID: NewID(id), Error: e}
}

// IsRequest reports whether the message is a call its sender is waiting on an answer to.
func (m *Message) IsRequest() bool {
	return m.Method != "" && m.ID.set
}

// IsNotification reports whether the message is a call that is never answered.
func (m *Message) IsNotification() bool {
	return m.Method != "" && m.ID.IsZero()
}

// IsResponse reports whether the message answers a request.
func (m *Message) IsResponse() bool {
	return m.Method == ""
}

// Validate reports whether a message is one of the three shapes this protocol defines.
func (m *Message) Validate() error {
	if m.JSONRPC != JSONRPC {
		return NewErrorf(CodeInvalidRequest, "jsonrpc must be %q, and this message carries %q",
			JSONRPC, m.JSONRPC)
	}

	if m.Method != "" {
		return m.validateCall()
	}

	return m.validateAnswer()
}

// validateCall checks a request or a notification, which are told apart by their id alone.
func (m *Message) validateCall() error {
	if m.Result != nil || m.Error != nil {
		return NewErrorf(CodeInvalidRequest,
			"%q carries a result or an error, which only a response does", m.Method)
	}

	if m.ID.null {
		return NewErrorf(CodeInvalidRequest,
			"%q carries a null id, which only the one framing error carries", m.Method)
	}

	if !m.ID.set && !Notifies(m.Method) {
		return NewErrorf(CodeInvalidRequest,
			"%q has no id, and only %s may be sent as a notification",
			m.Method, describeNotifications())
	}

	return nil
}

// validateAnswer checks a response, which carries an id and exactly one of a result and an error.
func (m *Message) validateAnswer() error {
	if m.ID.IsZero() {
		return NewError(CodeInvalidRequest,
			"a message with no method and no id is not a request, a notification, or a response")
	}

	if m.Result != nil && m.Error != nil {
		return NewErrorf(CodeInvalidRequest,
			"the response to id %s carries a result and an error, and it carries one or the other",
			m.ID)
	}

	if m.Result == nil && m.Error == nil {
		return NewErrorf(CodeInvalidRequest,
			"the response to id %s carries neither a result nor an error", m.ID)
	}

	if m.ID.null && m.Error == nil {
		return NewError(CodeInvalidRequest,
			"a null id answers no request, so it may only carry an error")
	}

	return nil
}

// ID is a request identifier: absent, an integer, or the one null §2.6 permits.
type ID struct {
	n    int64
	set  bool
	null bool
}

// NewID names a request by its number, which each side counts from 1 in its own direction.
func NewID(n int64) ID {
	return ID{n: n, set: true}
}

// NullID is what the one error response for a line that could not be framed carries.
func NullID() ID {
	return ID{null: true}
}

// Number returns the identifier's value and whether it has one.
func (i ID) Number() (int64, bool) {
	return i.n, i.set
}

// Null reports whether the identifier is the explicit null a framing error carries.
func (i ID) Null() bool {
	return i.null
}

// IsZero reports whether there is no id at all, which is what makes a call a notification.
func (i ID) IsZero() bool {
	return !i.set && !i.null
}

// String renders the identifier for a message a human reads.
func (i ID) String() string {
	switch {
	case i.set:
		return strconv.FormatInt(i.n, 10)
	case i.null:
		return "null"
	default:
		return "none"
	}
}

// MarshalJSON renders an integer or null; an absent id is omitted and never encoded.
func (i ID) MarshalJSON() ([]byte, error) {
	switch {
	case i.set:
		return strconv.AppendInt(nil, i.n, 10), nil
	case i.null:
		return []byte("null"), nil
	default:
		return nil, errors.New("an absent id is omitted rather than encoded")
	}
}

// UnmarshalJSON reads an integer or null, refusing the strings and floats JSON-RPC allows.
func (i *ID) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)

	if string(trimmed) == "null" {
		*i = NullID()

		return nil
	}

	if len(trimmed) == 0 || trimmed[0] == '"' {
		return errors.New(`id must be an integer, and this protocol does not accept the strings JSON-RPC allows`)
	}

	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return fmt.Errorf("id must be an integer: %w", err)
	}

	n, err := number.Int64()
	if err != nil {
		return fmt.Errorf("id must be an integer, and %s is not one", number.String())
	}

	*i = NewID(n)

	return nil
}

// Encode renders a message as the bytes that cross the wire, without its terminating newline.
func Encode(m *Message) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("encoding the message: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Decode reads one line's bytes into a message, refusing everything that is not one.
func Decode(data []byte) (*Message, error) {
	trimmed := bytes.TrimSpace(data)

	if len(trimmed) == 0 {
		return nil, NewError(CodeParse, "the line carries no message")
	}

	if trimmed[0] == '[' {
		return nil, NewError(CodeInvalidRequest,
			"a top-level array is not a message, and batch requests are not supported")
	}

	var m Message

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&m); err != nil {
		return nil, decodeFailure(err)
	}

	if err := refuseTrailing(dec); err != nil {
		return nil, err
	}

	if err := m.Validate(); err != nil {
		return nil, err
	}

	return &m, nil
}

// decodeFailure tells a line that is not JSON from a line that is JSON and not a message.
func decodeFailure(err error) error {
	if notJSON(err) {
		return NewErrorf(CodeParse, "the line is not JSON: %v", err)
	}

	return NewErrorf(CodeInvalidRequest, "the line is not a message: %v", err)
}

// notJSON reports whether a decoder gave up on the syntax rather than on the shape.
func notJSON(err error) bool {
	var syntax *json.SyntaxError

	return errors.As(err, &syntax) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.EOF)
}

// refuseTrailing refuses a line carrying anything past its message, which two messages in one is.
func refuseTrailing(dec *json.Decoder) error {
	rest, err := io.ReadAll(dec.Buffered())
	if err != nil {
		return NewErrorf(CodeParse, "reading past the message: %v", err)
	}

	if len(bytes.TrimSpace(rest)) > 0 {
		return NewError(CodeInvalidRequest, "the line carries more than one message")
	}

	return nil
}
