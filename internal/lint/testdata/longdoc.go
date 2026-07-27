package testdata

// LongFunc does a thing.
// It also does a second thing, which is the problem.
func LongFunc() {}

// LongType describes something.
// The second line is what makes this a violation.
type LongType struct {
	// LongField holds a value.
	// This second line is a violation too.
	LongField string
}

// LongGrouped is declared inside a group.
type (
	// Grouped has a doc attached to the spec rather than the declaration.
	// The second line still counts.
	Grouped int
)

// LongInterface is fine on its own.
type LongInterface interface {
	// Do performs the thing.
	// This second line is a violation on an interface method.
	Do() error
}

/*
LongBlock uses a block comment spanning
several lines.
*/
func LongBlock() {}

// LongSeparated has a paragraph break.
//
// That blank line does not rescue it, because two prose lines remain.
func LongSeparated() {}
