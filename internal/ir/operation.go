package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// Marshal encodes IR as JSON without HTML escaping; use it instead of encoding/json.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding IR: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
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
