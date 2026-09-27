package migr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
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

// The two runes encoding/json escapes whatever it is told, which docs/ir.md §3.3 emits as UTF-8.
const (
	lineSeparator      = rune(0x2028)
	paragraphSeparator = rune(0x2029)
)

// separatorEscape is the five bytes both of those escapes begin with, as encoding/json writes them.
var separatorEscape = []byte{'\\', 'u', '2', '0', '2'}

// marshal encodes IR escaping only what docs/ir.md §3.3 does, which every producer owes.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding IR: %w", err)
	}

	return unescapeSeparators(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// unescapeSeparators rewrites the two escapes encoding/json emits however it is configured.
func unescapeSeparators(encoded []byte) []byte {
	if !bytes.Contains(encoded, separatorEscape) {
		return encoded
	}

	out := make([]byte, 0, len(encoded))
	inString := false

	for i := 0; i < len(encoded); {
		if !inString {
			inString = encoded[i] == '"'
			out = append(out, encoded[i])
			i++

			continue
		}

		if separator, ok := separatorAt(encoded, i); ok {
			out = utf8.AppendRune(out, separator)
			i += len(separatorEscape) + 1

			continue
		}

		if encoded[i] == '\\' {
			out = append(out, encoded[i:min(i+2, len(encoded))]...)
			i += 2

			continue
		}

		inString = encoded[i] != '"'
		out = append(out, encoded[i])
		i++
	}

	return out
}

// separatorAt reports which separator an escape beginning at this offset names, if one does.
func separatorAt(encoded []byte, at int) (rune, bool) {
	rest := encoded[at:]

	if len(rest) <= len(separatorEscape) || !bytes.HasPrefix(rest, separatorEscape) {
		return 0, false
	}

	switch rest[len(separatorEscape)] {
	case '8':
		return lineSeparator, true
	case '9':
		return paragraphSeparator, true
	default:
		return 0, false
	}
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
