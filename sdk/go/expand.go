package migr

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// ErrInvalidReplacement marks a column replacement that cannot be carried out as it is declared.
var ErrInvalidReplacement = errors.New("this column replacement cannot be carried out")

// Replacement is one column replaced through expand, backfill, swap and contract, each its own migration.
type Replacement struct {
	// table is the table the column belongs to.
	table string
	// column is the column being replaced, named as it is before expand.
	column string
	// was is the column as it is before expand: its type, nullability and default.
	was Column
	// becomes is the column as it is after swap, its name aside.
	becomes Column
	// retyped reports whether With named a type, since the default is the type the column has.
	retyped bool
	// function is what the backfill applies to the old column to fill the new one.
	function TransformFunction
	// renamed is the name the column ends under in the rename form, empty in the keep-name form.
	renamed string
	// via is the name the new column carries until swap, in the keep-name form.
	via string
	// retired is the name the old column carries between swap and contract, in the keep-name form.
	retired string
}

// ReplaceColumn declares a change to one NOT NULL column of the given type, carried out in four phases.
func ReplaceColumn(table, column string, was Type) Replacement {
	return Replacement{
		table:    table,
		column:   column,
		was:      Column{Name: column, Type: was},
		becomes:  Column{Type: was},
		function: TransformCopy,
	}
}

// WasNull records that the column accepts NULL before expand, which ReplaceColumn assumes it does not.
func (r Replacement) WasNull() Replacement {
	r.was.Nullable = true

	return r
}

// WasDefault records the default the column carries before expand.
func (r Replacement) WasDefault(d *Default) Replacement {
	r.was.Default = d

	return r
}

// With names the type the column becomes, which is the type it has unless this says otherwise.
func (r Replacement) With(t Type) Replacement {
	r.becomes.Type = t
	r.retyped = true

	return r
}

// Null lets the replacing column accept NULL, which it does not by default.
func (r Replacement) Null() Replacement {
	r.becomes.Nullable = true

	return r
}

// Default gives the replacing column a default, from the moment expand adds it.
func (r Replacement) Default(d *Default) Replacement {
	r.becomes.Default = d

	return r
}

// Through names what the backfill applies to the old column, which is copy unless this says otherwise.
func (r Replacement) Through(f TransformFunction) Replacement {
	r.function = f

	return r
}

// RenameTo ends the column under a new name, so swap renames nothing and contract drops the old one.
func (r Replacement) RenameTo(name string) Replacement {
	r.renamed = name

	return r
}

// Via names the column the new values are built in until swap, which is <column>_next by default.
func (r Replacement) Via(name string) Replacement {
	r.via = name

	return r
}

// Retire names what the old column is called between swap and contract, <column>_old by default.
func (r Replacement) Retire(name string) Replacement {
	r.retired = name

	return r
}

// Expand is the migration that adds the replacing column, nullable whatever it ends as.
func (r Replacement) Expand() Migration {
	return expandPhase{r}
}

// Backfill is the migration that fills the replacing column from the old one, in chunks.
func (r Replacement) Backfill() Migration {
	return backfillPhase{r}
}

// Swap is the unsafe migration after which the replacing column is the one an app reads and writes.
func (r Replacement) Swap() Migration {
	return swapPhase{r}
}

// Contract is the unsafe migration that drops the old column, irreversible where the backfill lost data.
func (r Replacement) Contract() Migration {
	if r.function == TransformCopy {
		return contractPhase{r}
	}

	return lossyContractPhase{r}
}

// next is the name the replacing column carries from expand until swap.
func (r Replacement) next() string {
	switch {
	case r.renamed != "":
		return r.renamed
	case r.via != "":
		return r.via
	default:
		return r.column + "_next"
	}
}

// old is the name the replaced column carries from swap until contract drops it.
func (r Replacement) old() string {
	switch {
	case r.renamed != "":
		return r.column
	case r.retired != "":
		return r.retired
	default:
		return r.column + "_old"
	}
}

// final is the name the replacing column ends under.
func (r Replacement) final() string {
	if r.renamed != "" {
		return r.renamed
	}

	return r.column
}

// relaxes reports whether swap lets the old column accept NULL, which a NOT NULL one must.
func (r Replacement) relaxes() bool {
	return !r.was.Nullable
}

// tightens reports whether swap makes the replacing column refuse NULL, as it ends.
func (r Replacement) tightens() bool {
	return !r.becomes.Nullable
}

// staged is the replacing column as expand adds it: its final type and default, and nullable.
func (r Replacement) staged() Column {
	return Column{Name: r.next(), Type: r.becomes.Type, Nullable: true, Default: r.becomes.Default}
}

// retiredColumn is the old column as contract finds it: its own type and default, and nullable.
func (r Replacement) retiredColumn() Column {
	return Column{Name: r.old(), Type: r.was.Type, Nullable: true, Default: r.was.Default}
}

// Validate reports what makes this replacement impossible to carry out, reaching no database.
func (r Replacement) Validate() error {
	if err := r.checkNames(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidReplacement, err)
	}

	if err := r.checkColumns(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidReplacement, err)
	}

	return nil
}

// checkNames reports a blank name, a name used for two columns, or a form given both kinds of name.
func (r Replacement) checkNames() error {
	if err := requireName("table", r.table); err != nil {
		return err
	}

	if err := requireName("column", r.column); err != nil {
		return err
	}

	if r.renamed != "" && (r.via != "" || r.retired != "") {
		return fmt.Errorf("RenameTo(%q) ends the column under a name of its own, so Via and "+
			"Retire name nothing", r.renamed)
	}

	names := []string{r.column, r.next(), r.old()}
	if r.renamed == "" && slices.Contains(names[1:], r.column) {
		return fmt.Errorf("%q is the column being replaced, and cannot also name the column it "+
			"is built in or retired as", r.column)
	}

	if r.renamed != "" && r.renamed == r.column {
		return fmt.Errorf("RenameTo(%q) names the column it renames", r.renamed)
	}

	if r.next() == r.old() {
		return fmt.Errorf("%q cannot name both the new column and the retired one", r.next())
	}

	return nil
}

// checkColumns reports an unusable type, default or function, and a replacement that changes nothing.
func (r Replacement) checkColumns() error {
	if err := (Column{Name: r.column, Type: r.was.Type, Default: r.was.Default}).check(); err != nil {
		return fmt.Errorf("the column as it is: %w", err)
	}

	if err := r.staged().check(); err != nil {
		return fmt.Errorf("the column it becomes: %w", err)
	}

	if !slices.Contains(transformFunctions, r.function) {
		return fmt.Errorf("unknown function %q (expected one of: %s)", r.function,
			joinTransformFunctions())
	}

	if r.unchanged() {
		return fmt.Errorf("%q keeps its name, its type, its nullability and its values, so there "+
			"is nothing to replace -- name a type with With, a function with Through, or a name "+
			"with RenameTo", r.column)
	}

	return nil
}

// unchanged reports a replacement that would rebuild a column into exactly what it was.
func (r Replacement) unchanged() bool {
	return r.renamed == "" && r.function == TransformCopy &&
		(!r.retyped || r.becomes.Type.Equal(r.was.Type)) &&
		r.becomes.Nullable == r.was.Nullable && sameDefault(r.becomes.Default, r.was.Default)
}

// sameDefault reports whether two defaults are the same value, absent included.
func sameDefault(a, b *Default) bool {
	if a == nil || b == nil {
		return a == b
	}

	return a.Function == b.Function && (a.Literal == nil) == (b.Literal == nil) &&
		(a.Literal == nil || *a.Literal == *b.Literal)
}

// unsafe files the warning an unsafe step carries, in both modes, so a preview shows it too.
func (r Replacement) unsafe(s *Schema, step, consequence string) {
	s.Log(LevelWarn, "unsafe step: "+step+": "+consequence, map[string]string{
		"table": r.table, "column": r.column, "step": step,
	})
}

// The four steps, as each phase declares itself. Each waits on the one before it, a release apart.
const (
	stepExpand   = "expand"
	stepBackfill = "backfill"
	stepSwap     = "swap"
	stepContract = "contract"
)

// change names this replacement the way every phase of it does, by the table and the column it replaces.
func (r Replacement) change() string {
	return r.table + "." + r.column
}

// step declares one phase of this replacement, waiting on the phase before it.
func (r Replacement) step(name, after string) Phase {
	return Phase{Change: r.change(), Name: name, After: after}
}

// Phase declares the expand, which waits on nothing.
func (p expandPhase) Phase() Phase { return p.r.step(stepExpand, "") }

// Phase declares the backfill, which runs a release after the expand.
func (p backfillPhase) Phase() Phase { return p.r.step(stepBackfill, stepExpand) }

// Phase declares the swap, which runs a release after the backfill.
func (p swapPhase) Phase() Phase { return p.r.step(stepSwap, stepBackfill) }

// Phase declares the contract, which runs a release after the swap.
func (p contractPhase) Phase() Phase { return p.r.step(stepContract, stepSwap) }

// Phase declares the lossy contract, which runs a release after the swap as the other one does.
func (p lossyContractPhase) Phase() Phase { return p.r.step(stepContract, stepSwap) }

// quoted names a column of this replacement's table as an operator reads it.
func (r Replacement) quoted(column string) string {
	return fmt.Sprintf("%q.%q", r.table, column)
}

// expandPhase adds the replacing column.
type expandPhase struct{ r Replacement }

// Up adds the replacing column, nullable, so no app version writing the table is refused.
func (p expandPhase) Up(ctx context.Context, s *Schema) error {
	if err := p.r.Validate(); err != nil {
		return err
	}

	staged := p.r.staged()

	return s.Table(p.r.table).AddColumn(staged.Name, staged.Type).Null().Default(staged.Default).
		Apply(ctx)
}

// Down drops the replacing column, which is unsafe once an app version writes it.
func (p expandPhase) Down(ctx context.Context, s *Schema) error {
	if err := p.r.Validate(); err != nil {
		return err
	}

	p.r.unsafe(s, "expand down", fmt.Sprintf("drops %s, which an app version that writes both "+
		"columns still writes", p.r.quoted(p.r.next())))

	return s.Table(p.r.table).DropColumn(p.r.next()).Was(p.r.staged()).Apply(ctx)
}

// Validate reports what makes the replacement impossible to carry out.
func (p expandPhase) Validate() error { return p.r.Validate() }

// backfillPhase fills the replacing column from the old one.
type backfillPhase struct{ r Replacement }

// Up rewrites every row's replacing column from its old one, which runs in chunks on its own.
func (p backfillPhase) Up(ctx context.Context, s *Schema) error {
	if err := p.r.Validate(); err != nil {
		return err
	}

	return s.Table(p.r.table).Transform(p.r.next(), p.r.becomes.Type, p.r.column, p.r.function).
		Apply(ctx)
}

// Down undoes nothing, since expand's own down drops the column this filled.
func (p backfillPhase) Down(_ context.Context, _ *Schema) error {
	return p.r.Validate()
}

// Validate reports what makes the replacement impossible to carry out.
func (p backfillPhase) Validate() error { return p.r.Validate() }

// swapPhase makes the replacing column the one an app reads and writes.
type swapPhase struct{ r Replacement }

// Up relaxes the old column, tightens the new one, and in the keep-name form swaps their names.
func (p swapPhase) Up(ctx context.Context, s *Schema) error {
	if err := p.r.Validate(); err != nil {
		return err
	}

	p.r.unsafe(s, "swap", p.r.swapConsequence())

	table := s.Table(p.r.table)

	if p.r.relaxes() {
		table.AlterColumn(p.r.column).Null().WasNullable(false)
	}

	if p.r.tightens() {
		table.AlterColumn(p.r.next()).NotNull().WasNullable(true)
	}

	if p.r.renamed == "" {
		table.RenameColumn(p.r.column, p.r.old()).RenameColumn(p.r.next(), p.r.column)
	}

	return table.Apply(ctx)
}

// Down swaps the names back, then restores both columns' nullability, which is as unsafe as Up.
func (p swapPhase) Down(ctx context.Context, s *Schema) error {
	if err := p.r.Validate(); err != nil {
		return err
	}

	p.r.unsafe(s, "swap down", fmt.Sprintf("%s is the column an app reads again, so every app "+
		"version still serving must already read it", p.r.quoted(p.r.column)))

	table := s.Table(p.r.table)

	if p.r.renamed == "" {
		table.RenameColumn(p.r.column, p.r.next()).RenameColumn(p.r.old(), p.r.column)
	}

	if p.r.tightens() {
		table.AlterColumn(p.r.next()).Null().WasNullable(false)
	}

	if p.r.relaxes() {
		table.AlterColumn(p.r.column).NotNull().WasNullable(true)
	}

	return table.Apply(ctx)
}

// Validate reports what makes the replacement impossible to carry out.
func (p swapPhase) Validate() error { return p.r.Validate() }

// swapConsequence says what swap changes under an app version that is still serving.
func (r Replacement) swapConsequence() string {
	if r.renamed != "" {
		return fmt.Sprintf("%s stops being required and %s becomes the column an app writes, so "+
			"every app version still serving must already write it", r.quoted(r.column),
			r.quoted(r.renamed))
	}

	return fmt.Sprintf("%s now names the new column and %s the old one, so every app version "+
		"still serving must already read and write the new one", r.quoted(r.column),
		r.quoted(r.old()))
}

// contractPhase drops the old column.
type contractPhase struct{ r Replacement }

// Up drops the old column and the values in it, recording it as swap left it.
func (p contractPhase) Up(ctx context.Context, s *Schema) error {
	if err := p.r.Validate(); err != nil {
		return err
	}

	p.r.unsafe(s, "contract", fmt.Sprintf("drops %s and the values in it, so no app version still "+
		"serving may read it", p.r.quoted(p.r.old())))

	return s.Table(p.r.table).DropColumn(p.r.old()).Was(p.r.retiredColumn()).Apply(ctx)
}

// Validate reports what makes the replacement impossible to carry out.
func (p contractPhase) Validate() error { return p.r.Validate() }

// Down re-adds the old column and copies the values back, which a copy backfill makes exact.
func (p contractPhase) Down(ctx context.Context, s *Schema) error {
	if err := p.r.Validate(); err != nil {
		return err
	}

	retired := p.r.retiredColumn()

	return s.Table(p.r.table).
		AddColumn(retired.Name, retired.Type).Null().Default(retired.Default).
		Transform(retired.Name, retired.Type, p.r.final(), TransformCopy).
		Apply(ctx)
}

// lossyContractPhase drops an old column whose values the backfill did not carry over whole.
type lossyContractPhase struct{ r Replacement }

// Up drops the old column, as a copy's contract does.
func (p lossyContractPhase) Up(ctx context.Context, s *Schema) error {
	return contractPhase(p).Up(ctx, s)
}

// Validate reports what makes the replacement impossible to carry out.
func (p lossyContractPhase) Validate() error { return p.r.Validate() }

// Irreversible says why the old column's values cannot be put back.
func (p lossyContractPhase) Irreversible() string {
	return fmt.Sprintf("%s was filled through %s, which loses what the old column held, so the "+
		"values contract drops cannot be rebuilt from it", p.r.quoted(p.r.final()), p.r.function)
}

// compile-time proof that each phase is the migration shape Register checks for.
var (
	_ Downer       = expandPhase{}
	_ Downer       = backfillPhase{}
	_ Downer       = swapPhase{}
	_ Downer       = contractPhase{}
	_ Irreversible = lossyContractPhase{}
	_ Validator    = expandPhase{}
)
