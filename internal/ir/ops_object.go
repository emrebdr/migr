package ir

// CreateView creates or replaces a view whose query is engine SQL and does not port.
type CreateView struct {
	View      string `json:"view"`
	Query     string `json:"query"`
	OrReplace bool   `json:"or_replace,omitempty"`
}

// Kind returns the operation's discriminator.
func (o CreateView) Kind() Kind { return KindCreateView }

// Destructive reports whether the operation can lose data.
func (o CreateView) Destructive() bool { return false }

func (o CreateView) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o CreateView) MarshalJSON() ([]byte, error) {
	type alias CreateView

	return marshalOp(KindCreateView, alias(o))
}

// Validate reports whether the operation names a view and carries a query.
func (o CreateView) Validate() error {
	if err := requireName("view", o.View); err != nil {
		return err
	}

	return requireName("query", o.Query)
}

// DropView removes a view.
type DropView struct {
	View string `json:"view"`
}

// Kind returns the operation's discriminator.
func (o DropView) Kind() Kind { return KindDropView }

// Destructive reports whether the operation can lose data.
func (o DropView) Destructive() bool { return true }

func (o DropView) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DropView) MarshalJSON() ([]byte, error) {
	type alias DropView

	return marshalOp(KindDropView, alias(o))
}

// Validate reports whether the operation names a view.
func (o DropView) Validate() error {
	return requireName("view", o.View)
}

// Raw is the driver-scoped escape hatch: SQL run verbatim against the engine it names.
type Raw struct {
	Driver string `json:"driver"`
	SQL    string `json:"sql"`
}

// Kind returns the operation's discriminator.
func (o Raw) Kind() Kind { return KindRaw }

// Destructive reports whether the operation can lose data.
func (o Raw) Destructive() bool { return true }

func (o Raw) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o Raw) MarshalJSON() ([]byte, error) {
	type alias Raw

	return marshalOp(KindRaw, alias(o))
}

// Validate reports whether the operation names its driver and carries SQL.
func (o Raw) Validate() error {
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
