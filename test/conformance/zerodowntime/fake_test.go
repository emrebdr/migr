package zerodowntime

import "context"

// fakeLoad is a Load that sends nothing, for the cases' own shape.
type fakeLoad struct{}

// Seed does nothing.
func (fakeLoad) Seed(context.Context, string, int) error { return nil }

// Read does nothing.
func (fakeLoad) Read(context.Context, string, int64) error { return nil }

// Write does nothing.
func (fakeLoad) Write(context.Context, string, int64) error { return nil }

// View is a body no engine would run.
func (fakeLoad) View(string) string { return "a view" }

// Raw is a statement no engine would run.
func (fakeLoad) Raw() string { return "a statement" }

// Drop does nothing.
func (fakeLoad) Drop(context.Context, ...string) error { return nil }

// Gone reports nothing as gone.
func (fakeLoad) Gone(error) bool { return false }

// Sum is nothing.
func (fakeLoad) Sum(context.Context, string) (int64, error) { return 0, nil }
