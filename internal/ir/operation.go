package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// Operation is one schema change, sealed so the operation set stays closed.
type Operation interface {
	Kind() Kind
	Destructive() bool
	Reverse() (Operation, error)
	Validate() error
	isOperation()
}

// Operations is an ordered list of operations, decodable from a JSON array.
type Operations []Operation

// MarshalJSON renders the list as a JSON array of discriminated operations.
func (ops Operations) MarshalJSON() ([]byte, error) {
	items := make([]json.RawMessage, len(ops))

	for i, op := range ops {
		encoded, err := Marshal(op)
		if err != nil {
			return nil, fmt.Errorf("operation %d (%s): %w", i, op.Kind(), err)
		}

		items[i] = encoded
	}

	return Marshal(items)
}

// UnmarshalJSON decodes a JSON array into concrete operations, in order.
func (ops *Operations) UnmarshalJSON(data []byte) error {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("operation list: %w", err)
	}

	decoded := make(Operations, len(items))

	for i, item := range items {
		op, err := UnmarshalOperation(item)
		if err != nil {
			return fmt.Errorf("operation %d: %w", i, err)
		}

		decoded[i] = op
	}

	*ops = decoded

	return nil
}

// discriminator is the one field every operation document is guaranteed to carry.
type discriminator struct {
	Op Kind `json:"op"`
}

// The two runes encoding/json escapes whatever it is told, which docs/ir.md §3.3 emits as UTF-8.
const (
	lineSeparator      = rune(0x2028)
	paragraphSeparator = rune(0x2029)
)

// separatorEscape is the five bytes both of those escapes begin with, as encoding/json writes them.
var separatorEscape = []byte{'\\', 'u', '2', '0', '2'}

// Marshal encodes IR as JSON escaping only what docs/ir.md §3.3 does; use it, not encoding/json.
func Marshal(v any) ([]byte, error) {
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

// Unmarshal decodes an IR document, rejecting any field its target does not define.
func Unmarshal(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("decoding IR: %w", err)
	}

	return nil
}

// UnmarshalOperation decodes one operation document into its concrete type.
func UnmarshalOperation(data []byte) (Operation, error) {
	var d discriminator
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("reading the op discriminator: %w", err)
	}

	if d.Op == "" {
		return nil, fmt.Errorf(`operation has no "op" field (expected one of: %s)`, joinKinds())
	}

	if !d.Op.Valid() {
		return nil, fmt.Errorf("unknown operation %q (expected one of: %s)", string(d.Op), joinKinds())
	}

	op, err := decodeKind(d.Op, data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.Op, err)
	}

	return op, nil
}

// decodeKind routes a document to the concrete type its discriminator names.
func decodeKind(k Kind, data []byte) (Operation, error) {
	switch k {
	case KindCreateTable:
		return decodeOp[CreateTable](data)
	case KindDropTable:
		return decodeOp[DropTable](data)
	case KindRenameTable:
		return decodeOp[RenameTable](data)
	case KindAddColumn:
		return decodeOp[AddColumn](data)
	case KindDropColumn:
		return decodeOp[DropColumn](data)
	case KindRenameColumn:
		return decodeOp[RenameColumn](data)
	case KindAlterColumn:
		return decodeOp[AlterColumn](data)
	case KindAddIndex:
		return decodeOp[AddIndex](data)
	case KindDropIndex:
		return decodeOp[DropIndex](data)
	case KindAddForeignKey:
		return decodeOp[AddForeignKey](data)
	case KindDropForeignKey:
		return decodeOp[DropForeignKey](data)
	case KindAddUnique:
		return decodeOp[AddUnique](data)
	case KindDropUnique:
		return decodeOp[DropUnique](data)
	case KindAddCheck:
		return decodeOp[AddCheck](data)
	case KindDropCheck:
		return decodeOp[DropCheck](data)
	case KindAddPrimaryKey:
		return decodeOp[AddPrimaryKey](data)
	case KindDropPrimaryKey:
		return decodeOp[DropPrimaryKey](data)
	case KindCreateView:
		return decodeOp[CreateView](data)
	case KindDropView:
		return decodeOp[DropView](data)
	case KindRaw:
		return decodeOp[Raw](data)
	default:
		return nil, fmt.Errorf("unknown operation (expected one of: %s)", joinKinds())
	}
}

// decodeOp reads a document into one operation type and checks it is well formed.
func decodeOp[T Operation](data []byte) (Operation, error) {
	var op T

	if err := strictDecode(data, &op); err != nil {
		return nil, err
	}

	if err := op.Validate(); err != nil {
		return nil, err
	}

	return op, nil
}

// strictDecode reads a document into target, dropping the discriminator no type declares.
func strictDecode(data []byte, target any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("reading fields: %w", err)
	}

	delete(fields, "op")

	body, err := Marshal(fields)
	if err != nil {
		return fmt.Errorf("re-encoding fields: %w", err)
	}

	return Unmarshal(body, target)
}

// marshalOp renders an operation with its discriminator first, ahead of the type's fields.
func marshalOp(kind Kind, fields any) ([]byte, error) {
	body, err := Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kind, err)
	}

	head, err := Marshal(discriminator{Op: kind})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kind, err)
	}

	return splice(head, body), nil
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
