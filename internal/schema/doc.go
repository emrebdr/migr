// Package schema defines the logical type system and the schema snapshot model.
//
// Migrations name logical types and never native ones; each driver maps them to its
// own. Snapshots are driver-neutral and comparable, so an equivalent schema on
// PostgreSQL and MySQL compares equal despite differing native names.
//
// Status: types complete (M1-T1); snapshot model in M11-T1.
//
// # Grammar
//
//	type := ident [ "(" arg { "," arg } ")" ]
//
// Fourteen kinds stand alone — text, bool, int16, int32, int64, float32, float64,
// uuid, json, timestamp, timestamptz, date, time, binary. Four take parameters:
// string(n), decimal(p,s), enum(values...), and array(of), whose argument is itself
// a full type.
//
// Enum values are letters, digits, underscore, and hyphen, and their order is part of
// the type: PostgreSQL compares enum labels by declaration order, so enum(a,b) and
// enum(b,a) are different types.
//
// # The canonical string is the wire format
//
// A Type marshals to exactly the string it parses from, so the IR carries
// "decimal(10,2)" rather than a nested object. One representation means golden files
// stay readable, plan output needs no translation, and there is a single code path
// that can be wrong. See docs/adr/0005-logical-type-encoding.md.
//
// That places real weight on uniqueness: every type has exactly one spelling. Two
// SDKs emitting different bytes for the same schema would break the byte-identical-IR
// conformance claim that makes language independence testable, so parsing normalizes
// — whitespace around parameters is trimmed — while String always emits the tight
// form. Keywords are lowercase; an uppercase name is rejected with the canonical
// spelling as a hint rather than quietly accepted as a second way to say the same
// thing.
//
// # Invalid states are unrepresentable
//
// Type's fields are unexported and there are no setters, so every value comes from a
// constructor that has already validated it. A zero Type is not a type: it renders as
// <invalid> and refuses to marshal, so it cannot reach an IR document as an empty
// string. Because Type holds a slice, == does not compile; Equal compares canonical
// strings, which is exact precisely because that form is unique.
//
// # What is deliberately not checked here
//
// Upper bounds. PostgreSQL caps decimal precision at 1000 and MySQL at 65; varchar
// limits differ again. Those are capability questions, and CLAUDE.md puts engine
// differences in the capability model rather than in a type system that has to hold
// for every engine at once. Likewise array(array(int32)) parses: whether an engine
// supports it is for that engine's driver to declare.
//
// Array nesting is bounded only to keep hostile input from recursing away — IR
// arrives over a pipe from a subprocess, so a corrupt array(array(array(...))) must
// produce an error rather than a stack overflow.
package schema
