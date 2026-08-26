package migr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Operation is one schema change a migration emits, which the engine compiles for its driver.
type Operation interface {
	// Kind names the operation, which is the one member every IR document carries.
	Kind() Kind
	// check reports an operation the engine would refuse, before it is sent.
	check() error
}

// ErrInvalidOperation marks an operation the engine would refuse as an IR document.
var ErrInvalidOperation = errors.New("invalid operation")

// documentOf checks one operation and renders the IR document that crosses the wire verbatim.
func documentOf(op Operation) (json.RawMessage, error) {
	if op == nil {
		return nil, errors.New("there is nothing here")
	}

	if err := op.check(); err != nil {
		return nil, err
	}

	document, err := marshal(op)
	if err != nil {
		return nil, err
	}

	return document, nil
}

// labelOf names the operation an error is about, when there is an operation to name.
func labelOf(op Operation) string {
	if op == nil {
		return ""
	}

	return " (" + op.Kind().String() + ")"
}

// marshal encodes IR without HTML escaping, which docs/ir.md §3.3 requires of every producer.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding IR: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// marshalOp renders an operation with its discriminator first, ahead of the type's own fields.
func marshalOp(kind Kind, fields any) ([]byte, error) {
	body, err := marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kind, err)
	}

	head, err := marshal(discriminator{Op: kind})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kind, err)
	}

	return splice(head, body), nil
}

// discriminator is the one member every operation document is guaranteed to carry.
type discriminator struct {
	Op Kind `json:"op"`
}

// splice joins two JSON objects, keeping the first object's keys ahead of the second's.
func splice(head, body []byte) []byte {
	if len(body) <= 2 {
		return head
	}

	joined := make([]byte, 0, len(head)+len(body))
	joined = append(joined, head[:len(head)-1]...)
	joined = append(joined, ',')
	joined = append(joined, body[1:]...)

	return joined
}
