// Package testdata holds fixtures for the comment-rule check.
//
// This package doc is deliberately several lines long. Package docs are exactly where
// multi-line context belongs, so the check must never report one.
//
// The go tool ignores directories named testdata, so nothing here is built, vetted, or
// linted by anything except the check's own tests.
package testdata

// Clean is a type with a one-line doc.
type Clean struct {
	// Name is a field with a one-line doc.
	Name string

	Untouched int
}

// Method is a method with a one-line doc.
func (c Clean) Method() string {
	if c.Name == "" {
		return "anonymous"
	}

	return c.Name
}

// Contract is an interface whose method docs are one line each.
type Contract interface {
	// Do performs the thing.
	Do() error
}

// literal exercises a package-level function literal with no comment inside it.
var literal = func() int {
	return 1
}
