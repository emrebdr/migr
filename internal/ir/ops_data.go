package ir

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// TransformFunction names what data_transform applies to its source column, which every driver spells.
type TransformFunction string

// The transform functions, a closed set: a fifth is a format change (docs/ir.md §10.2).
const (
	TransformCopy  TransformFunction = "copy"
	TransformLower TransformFunction = "lower"
	TransformUpper TransformFunction = "upper"
	TransformTrim  TransformFunction = "trim"
)

// transformFunctions lists every accepted transform function, in the order docs/ir.md states them.
var transformFunctions = []TransformFunction{TransformCopy, TransformLower, TransformUpper,
	TransformTrim}

// TransformFunctions returns every accepted transform function, as a copy.
func TransformFunctions() []TransformFunction {
	return slices.Clone(transformFunctions)
}

// Valid reports whether a transform function is one of the accepted set.
func (f TransformFunction) Valid() bool {
	return slices.Contains(transformFunctions, f)
}

// CopyColumn is one column data_copy reads from its source table and the column it writes it into.
type CopyColumn struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// DataCopy inserts every row of one table into another, column by named column.
type DataCopy struct {
	From    string       `json:"from"`
	To      string       `json:"to"`
	Columns []CopyColumn `json:"columns"`
}

// Kind returns the operation's discriminator.
func (o DataCopy) Kind() Kind { return KindDataCopy }

// Destructive reports whether the operation can lose data, which an insert cannot.
func (o DataCopy) Destructive() bool { return false }

// Reverse always fails: the rows it inserted cannot be told from the rows already there.
func (o DataCopy) Reverse() (Operation, error) {
	return nil, irreversible(KindDataCopy, "the rows it inserted cannot be told from the rows "+
		"the table already held; author a Down, which is usually the inverse of whatever created "+
		"the table it copied into")
}

func (o DataCopy) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DataCopy) MarshalJSON() ([]byte, error) {
	type alias DataCopy

	return marshalOp(KindDataCopy, alias(o))
}

// Validate reports whether the operation names two tables and each column pair once.
func (o DataCopy) Validate() error {
	if err := requireName("from", o.From); err != nil {
		return err
	}

	if err := requireName("to", o.To); err != nil {
		return err
	}

	if o.From == o.To {
		return fmt.Errorf("from and to both name %q, and a table copied into itself duplicates "+
			"every row", o.From)
	}

	return validateCopyColumns(o.Columns)
}

// validateCopyColumns reports whether every pair names both columns and no target is written twice.
func validateCopyColumns(columns []CopyColumn) error {
	if len(columns) == 0 {
		return errors.New("columns must list at least one column to copy")
	}

	written := make(map[string]bool, len(columns))

	for i, c := range columns {
		if strings.TrimSpace(c.From) == "" || strings.TrimSpace(c.To) == "" {
			return fmt.Errorf("columns[%d] must name both the column it reads and the one it writes",
				i)
		}

		if written[c.To] {
			return fmt.Errorf("columns[%d] writes %q, which an earlier pair already writes", i, c.To)
		}

		written[c.To] = true
	}

	return nil
}

// DataBackfill fills the NULLs of one column with a literal or a neutral function, and nothing else.
type DataBackfill struct {
	Table  string      `json:"table"`
	Column string      `json:"column"`
	Type   schema.Type `json:"type"`
	Value  *Default    `json:"value"`
}

// Kind returns the operation's discriminator.
func (o DataBackfill) Kind() Kind { return KindDataBackfill }

// Destructive reports whether the operation can lose data, which filling only NULLs cannot.
func (o DataBackfill) Destructive() bool { return false }

// Reverse always fails: which rows were NULL before it ran is recorded nowhere.
func (o DataBackfill) Reverse() (Operation, error) {
	return nil, irreversible(KindDataBackfill, "which rows were NULL before it ran is recorded "+
		"nowhere; author a Down, which is usually the inverse of the add_column it filled")
}

func (o DataBackfill) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DataBackfill) MarshalJSON() ([]byte, error) {
	type alias DataBackfill

	return marshalOp(KindDataBackfill, alias(o))
}

// Validate reports whether the operation names its column, that column's type, and one value.
func (o DataBackfill) Validate() error {
	if err := requireTarget(o.Table, o.Column, o.Type); err != nil {
		return err
	}

	if o.Value == nil {
		return errors.New("value is required: a backfill writes a literal or a neutral function")
	}

	if err := o.Value.Validate(); err != nil {
		return fmt.Errorf("value: %w", err)
	}

	return nil
}

// DataTransform rewrites one column of every row from another column, through a neutral function.
type DataTransform struct {
	Table    string            `json:"table"`
	Column   string            `json:"column"`
	Type     schema.Type       `json:"type"`
	From     string            `json:"from"`
	Function TransformFunction `json:"function"`
}

// Kind returns the operation's discriminator.
func (o DataTransform) Kind() Kind { return KindDataTransform }

// Destructive reports whether the operation can lose data, which overwriting every row can.
func (o DataTransform) Destructive() bool { return true }

// Reverse always fails: the values it overwrote are recorded nowhere.
func (o DataTransform) Reverse() (Operation, error) {
	return nil, irreversible(KindDataTransform, "the values it overwrote are recorded nowhere; "+
		"author a Down, which is usually the inverse of the add_column it wrote into")
}

func (o DataTransform) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DataTransform) MarshalJSON() ([]byte, error) {
	type alias DataTransform

	return marshalOp(KindDataTransform, alias(o))
}

// Validate reports whether the operation names both columns, the target's type, and a known function.
func (o DataTransform) Validate() error {
	if err := requireTarget(o.Table, o.Column, o.Type); err != nil {
		return err
	}

	if err := requireName("from", o.From); err != nil {
		return err
	}

	if !o.Function.Valid() {
		return fmt.Errorf("unknown function %q (expected one of: %s)", o.Function,
			joinTransformFunctions())
	}

	if o.Function == TransformCopy && o.From == o.Column {
		return fmt.Errorf("copies %q into itself, which changes nothing", o.Column)
	}

	return nil
}

// requireTarget reports whether a write names its table, its column, and the column's logical type.
func requireTarget(table, column string, t schema.Type) error {
	if err := requireName("table", table); err != nil {
		return err
	}

	if err := requireName("column", column); err != nil {
		return err
	}

	if t.Zero() {
		return fmt.Errorf("type is required: it is the type %q holds, which every driver renders "+
			"the value in", column)
	}

	return nil
}

// joinTransformFunctions renders the transform-function set for an error message.
func joinTransformFunctions() string {
	names := make([]string, len(transformFunctions))
	for i, f := range transformFunctions {
		names[i] = string(f)
	}

	return strings.Join(names, ", ")
}

// compile-time proof that every data operation satisfies the sealed interface.
var (
	_ Operation = DataCopy{}
	_ Operation = DataBackfill{}
	_ Operation = DataTransform{}
)
