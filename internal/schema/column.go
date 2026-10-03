package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Certainty is how well an engine's own spelling identified a column's logical type.
type Certainty string

// The three answers introspection can honestly give about one column's type.
const (
	// CertaintyExact names one logical type and no other: PostgreSQL's integer is int32.
	CertaintyExact Certainty = "exact"
	// CertaintyAmbiguous names the best of several: MySQL's datetime(6) is timestamp or timestamptz.
	CertaintyAmbiguous Certainty = "ambiguous"
	// CertaintyUnknown names none at all: PostgreSQL's tsvector is no logical type migr has.
	CertaintyUnknown Certainty = "unknown"
)

// ErrNoColumnType reports an operation attempted on a ColumnType no constructor built.
var ErrNoColumnType = errors.New("zero ColumnType is not a column type")

// ColumnType is a column's type as a snapshot can know it, which is not always one logical type.
type ColumnType struct {
	logical   Type
	native    string
	certainty Certainty
}

// ExactType returns the type of a column whose native spelling names one logical type and no other.
func ExactType(t Type, native string) (ColumnType, error) {
	return builtType(CertaintyExact, t, native)
}

// AmbiguousType returns the type of a column whose native spelling could be several logical types.
func AmbiguousType(best Type, native string) (ColumnType, error) {
	return builtType(CertaintyAmbiguous, best, native)
}

// UnknownType returns the type of a column whose native spelling names no logical type at all.
func UnknownType(native string) (ColumnType, error) {
	if strings.TrimSpace(native) == "" {
		return ColumnType{}, errors.New("a column type names no native spelling")
	}

	return ColumnType{native: native, certainty: CertaintyUnknown}, nil
}

// builtType returns a column type of the given certainty, refusing one that cannot mean anything.
func builtType(certainty Certainty, t Type, native string) (ColumnType, error) {
	if strings.TrimSpace(native) == "" {
		return ColumnType{}, errors.New("a column type names no native spelling")
	}

	if t.Zero() {
		return ColumnType{}, fmt.Errorf("an %s column type carries no logical type: %w",
			certainty, ErrInvalidType)
	}

	return ColumnType{logical: t, native: native, certainty: certainty}, nil
}

// Certainty reports how well this engine's own spelling identified the logical type.
func (c ColumnType) Certainty() Certainty {
	return c.certainty
}

// Exact reports whether the native spelling names one logical type and no other.
func (c ColumnType) Exact() bool {
	return c.certainty == CertaintyExact
}

// Logical returns the logical type recorded, and whether that answer is exact rather than a guess.
func (c ColumnType) Logical() (Type, bool) {
	return c.logical, c.certainty == CertaintyExact
}

// Native returns the engine's own spelling, which is what every message about this column names.
func (c ColumnType) Native() string {
	return c.native
}

// Zero reports whether the column type was never built by a constructor.
func (c ColumnType) Zero() bool {
	return c.certainty == "" && c.native == "" && c.logical.Zero()
}

// Validate reports whether the column type is one a constructor could have produced.
func (c ColumnType) Validate() error {
	if c.Zero() {
		return ErrNoColumnType
	}

	if strings.TrimSpace(c.native) == "" {
		return errors.New("a column type names no native spelling")
	}

	switch c.certainty {
	case CertaintyExact, CertaintyAmbiguous:
		if c.logical.Zero() {
			return fmt.Errorf("an %s column type carries no logical type: %w",
				c.certainty, ErrInvalidType)
		}

		return nil
	case CertaintyUnknown:
		if !c.logical.Zero() {
			return fmt.Errorf("an unknown column type carries the logical type %s, which is a "+
				"claim it cannot make", c.logical)
		}

		return nil
	default:
		return fmt.Errorf("unknown certainty %q (expected one of: %s, %s, %s)",
			string(c.certainty), CertaintyExact, CertaintyAmbiguous, CertaintyUnknown)
	}
}

// String renders the type as the logical answer, the guess, or the engine's spelling alone.
func (c ColumnType) String() string {
	switch c.certainty {
	case CertaintyExact:
		return c.logical.String()
	case CertaintyAmbiguous:
		return fmt.Sprintf("%s? (%s)", c.logical, c.native)
	case CertaintyUnknown:
		return fmt.Sprintf("unknown (%s)", c.native)
	default:
		return invalidType
	}
}

// Equal reports whether two column types are knowably the same type.
func (c ColumnType) Equal(other ColumnType) bool {
	if c.Exact() && other.Exact() {
		return c.logical.Equal(other.logical)
	}

	return c.certainty == other.certainty &&
		c.logical.Equal(other.logical) &&
		c.native == other.native
}

// columnTypeDocument is the JSON shape of a column type, whose own fields are unexported.
type columnTypeDocument struct {
	Certainty Certainty `json:"certainty"`
	Logical   *Type     `json:"logical,omitempty"`
	Native    string    `json:"native"`
}

// MarshalJSON renders the certainty, the logical type where there is one, and the native spelling.
func (c ColumnType) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("encoding a column type: %w", err)
	}

	document := columnTypeDocument{Certainty: c.certainty, Native: c.native}
	if !c.logical.Zero() {
		document.Logical = &c.logical
	}

	return json.Marshal(document)
}

// UnmarshalJSON decodes a column type, refusing a member this build does not know.
func (c *ColumnType) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var document columnTypeDocument

	if err := dec.Decode(&document); err != nil {
		return fmt.Errorf("decoding a column type: %w", err)
	}

	built := ColumnType{certainty: document.Certainty, native: document.Native}
	if document.Logical != nil {
		built.logical = *document.Logical
	}

	if err := built.Validate(); err != nil {
		return fmt.Errorf("decoding a column type: %w", err)
	}

	*c = built

	return nil
}

// ColumnDefault is what a column defaults to: a literal, a neutral function, or engine expression.
type ColumnDefault struct {
	// Literal is the default value as text, which is empty-but-present for an empty string.
	Literal *string `json:"literal,omitempty"`
	// Function is one of the four neutral functions, where the engine's own default is one of them.
	Function Function `json:"function,omitempty"`
	// Expression is the engine's own text, for a default this vocabulary cannot name.
	Expression string `json:"expression,omitempty"`
}

// Validate reports whether the default names exactly one source of its value.
func (d *ColumnDefault) Validate() error {
	if d == nil {
		return nil
	}

	if named := d.named(); named != 1 {
		return fmt.Errorf("a default must set exactly one of literal, function and expression, "+
			"and this one sets %d", named)
	}

	if d.Function != "" && !d.Function.Valid() {
		return fmt.Errorf("unknown default function %q (expected one of: %s)",
			string(d.Function), joinFunctions())
	}

	return nil
}

// named counts how many of the three sources of a default value this one carries.
func (d *ColumnDefault) named() int {
	var found int

	if d.Literal != nil {
		found++
	}

	if d.Function != "" {
		found++
	}

	if strings.TrimSpace(d.Expression) != "" {
		found++
	}

	return found
}

// Equal reports whether two defaults produce the same value, an absent one included.
func (d *ColumnDefault) Equal(other *ColumnDefault) bool {
	if d == nil || other == nil {
		return d == nil && other == nil
	}

	if (d.Literal == nil) != (other.Literal == nil) {
		return false
	}

	if d.Literal != nil && *d.Literal != *other.Literal {
		return false
	}

	return d.Function == other.Function && d.Expression == other.Expression
}

// String renders the default the way a plan or a diff names it.
func (d *ColumnDefault) String() string {
	switch {
	case d == nil:
		return ""
	case d.Literal != nil:
		return fmt.Sprintf("%q", *d.Literal)
	case d.Function != "":
		return string(d.Function)
	default:
		return d.Expression
	}
}

// Column is one column of a table, named with a logical type rather than a native one.
type Column struct {
	// Name is the column's own name, as the engine holds it.
	Name string `json:"name"`
	// Type is what the engine said the column is, and how certainly that names a logical type.
	Type ColumnType `json:"type"`
	// Nullable reports whether the column accepts null, so an unqualified one is NOT NULL.
	Nullable bool `json:"nullable"`
	// Default is what the column defaults to, absent where it defaults to nothing.
	Default *ColumnDefault `json:"default,omitempty"`
	// Beyond marks a column the v1 operation set cannot express, generated and identity ones alike.
	Beyond *Unrepresented `json:"beyond,omitempty"`
}

// Validate reports whether the column names itself and carries a usable type.
func (c Column) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("a column has no name")
	}

	if err := c.Type.Validate(); err != nil {
		return fmt.Errorf("column %q: %w", c.Name, err)
	}

	if err := c.Default.Validate(); err != nil {
		return fmt.Errorf("column %q: %w", c.Name, err)
	}

	if err := c.Beyond.Validate(); err != nil {
		return fmt.Errorf("column %q: %w", c.Name, err)
	}

	return nil
}

// Equal reports whether two columns are the same column, which includes what neither can express.
func (c Column) Equal(other Column) bool {
	return c.Name == other.Name &&
		c.Type.Equal(other.Type) &&
		c.Nullable == other.Nullable &&
		c.Default.Equal(other.Default) &&
		c.Beyond.Equal(other.Beyond)
}

// joinFunctions renders the default-function vocabulary for an error message.
func joinFunctions() string {
	names := make([]string, len(functions))
	for i, f := range functions {
		names[i] = string(f)
	}

	return strings.Join(names, ", ")
}
