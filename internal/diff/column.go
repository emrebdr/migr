package diff

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// A matched column is judged attribute by attribute, so a column whose type the catalog cannot pin
// down can still have its nullability changed. What is emitted states the column's whole expressible
// state except its type, and the three are treated differently for three measured reasons:
//
//	nullable   only when it changes, because restating an unchanged NOT NULL costs PostgreSQL a
//	           full scan to re-validate, which is invariant #12's concern rather than tidiness
//	default    whenever the target column has one, changed or not, because a driver that changes a
//	           column by restating its whole definition drops every attribute it was not told about
//	           (ADR 0058) -- stated as a property of a generator that knows both states, not as a
//	           branch on an engine
//	type       only when it changes, because ir.AlterColumn.Destructive() keys on its presence, so
//	           restating an unchanged one would make every column change need permitting

// alterColumn emits what one matched column differs by, and refuses the attributes it cannot answer.
func (d *differ) alterColumn(table string, before, after schema.Column) {
	subject := qualified(table, after.Name)

	if !before.Beyond.Equal(after.Beyond) {
		d.refuse(ir.KindAlterColumn, subject, describeMarkerChange(before, after))

		return
	}

	op := ir.AlterColumn{Table: table, Column: after.Name, From: &ir.ColumnState{}}

	changed := d.stateChanges(&op, subject, before, after)
	if !changed {
		return
	}

	if marked := markedBeyond(after.Beyond); marked != "" {
		d.refuse(ir.KindAlterColumn, subject, marked)

		return
	}

	stateDefault(&op, before, after)
	d.emit(op)
}

// stateChanges sets what differs about a column on the operation, and reports whether anything does.
func (d *differ) stateChanges(op *ir.AlterColumn, subject string,
	before, after schema.Column,
) bool {
	changed := false

	retype, problem := typeChange(before, after)

	switch {
	case problem != "":
		d.refuse(ir.KindAlterColumn, subject, problem)
	case retype:
		stateType(op, before, after)

		changed = true
	}

	if before.Nullable != after.Nullable {
		stateNullable(op, before, after)

		changed = true
	}

	moved, problem := defaultChange(before, after)

	switch {
	case problem != "":
		d.refuse(ir.KindAlterColumn, subject, problem)
	case moved:
		changed = true
	}

	return changed
}

// A type is compared on the logical answer, which is the only part of it anything can act on. Two
// guesses that agree propose nothing, since the best answer available is that the column is
// unchanged; two that disagree are refused, because the operation would carry a target type nothing
// verified and record a prior type that is itself a guess -- ADR 0008's trust problem, written by
// migr rather than by an author. Creating is not changing: a new column is emitted off the logical
// answer however certain it is, there being nothing to compare it against and no data at risk.

// typeChange reports whether a column is being retyped, and what stops that question being answered.
func typeChange(before, after schema.Column) (bool, string) {
	previous, wasExact := before.Type.Logical()
	current, nowExact := after.Type.Logical()

	if previous.Zero() || current.Zero() {
		if before.Type.Equal(after.Type) {
			return false, ""
		}

		return false, fmt.Sprintf("its type is %s before and %s after, and one of those names no "+
			"logical type at all, so nothing can say what to change it to", before.Type, after.Type)
	}

	if previous.Equal(current) {
		return false, ""
	}

	if !wasExact || !nowExact {
		return false, fmt.Sprintf("its type is %s before and %s after, and the catalog cannot tell "+
			"those apart, so changing it would be a rewrite proposed from a guess",
			before.Type, after.Type)
	}

	return true, ""
}

// stateType sets the type the column is becoming, and records the one it is.
func stateType(op *ir.AlterColumn, before, after schema.Column) {
	previous, _ := before.Type.Logical()
	current, _ := after.Type.Logical()

	op.Type, op.From.Type = &current, &previous
}

// stateNullable sets what the column is to accept, and records what it accepts now.
func stateNullable(op *ir.AlterColumn, before, after schema.Column) {
	previous, current := before.Nullable, after.Nullable

	op.Nullable, op.From.Nullable = &current, &previous
}

// defaultChange reports whether a column's default is moving, and what stops it being expressed.
func defaultChange(before, after schema.Column) (bool, string) {
	if before.Default.Equal(after.Default) {
		return false, ""
	}

	if engineText(before.Default) || engineText(after.Default) {
		return false, fmt.Sprintf("its default is %s before and %s after, and one of those is "+
			"engine SQL, which an operation's default never carries",
			describeDefault(before.Default), describeDefault(after.Default))
	}

	return true, ""
}

// stateDefault sets the default the operation carries, which is the new one or the one it restates.
func stateDefault(op *ir.AlterColumn, before, after schema.Column) {
	if engineText(before.Default) || engineText(after.Default) {
		return
	}

	if after.Default != nil {
		stateNewDefault(op, before.Default, after.Default)

		return
	}

	if before.Default != nil {
		op.DropDefault = true
		op.From.Default = irDefault(before.Default)
	}
}

// stateNewDefault sets the default the column is to have, and records the one it has now.
func stateNewDefault(op *ir.AlterColumn, before, after *schema.ColumnDefault) {
	op.Default = irDefault(after)

	if before == nil {
		op.From.NoDefault = true

		return
	}

	op.From.Default = irDefault(before)
}

// engineText reports whether a default is the engine's own SQL, which the operations never carry.
func engineText(value *schema.ColumnDefault) bool {
	return value != nil && strings.TrimSpace(value.Expression) != ""
}

// describeDefault renders a default the way a refusal names one, an absent one included.
func describeDefault(value *schema.ColumnDefault) string {
	if value == nil {
		return "none"
	}

	return value.String()
}

// describeMarkerChange says which side of a matched column carries what no operation can express.
func describeMarkerChange(before, after schema.Column) string {
	switch {
	case before.Beyond == nil:
		return "the target schema records something of it no operation can express: " +
			after.Beyond.Detail
	case after.Beyond == nil:
		return "this schema records something of it no operation can express and the target does " +
			"not: " + before.Beyond.Detail
	default:
		return fmt.Sprintf("the two schemas record different things of it no operation can "+
			"express: %s, and %s", before.Beyond.Detail, after.Beyond.Detail)
	}
}
