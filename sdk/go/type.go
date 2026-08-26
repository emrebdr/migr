package migr

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// TypeKind names one logical type, which each driver maps to a native type of its own.
type TypeKind string

// The eighteen logical types docs/ir.md §5.2 defines. Four take parameters; the rest stand alone.
const (
	TypeString      TypeKind = "string"
	TypeText        TypeKind = "text"
	TypeBool        TypeKind = "bool"
	TypeInt16       TypeKind = "int16"
	TypeInt32       TypeKind = "int32"
	TypeInt64       TypeKind = "int64"
	TypeFloat32     TypeKind = "float32"
	TypeFloat64     TypeKind = "float64"
	TypeDecimal     TypeKind = "decimal"
	TypeUUID        TypeKind = "uuid"
	TypeJSON        TypeKind = "json"
	TypeTimestamp   TypeKind = "timestamp"
	TypeTimestampTZ TypeKind = "timestamptz"
	TypeDate        TypeKind = "date"
	TypeTime        TypeKind = "time"
	TypeBinary      TypeKind = "binary"
	TypeEnum        TypeKind = "enum"
	TypeArray       TypeKind = "array"
)

// invalidType is what a type with no canonical spelling renders as, so a stray print is obvious.
const invalidType = "<invalid>"

// maxArrayDepth bounds array nesting, which §5.4 sets so corrupt input errors instead of recursing.
const maxArrayDepth = 32

// typeKinds lists every logical type in the order docs/ir.md §5.2 declares them.
var typeKinds = []TypeKind{
	TypeString, TypeText, TypeBool,
	TypeInt16, TypeInt32, TypeInt64,
	TypeFloat32, TypeFloat64, TypeDecimal,
	TypeUUID, TypeJSON,
	TypeTimestamp, TypeTimestampTZ, TypeDate, TypeTime,
	TypeBinary, TypeEnum, TypeArray,
}

// parameterized lists the four kinds that carry arguments in their canonical form.
var parameterized = []TypeKind{TypeString, TypeDecimal, TypeEnum, TypeArray}

// ErrInvalidType marks a field that was never given a logical type.
var ErrInvalidType = errors.New("no logical type was given")

// TypeKinds returns every logical type, in declaration order.
func TypeKinds() []TypeKind {
	return slices.Clone(typeKinds)
}

// Valid reports whether a kind is one of the eighteen logical types.
func (k TypeKind) Valid() bool {
	return slices.Contains(typeKinds, k)
}

// Parameterized reports whether a kind takes arguments in its canonical form.
func (k TypeKind) Parameterized() bool {
	return slices.Contains(parameterized, k)
}

// String returns the kind's wire name.
func (k TypeKind) String() string {
	return string(k)
}

// Type is one logical type, always canonically spelled, or the reason it has no spelling.
type Type struct {
	kind      TypeKind
	length    int
	precision int
	scale     int
	values    []string
	elem      *Type
	err       error
}

// Scalar returns the parameterless type of the given kind.
func Scalar(k TypeKind) Type {
	if !k.Valid() {
		return badType(fmt.Errorf("unknown type %q (expected one of: %s)", string(k), joinTypeKinds()))
	}

	if k.Parameterized() {
		return badType(fmt.Errorf("%s takes parameters (use %s)", k, constructorFor(k)))
	}

	return Type{kind: k}
}

// String returns the variable-length string type of the given maximum length.
func String(length int) Type {
	if length < 1 {
		return badType(fmt.Errorf("string length must be at least 1, got %d", length))
	}

	return Type{kind: TypeString, length: length}
}

// Decimal returns the exact numeric type of the given precision and scale.
func Decimal(precision, scale int) Type {
	if precision < 1 {
		return badType(fmt.Errorf("decimal precision must be at least 1, got %d", precision))
	}

	if scale < 0 {
		return badType(fmt.Errorf("decimal scale must not be negative, got %d", scale))
	}

	if scale > precision {
		return badType(fmt.Errorf("decimal scale %d exceeds precision %d", scale, precision))
	}

	return Type{kind: TypeDecimal, precision: precision, scale: scale}
}

// Enum returns the enumerated type over the given labels, whose order is part of the type.
func Enum(values ...string) Type {
	if len(values) == 0 {
		return badType(errors.New("enum needs at least one value"))
	}

	if err := checkEnumValues(values); err != nil {
		return badType(err)
	}

	return Type{kind: TypeEnum, values: slices.Clone(values)}
}

// Array returns the list type over the given element type.
func Array(elem Type) Type {
	if err := elem.Err(); err != nil {
		return badType(fmt.Errorf("array element: %w", err))
	}

	if depth(elem)+1 > maxArrayDepth {
		return badType(fmt.Errorf("array nesting exceeds the limit of %d", maxArrayDepth))
	}

	return Type{kind: TypeArray, elem: &elem}
}

// Text returns the unbounded text type.
func Text() Type { return Scalar(TypeText) }

// Bool returns the boolean type.
func Bool() Type { return Scalar(TypeBool) }

// Int16 returns the 16-bit signed integer type.
func Int16() Type { return Scalar(TypeInt16) }

// Int32 returns the 32-bit signed integer type.
func Int32() Type { return Scalar(TypeInt32) }

// Int64 returns the 64-bit signed integer type.
func Int64() Type { return Scalar(TypeInt64) }

// Float32 returns the single-precision floating point type.
func Float32() Type { return Scalar(TypeFloat32) }

// Float64 returns the double-precision floating point type.
func Float64() Type { return Scalar(TypeFloat64) }

// UUID returns the universally unique identifier type.
func UUID() Type { return Scalar(TypeUUID) }

// JSON returns the JSON document type.
func JSON() Type { return Scalar(TypeJSON) }

// Timestamp returns the date and time type that carries no zone.
func Timestamp() Type { return Scalar(TypeTimestamp) }

// TimestampTZ returns the date and time type that carries a zone.
func TimestampTZ() Type { return Scalar(TypeTimestampTZ) }

// Date returns the calendar date type.
func Date() Type { return Scalar(TypeDate) }

// Time returns the time of day type.
func Time() Type { return Scalar(TypeTime) }

// Binary returns the byte string type.
func Binary() Type { return Scalar(TypeBinary) }

// Kind returns the type's kind, and the empty kind for one with no canonical spelling.
func (t Type) Kind() TypeKind {
	return t.kind
}

// Length returns the declared length of a string type, and false for any other kind.
func (t Type) Length() (int, bool) {
	return t.length, t.kind == TypeString
}

// Precision returns the declared precision of a decimal type, and false for any other kind.
func (t Type) Precision() (int, bool) {
	return t.precision, t.kind == TypeDecimal
}

// Scale returns the declared scale of a decimal type, and false for any other kind.
func (t Type) Scale() (int, bool) {
	return t.scale, t.kind == TypeDecimal
}

// Values returns a copy of an enum type's labels, and false for any other kind.
func (t Type) Values() ([]string, bool) {
	return slices.Clone(t.values), t.kind == TypeEnum
}

// Elem returns the element type of an array, and false for any other kind.
func (t Type) Elem() (Type, bool) {
	if t.kind != TypeArray || t.elem == nil {
		return Type{}, false
	}

	return *t.elem, true
}

// Zero reports whether no type was given at all, as opposed to one that could not be built.
func (t Type) Zero() bool {
	return t.kind == "" && t.err == nil
}

// Err reports why this type has no canonical spelling, and nil when it has one.
func (t Type) Err() error {
	if t.err != nil {
		return t.err
	}

	if t.kind == "" {
		return ErrInvalidType
	}

	return nil
}

// Equal reports whether two types are the same logical type.
func (t Type) Equal(other Type) bool {
	return t.Err() == nil && other.Err() == nil && t.String() == other.String()
}

// String renders the type in the one spelling §5.3 permits, with no spaces.
func (t Type) String() string {
	switch t.kind {
	case "":
		return invalidType
	case TypeString:
		return fmt.Sprintf("%s(%d)", TypeString, t.length)
	case TypeDecimal:
		return fmt.Sprintf("%s(%d,%d)", TypeDecimal, t.precision, t.scale)
	case TypeEnum:
		return fmt.Sprintf("%s(%s)", TypeEnum, strings.Join(t.values, ","))
	case TypeArray:
		if t.elem == nil {
			return invalidType
		}

		return fmt.Sprintf("%s(%s)", TypeArray, t.elem.String())
	default:
		return string(t.kind)
	}
}

// MarshalText renders the type as its canonical string, which is how the IR carries one.
func (t Type) MarshalText() ([]byte, error) {
	if err := t.Err(); err != nil {
		return nil, err
	}

	return []byte(t.String()), nil
}

// UnmarshalText parses a canonical type string, which is how the IR carries one.
func (t *Type) UnmarshalText(text []byte) error {
	parsed := ParseType(string(text))
	if err := parsed.Err(); err != nil {
		return err
	}

	*t = parsed

	return nil
}

// badType returns the type that has no canonical spelling, carrying the reason it has none.
func badType(err error) Type {
	return Type{err: err}
}

// depth returns how many array layers wrap a type.
func depth(t Type) int {
	if t.kind != TypeArray || t.elem == nil {
		return 0
	}

	return depth(*t.elem) + 1
}

// checkEnumValues rejects empty, duplicated, and illegally spelled enum labels.
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

// firstIllegalRune describes the first disallowed character in an enum label.
func firstIllegalRune(v string) string {
	for _, r := range v {
		if !isEnumRune(r) {
			return strconv.QuoteRune(r)
		}
	}

	return ""
}

// isEnumRune reports whether a character may appear in an enum label.
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

// joinTypeKinds renders the whole logical type vocabulary for an error message.
func joinTypeKinds() string {
	names := make([]string, len(typeKinds))
	for i, k := range typeKinds {
		names[i] = string(k)
	}

	return strings.Join(names, ", ")
}

// constructorFor names the constructor that builds a parameterized kind.
func constructorFor(k TypeKind) string {
	switch k {
	case TypeString:
		return "String(length)"
	case TypeDecimal:
		return "Decimal(precision, scale)"
	case TypeEnum:
		return "Enum(values...)"
	case TypeArray:
		return "Array(elem)"
	default:
		return "Scalar"
	}
}
