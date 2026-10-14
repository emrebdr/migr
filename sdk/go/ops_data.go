package migr

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// TransformFunction names what DataTransform applies to its source column, spelled as docs/ir.md spells it.
type TransformFunction string

// The transform functions, a closed set: lower, upper and trim apply to the source's text.
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

// CopyColumn is one column DataCopy reads from its source table and the column it writes it into.
type CopyColumn struct {
	// From is the column read.
	From string `json:"from"`
	// To is the column written.
	To string `json:"to"`
}

// DataCopy inserts every row of one table into another, column by named column.
type DataCopy struct {
	// From is the table read.
	From string `json:"from"`
	// To is the table written, which must differ from From.
	To string `json:"to"`
	// Columns pairs each column read with the one it is written into.
	Columns []CopyColumn `json:"columns"`
}

// Kind names this operation, which is data_copy.
func (o DataCopy) Kind() Kind { return KindDataCopy }

// MarshalJSON renders the operation with its discriminator first.
func (o DataCopy) MarshalJSON() ([]byte, error) {
	type alias DataCopy

	return marshalOp(KindDataCopy, alias(o))
}

// check reports a copy naming no table, copying a table into itself, or writing a column twice.
func (o DataCopy) check() error {
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

	return checkCopyColumns(o.Columns)
}

// checkCopyColumns reports a pair naming one side only, or a target written twice.
func checkCopyColumns(columns []CopyColumn) error {
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
	// Table is the table written.
	Table string `json:"table"`
	// Column is the column whose NULLs are filled.
	Column string `json:"column"`
	// Type is that column's logical type, which the value is rendered in.
	Type Type `json:"type"`
	// Value is what fills them: a literal or one of the neutral functions.
	Value *Default `json:"value"`
}

// Kind names this operation, which is data_backfill.
func (o DataBackfill) Kind() Kind { return KindDataBackfill }

// MarshalJSON renders the operation with its discriminator first.
func (o DataBackfill) MarshalJSON() ([]byte, error) {
	type alias DataBackfill

	return marshalOp(KindDataBackfill, alias(o))
}

// check reports a backfill naming no column, no type, or no value.
func (o DataBackfill) check() error {
	if err := checkTarget(o.Table, o.Column, o.Type); err != nil {
		return err
	}

	if o.Value == nil {
		return errors.New("value is required: a backfill writes a literal or a neutral function")
	}

	if err := o.Value.check(); err != nil {
		return fmt.Errorf("value: %w", err)
	}

	return nil
}

// DataTransform rewrites one column of every row from another column, through a neutral function.
type DataTransform struct {
	// Table is the table written.
	Table string `json:"table"`
	// Column is the column written.
	Column string `json:"column"`
	// Type is that column's logical type, which the result is converted to.
	Type Type `json:"type"`
	// From is the column read.
	From string `json:"from"`
	// Function is what is applied to it.
	Function TransformFunction `json:"function"`
}

// Kind names this operation, which is data_transform.
func (o DataTransform) Kind() Kind { return KindDataTransform }

// MarshalJSON renders the operation with its discriminator first.
func (o DataTransform) MarshalJSON() ([]byte, error) {
	type alias DataTransform

	return marshalOp(KindDataTransform, alias(o))
}

// check reports a transform naming no columns, no type, an unknown function, or a copy into itself.
func (o DataTransform) check() error {
	if err := checkTarget(o.Table, o.Column, o.Type); err != nil {
		return err
	}

	if err := requireName("from", o.From); err != nil {
		return err
	}

	if !slices.Contains(transformFunctions, o.Function) {
		return fmt.Errorf("unknown function %q (expected one of: %s)", o.Function,
			joinTransformFunctions())
	}

	if o.Function == TransformCopy && o.From == o.Column {
		return fmt.Errorf("copies %q into itself, which changes nothing", o.Column)
	}

	return nil
}

// checkTarget reports a write naming no table, no column, or no usable type for that column.
func checkTarget(table, column string, t Type) error {
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

	return t.Err()
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
