package testdata

// Between has a comment sitting between two statements.
func Between() int {
	n := 1

	// this explains the next line
	n++

	return n
}

// Trailing has a comment on the end of a statement line.
func Trailing() int {
	n := 1 // start at one

	return n
}

// Nested has a comment inside a closure inside a function body.
func Nested() func() int {
	return func() int {
		// inside the literal
		return 2
	}
}

// Composite has a comment inside a composite literal.
func Composite() []string {
	return []string{
		// the first element
		"a",
		"b",
	}
}

// Block has a block comment inside the body.
func Block() int {
	/* explaining */
	return 3
}

// Declaration has a doc comment on a declaration inside the body.
func Declaration() int {
	// limit caps the loop
	const limit = 4

	return limit
}

// packageLevelLiteral has a comment inside a function literal at package level.
var packageLevelLiteral = func() int {
	// not a loophole
	return 5
}
