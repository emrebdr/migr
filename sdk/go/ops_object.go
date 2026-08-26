package migr

// CreateView creates or replaces a view whose query is engine SQL and does not port.
type CreateView struct {
	// View is the name of the view to create.
	View string `json:"view"`
	// Query is the view's own SQL, in the target engine's dialect.
	Query string `json:"query"`
	// OrReplace overwrites an existing view, which needs From to be undoable.
	OrReplace bool `json:"or_replace,omitempty"`
	// From records the definition an OrReplace overwrote.
	From *ViewDef `json:"from,omitempty"`
}

// Kind names this operation, which is create_view.
func (o CreateView) Kind() Kind { return KindCreateView }

// MarshalJSON renders the operation with its discriminator first.
func (o CreateView) MarshalJSON() ([]byte, error) {
	type alias CreateView

	return marshalOp(KindCreateView, alias(o))
}

// check reports a view that names nothing, or carries no query.
func (o CreateView) check() error {
	if err := requireName("view", o.View); err != nil {
		return err
	}

	if err := requireName("query", o.Query); err != nil {
		return err
	}

	return o.From.check()
}

// DropView removes a view.
type DropView struct {
	// View is the name of the view to remove.
	View string `json:"view"`
	// From records the query it was defined by, without which the drop cannot be undone.
	From *ViewDef `json:"from,omitempty"`
}

// Kind names this operation, which is drop_view.
func (o DropView) Kind() Kind { return KindDropView }

// MarshalJSON renders the operation with its discriminator first.
func (o DropView) MarshalJSON() ([]byte, error) {
	type alias DropView

	return marshalOp(KindDropView, alias(o))
}

// check reports a drop that names no view.
func (o DropView) check() error {
	if err := requireName("view", o.View); err != nil {
		return err
	}

	return o.From.check()
}

// Raw is the driver-scoped escape hatch: SQL run verbatim against the engine it names.
type Raw struct {
	// Driver is the engine this SQL is written for, which no other engine will run.
	Driver string `json:"driver"`
	// SQL is the statement itself, in that engine's own dialect.
	SQL string `json:"sql"`
}

// Kind names this operation, which is raw.
func (o Raw) Kind() Kind { return KindRaw }

// MarshalJSON renders the operation with its discriminator first.
func (o Raw) MarshalJSON() ([]byte, error) {
	type alias Raw

	return marshalOp(KindRaw, alias(o))
}

// check reports raw SQL naming no engine, or naming one and carrying nothing to run.
func (o Raw) check() error {
	if err := requireName("driver", o.Driver); err != nil {
		return err
	}

	return requireName("sql", o.SQL)
}

// compile-time proof that every object operation satisfies the sealed interface.
var (
	_ Operation = CreateView{}
	_ Operation = DropView{}
	_ Operation = Raw{}
)
