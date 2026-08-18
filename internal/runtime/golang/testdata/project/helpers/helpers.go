// Package helpers is a package the migrations import from outside their own directory.
//
// It exists to pin the stamp's one documented hole: this file is not in the digest, so
// editing it does not force a rebuild. A test asserts exactly that, so widening the stamp
// fails a test rather than quietly making the documentation wrong.
package helpers

// Suffix is appended to what the fixture migration registers, so an edit here is observable.
const Suffix = ""
