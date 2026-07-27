package testdata

import "os"

// Kind carries a go directive in its doc group, in the position gofmt puts it.
//
//go:generate stringer -type=Kind
type Kind int

// Deprecated marks itself deprecated across several lines.
//
// Deprecated: use Kind instead. This explanation wraps onto a second line, which is
// allowed because everything from the marker onward stops counting.
type Deprecated int

// ReadFile carries a nolint directive inside its body.
func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // the path is operator-supplied by design
}

// Directive carries a go directive inside its body.
func Directive() int {
	//go:noinline
	return 1
}
