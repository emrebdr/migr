package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Error is a JSON-RPC error object, which is also a Go error.
type Error struct {
	Code    Code            `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// slugged reads the one member docs/protocol.md §7.1 requires of every error's data.
type slugged struct {
	Slug string `json:"slug"`
}

// NewError builds an error carrying the code's own slug, which is what an SDK branches on.
func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message, Data: slugOf(code)}
}

// NewErrorf builds an error whose message is formatted.
func NewErrorf(code Code, format string, args ...any) *Error {
	return NewError(code, fmt.Sprintf(format, args...))
}

// Error renders the failure with the slug beside it, per CLAUDE.md invariant #9.
func (e *Error) Error() string {
	if slug := e.Slug(); slug != "" {
		return fmt.Sprintf("%s (%s)", e.Message, slug)
	}

	return e.Message
}

// Slug returns what data.slug names, or empty when the sender supplied none.
func (e *Error) Slug() string {
	var data slugged

	if err := json.Unmarshal(e.Data, &data); err != nil {
		return ""
	}

	return data.Slug
}

// Validate reports whether an error carries the slug §7.1 requires of a sender.
func (e *Error) Validate() error {
	if e.Slug() == "" {
		return fmt.Errorf("error %d (%s) carries no data.slug, which docs/protocol.md §7.1 requires",
			int(e.Code), e.Message)
	}

	return nil
}

// slugOf renders the data member a code's own slug fills, which every error carries.
func slugOf(code Code) json.RawMessage {
	data, err := json.Marshal(slugged{Slug: code.String()})
	if err != nil {
		return nil
	}

	return data
}

// CodeOf reports the JSON-RPC code a failure carries, and whether it carries one at all.
func CodeOf(err error) (Code, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}

	return 0, false
}
