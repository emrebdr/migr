// Package runtime is the conformance suite every language SDK must pass unmodified.
//
// A language's whole conformance run is one line, because the suite supplies the fixtures and
// offers no way to opt out of any of them:
//
//	conformance.Suite{Runtime: adapter, Project: project}.Run(t)
//
// Status: complete. Go passes it (M6-T9) and TypeScript passes it (M7-T7), which is the point at
// which "identical IR from two languages" stopped being a claim.
//
// # What this proves that nothing below it did
//
// internal/ir and sdk/go each hold their structs to docs/ir.md, so the two copies agree on every
// documented vector. internal/runtime/host proves a migration's callbacks produce identical IR in
// either mode -- with a Go closure playing the SDK. internal/runtime/golang generates a runner,
// builds it, starts it, and completes a handshake, and stops at migration.discover.
//
// So until this package, nothing had ever read what a migration emits off the wire from a real
// source. That is the framework's central claim, and this is the thing that measures it.
//
// # A fixture is a manifest, and a language expresses it
//
// testdata/fixtures holds one JSON file per canonical migration: the operations every SDK must
// emit for each direction, and, where the migration reads or writes, exactly what it is told.
// testdata/sources/<language> holds that language's expression of the same fixture, under the same
// filename stem -- which is also how one fixture carries one version across languages, since
// Register derives a migration's version from its filename (ADR 0037, B).
//
// The manifest is the contract and neither language is. A fixture's own bytes are held to being
// exactly what ir.Marshal emits, so a definition no SDK could match fails at load rather than
// failing every language.
//
// # The reads are pinned, which is what lets a fixture branch
//
// A migration may branch on a live read, and two languages given different rows would emit
// different IR for reasons that say nothing about either SDK. So the manifest states what each
// read returns, and the suite answers them in order while checking the statement it was actually
// sent -- a language that reads different SQL fails naming both. One fixture branches on the
// pinned answer, which is the strongest form of "identical IR from one source" available.
//
// # Both directions, both modes, and what only the mode changes
//
// Every fixture is asked four questions. The operations must be identical across modes, which is
// what internal/runtime/host proved host-side and this proves from a source; it also forbids a
// fixture from branching on Result.Executed, which is the one thing that legitimately differs.
//
// The escape hatch is the difference. Under mode plan, Emitted.Direct carries the statements the
// migration sent itself and the database saw none of them; under mode apply, Direct is empty and
// the database saw exactly those statements, in order. That is ADR 0032 and ADR 0036 end to end
// from a real SDK.
//
// # No exemptions, structurally
//
// Suite has two exported fields. There is no case list, no options, and no skip hook, so "no
// per-language exemptions" is a shape rather than a rule someone has to remember. Coverage is
// derived from ir.Kinds(), so an operation no fixture emits fails the suite -- and so does the
// twenty-first operation, on the day it is added. The fixture count in M6-T9's done-when is written
// down nowhere.
//
// There is one unexported field, and it is a test seam rather than an exemption: this package's own
// tests replace the fixture set so that the suite's judgments can be exercised. They have to be. In
// a repository where the language already agrees with every fixture, an assertion that only fires
// on a disagreement is never reached, and a mutation sweep says so loudly -- twenty of them survived
// the first pass here for exactly that reason. judge and matchDiscovered are therefore pure
// functions over one answer, called directly by judge_test.go with answers that are wrong.
//
// # It takes a prepared project, not a language
//
// Assembling the sources into something a runtime can start is that language's own problem: a
// go.mod and a package.json are not the same shape. Sources hands a caller its own expressions and
// the caller writes them where its adapter expects them. That is also why the fixture tree carries
// no go.mod -- //go:embed skips any directory holding one, and a module manifest is a Go fact in a
// tree that belongs to no language.
//
// # Where a language's run lives
//
// Not here. depguard permits only cmd/migr to name a concrete runtime, so each adapter runs the
// suite from its own package -- internal/runtime/golang/conformance_test.go and
// internal/runtime/node/conformance_test.go -- exactly as M5-T2 put the end-to-end CLI suite in
// cmd/migr for the same reason. This package must therefore never import
// internal/runtime/<language>.
//
// Assembling the sources is each language's own problem and the two look nothing alike: Go writes a
// go.mod with a replace onto sdk/go and has the adapter build a runner; TypeScript writes a
// package.json and links node_modules/@migr/sdk, and the runtime compiles each migration as it
// imports it. That difference is why Sources hands over the files rather than a project.
//
// # No build tag here, and one on the TypeScript run
//
// The suite needs no database, and the Go run needs no toolchain make test does not already have:
// internal/runtime/golang builds with go build, and the Go toolchain is present by definition when
// go test is what is running (M6-T6). A //go:build conformance tag would have made both invisible
// to every linter but the comment checker, since golangci-lint is invoked with no --build-tags,
// and invisible to CI, which calls lint, test, and build.
//
// The tag is for a toolchain make test might genuinely not have, which begins at Node. So
// internal/runtime/node's run is tagged conformance and reached by make test-conformance, which
// builds the SDK first; the tag is named in .golangci.yml, so that file is still linted. Under the
// tag a missing node is a failure and not a skip, because asking for the run is itself a statement
// that the toolchain is there.
//
// # Two notes for whoever adds a language
//
// The package is named runtime and imports internal/runtime, which is legal because a package's
// own name is never an identifier in its own files. A caller needing both aliases one --
// conformance for this package reads best.
//
// The fixtures live under testdata and are therefore not linted: internal/lint skips that
// directory, as it already does for the adapter's own project. They are still exemplars an author
// may read, so write them the way the conventions ask.
package runtime
