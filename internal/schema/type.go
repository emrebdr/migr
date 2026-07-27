package schema

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Kind names one logical type. Drivers map each to a native type of their own.
type Kind string

// The logical type vocabulary. Four kinds take parameters; the rest stand alone.
const (
	KindString      Kind = "string"
	KindText        Kind = "text"
	KindBool        Kind = "bool"
	KindInt16       Kind = "int16"
	KindInt32       Kind = "int32"
	KindInt64       Kind = "int64"
	KindFloat32     Kind = "float32"
	KindFloat64     Kind = "float64"
	KindDecimal     Kind = "decimal"
	KindUUID        Kind = "uuid"
	KindJSON        Kind = "json"
	KindTimestamp   Kind = "timestamp"
	KindTimestampTZ Kind = "timestamptz"
	KindDate        Kind = "date"
	KindTime        Kind = "time"
	KindBinary      Kind = "binary"
	KindEnum        Kind = "enum"
	KindArray       Kind = "array"
)

// invalidType is what a zero Type renders as, so a stray debug print is obvious.
const invalidType = "<invalid>"

// maxArrayDepth bounds array nesting, because IR arrives over a pipe from a subprocess.
const maxArrayDepth = 32

// kinds lists every Kind in the order CLAUDE.md §4.1 declares them.
var kinds = []Kind{
	KindString, KindText, KindBool,
	KindInt16, KindInt32, KindInt64,
	KindFloat32, KindFloat64, KindDecimal,
	KindUUID, KindJSON,
	KindTimestamp, KindTimestampTZ, KindDate, KindTime,
	KindBinary, KindEnum, KindArray,
}

// parameterized lists the kinds that carry arguments in their canonical form.
var parameterized = []Kind{KindString, KindDecimal, KindEnum, KindArray}

// ErrInvalidType reports an operation attempted on a zero Type.
var ErrInvalidType = errors.New("zero Type is not a logical type")

// Kinds returns every logical kind, in declaration order.
func Kinds() []Kind {
	return slices.Clone(kinds)
}

// Valid reports whether a kind is one of the eighteen logical types.
func (k Kind) Valid() bool {
	return slices.Contains(kinds, k)
}

// Parameterized reports whether a kind takes arguments in its canonical form.
func (k Kind) Parameterized() bool {
	return slices.Contains(parameterized, k)
}

// Type is one logical type, immutable once built and always canonically spelled.
type Type struct {
	kind      Kind
	length    int
	precision int
	scale     int
	values    []string
	elem      *Type
}

// Scalar returns the parameterless type of the given kind.
func Scalar(k Kind) (Type, error) {
	if !k.Valid() {
		return Type{}, fmt.Errorf("unknown type %q (expected one of: %s)", string(k), joinKinds())
	}

	if k.Parameterized() {
		return Type{}, fmt.Errorf("%s takes parameters (use %s)", k, constructorFor(k))
	}

	return Type{kind: k}, nil
}

// String returns the variable-length string type of the given length.
func String(length int) (Type, error) {
	if length < 1 {
		return Type{}, fmt.Errorf("string length must be at least 1, got %d", length)
	}

	return Type{kind: KindString, length: length}, nil
}

// Decimal returns the fixed-point type of the given precision and scale.
func Decimal(precision, scale int) (Type, error) {
	if precision < 1 {
		return Type{}, fmt.Errorf("decimal precision must be at least 1, got %d", precision)
	}

	if scale < 0 {
		return Type{}, fmt.Errorf("decimal scale must not be negative, got %d", scale)
	}

	if scale > precision {
		return Type{}, fmt.Errorf("decimal scale %d exceeds precision %d", scale, precision)
	}

	return Type{kind: KindDecimal, precision: precision, scale: scale}, nil
}

// Enum returns the enumerated type over the given values, whose order is significant.
func Enum(values ...string) (Type, error) {
	if len(values) == 0 {
		return Type{}, errors.New("enum needs at least one value")
	}

	if err := checkEnumValues(values); err != nil {
		return Type{}, err
	}

	return Type{kind: KindEnum, values: slices.Clone(values)}, nil
}

// Array returns the array type over the given element type.
func Array(elem Type) (Type, error) {
	if elem.kind == "" {
		return Type{}, fmt.Errorf("array element: %w", ErrInvalidType)
	}

	if depth(elem)+1 > maxArrayDepth {
		return Type{}, fmt.Errorf("array nesting exceeds the limit of %d", maxArrayDepth)
	}

	return Type{kind: KindArray, elem: &elem}, nil
}

// checkEnumValues rejects empty, duplicated, and illegally spelled enum values.
func checkEnumValues(values []string) error {
	seen := make(map[string]bool, len(values))

	for i, v := range values {
		if v == "" {
			return fmt.Errorf("enum value %d is empty", i+1)
		}

		if bad := firstIllegalRune(v); bad != "" {
			return fmt.Errorf("enum value %q contains %s (allowed: letters, digits, _ and -)", v, bad)
		}

		if seen[v] {
			return fmt.Errorf("enum value %q appears more than once", v)
		}

		seen[v] = true
	}

	return nil
}

// firstIllegalRune describes the first disallowed character in an enum value.
func firstIllegalRune(v string) string {
	for _, r := range v {
		if !isEnumRune(r) {
			return strconv.QuoteRune(r)
		}
	}

	return ""
}

// isEnumRune reports whether a character may appear in an enum value.
func isEnumRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_' || r == '-':
		return true
	default:
		return false
	}
}

// depth returns how many array layers wrap a type.
func depth(t Type) int {
	if t.kind != KindArray || t.elem == nil {
		return 0
	}

	return depth(*t.elem) + 1
}

// Kind returns the type's kind.
func (t Type) Kind() Kind {
	return t.kind
}

// Length returns the declared length of a string type, and false for any other kind.
func (t Type) Length() (int, bool) {
	return t.length, t.kind == KindString
}

// Precision returns the declared precision of a decimal type, and false otherwise.
func (t Type) Precision() (int, bool) {
	return t.precision, t.kind == KindDecimal
}

// Scale returns the declared scale of a decimal type, and false otherwise.
func (t Type) Scale() (int, bool) {
	return t.scale, t.kind == KindDecimal
}

// Values returns a copy of an enum type's values, and false for any other kind.
func (t Type) Values() ([]string, bool) {
	return slices.Clone(t.values), t.kind == KindEnum
}

// Elem returns the element type of an array, and false for any other kind.
func (t Type) Elem() (Type, bool) {
	if t.kind != KindArray || t.elem == nil {
		return Type{}, false
	}

	return *t.elem, true
}

// Zero reports whether the type was never built by a constructor.
func (t Type) Zero() bool {
	return t.kind == ""
}

// String renders the type in its canonical form, the one and only spelling.
func (t Type) String() string {
	switch t.kind {
	case "":
		return invalidType
	case KindString:
		return fmt.Sprintf("%s(%d)", KindString, t.length)
	case KindDecimal:
		return fmt.Sprintf("%s(%d,%d)", KindDecimal, t.precision, t.scale)
	case KindEnum:
		return fmt.Sprintf("%s(%s)", KindEnum, strings.Join(t.values, ","))
	case KindArray:
		if t.elem == nil {
			return invalidType
		}

		return fmt.Sprintf("%s(%s)", KindArray, t.elem.String())
	case KindText, KindBool, KindInt16, KindInt32, KindInt64, KindFloat32, KindFloat64,
		KindUUID, KindJSON, KindTimestamp, KindTimestampTZ, KindDate, KindTime, KindBinary:
		return string(t.kind)
	default:
		return invalidType
	}
}

// Equal reports whether two types are the same logical type.
func (t Type) Equal(other Type) bool {
	return t.String() == other.String()
}

// MarshalText renders the type as its canonical string, for JSON and YAML alike.
func (t Type) MarshalText() ([]byte, error) {
	if t.Zero() {
		return nil, ErrInvalidType
	}

	return []byte(t.String()), nil
}

// UnmarshalText parses a canonical type string.
func (t *Type) UnmarshalText(text []byte) error {
	parsed, err := Parse(string(text))
	if err != nil {
		return err
	}

	*t = parsed

	return nil
}

// joinKinds renders the full kind vocabulary for an error message.
func joinKinds() string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}

	return strings.Join(names, ", ")
}

// constructorFor names the constructor that builds a parameterized kind.
func constructorFor(k Kind) string {
	switch k {
	case KindString:
		return "String(length)"
	case KindDecimal:
		return "Decimal(precision, scale)"
	case KindEnum:
		return "Enum(values...)"
	case KindArray:
		return "Array(elem)"
	case KindText, KindBool, KindInt16, KindInt32, KindInt64, KindFloat32, KindFloat64,
		KindUUID, KindJSON, KindTimestamp, KindTimestampTZ, KindDate, KindTime, KindBinary:
		return "Scalar"
	default:
		return "Scalar"
	}
}
