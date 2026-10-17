package migr

import "context"

// View is a change to a view, built up and then applied in one call.
type View struct {
	build *builder
	name  string
}

// Apply emits everything built against this view, and returns the first problem found.
func (v *View) Apply(ctx context.Context) error {
	return v.build.apply(ctx)
}

// Name returns the view these changes act on.
func (v *View) Name() string {
	return v.name
}

// Create defines the view, whose query is engine SQL and does not port.
func (v *View) Create(query string) *View {
	v.build.add(&CreateView{View: v.name, Query: query})

	return v
}

// Replace overwrites an existing view, which Was makes reversible.
func (v *View) Replace(query string) *ViewRecord {
	op := &CreateView{View: v.name, Query: query, OrReplace: true}
	v.build.add(op)

	return &ViewRecord{View: v, record: func(d *ViewDef) { op.From = d }}
}

// Drop removes the view, which Was makes reversible.
func (v *View) Drop() *ViewRecord {
	op := &DropView{View: v.name}
	v.build.add(op)

	return &ViewRecord{View: v, record: func(d *ViewDef) { op.From = d }}
}

// ViewRecord is a view change that may record the query it replaced.
type ViewRecord struct {
	*View
	record func(*ViewDef)
}

// Was records the query the view was defined by; without it the change cannot be undone.
func (r *ViewRecord) Was(query string) *View {
	r.record(&ViewDef{Query: query})

	return r.View
}

// Statements are raw statements addressed to one engine by name, the escape hatch the IR cannot express.
type Statements struct {
	build *builder
	// last is the statement most recently added, which NoTransaction marks.
	last *Raw
}

// Apply emits every statement built here, and returns the first problem found.
func (s *Statements) Apply(ctx context.Context) error {
	return s.build.apply(ctx)
}

// Raw adds one more statement, in the named engine's own dialect.
func (s *Statements) Raw(driver, sql string) *Statements {
	s.last = &Raw{Driver: driver, SQL: sql}
	s.build.add(s.last)

	return s
}

// NoTransaction declares that the statement just added cannot run inside a transaction.
func (s *Statements) NoTransaction() *Statements {
	if s.last != nil {
		s.last.NoTransaction = true
	}

	return s
}
