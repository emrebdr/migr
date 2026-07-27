package ir

// CreateView creates or replaces a view whose query is engine SQL and does not port.
type CreateView struct {
	View      string   `json:"view"`
	Query     string   `json:"query"`
	OrReplace bool     `json:"or_replace,omitempty"`
	From      *ViewDef `json:"from,omitempty"`
}

// Kind returns the operation's discriminator.
func (o CreateView) Kind() Kind { return KindCreateView }

// Destructive reports whether the operation can lose data.
func (o CreateView) Destructive() bool { return false }

// Reverse drops the view, or restores the definition an or_replace overwrote.
func (o CreateView) Reverse() (Operation, error) {
	if !o.OrReplace {
		return DropView{View: o.View}, nil
	}

	if o.From == nil {
		return nil, irreversible(KindCreateView,
			`or_replace may have overwritten an existing view whose definition is not recorded; `+
				`supply "from", or set or_replace to false when the view is new`)
	}

	return CreateView{
		View:      o.View,
		Query:     o.From.Query,
		OrReplace: true,
		From:      &ViewDef{Query: o.Query},
	}, nil
}

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

	if err := requireName("query", o.Query); err != nil {
		return err
	}

	return o.From.Validate()
}

// DropView removes a view.
type DropView struct {
	View string   `json:"view"`
	From *ViewDef `json:"from,omitempty"`
}

// Kind returns the operation's discriminator.
func (o DropView) Kind() Kind { return KindDropView }

// Destructive reports whether the operation can lose data.
func (o DropView) Destructive() bool { return true }

// Reverse recreates the view when its prior query was recorded.
func (o DropView) Reverse() (Operation, error) {
	if o.From == nil {
		return nil, missingFrom(KindDropView, "view query")
	}

	return CreateView{View: o.View, Query: o.From.Query}, nil
}

func (o DropView) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DropView) MarshalJSON() ([]byte, error) {
	type alias DropView

	return marshalOp(KindDropView, alias(o))
}

// Validate reports whether the operation names a view.
func (o DropView) Validate() error {
	if err := requireName("view", o.View); err != nil {
		return err
	}

	return o.From.Validate()
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

// Reverse always fails: the framework cannot know what the SQL did.
func (o Raw) Reverse() (Operation, error) {
	return nil, irreversible(KindRaw,
		"its effect is opaque to the framework; write the undo as a raw operation in Down")
}

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
