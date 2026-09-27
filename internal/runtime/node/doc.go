// Package node is the runtime adapter for migrations written in TypeScript and
// JavaScript.
//
// It finds node, asks it what it is and where this project's @migr/sdk is, and
// hands back the migr-runtime-node the package installs. Starting it is not this
// package's: an adapter returns a Command description and internal/runtime.Session
// owns the pipes, the process group, §8.2's timers and the kill (ADR 0034, B).
//
// # What one probe answers
//
// Command runs node once, with the project root as its working directory, and reads
// two facts off one marked line: what node reports itself as, and what
// require.resolve("@migr/sdk/package.json") finds. Resolution is Node's own rather
// than an imitation of it, which is the choice internal/runtime/golang makes when it
// asks `go list -m` instead of reading go.mod — and it is what makes a workspace
// whose node_modules is hoisted above the project root work, along with pnpm's
// symlinks and whatever a package manager invents next.
//
// The line is marked because stdout is not guaranteed to be empty: an ambient
// NODE_OPTIONS that preloads a module which prints would otherwise be read as a
// version. The probe itself cannot throw, so one process answers both questions and
// this package decides which failure to report — the version first, since a node old
// enough to matter cannot resolve an exports-only package either, and would
// otherwise be reported as a missing install.
//
// Discover opens no process at all. It answers "can this machine run the language",
// which a report may ask without paying for a toolchain probe (ADR 0035).
//
// # Where the path comes from
//
// From the package, not from here. @migr/sdk's own bin field names its executable,
// so the location of the built file is stated once, in the package that builds it,
// and this adapter reads it. It runs that file through node rather than running
// node_modules/.bin/migr-runtime-node, which is a shell script on POSIX and a .cmd
// on Windows — the file both of them point at is the one bin names.
//
// The resulting Command carries an argument, where the Go adapter's carries none.
// That is not in tension with docs/protocol.md §9: what §9 forbids is a runtime
// depending on argv, and migr-runtime-node reads none. Naming a script to node is
// how the program is spelled.
//
// # What this adapter deliberately does not do
//
// There is no build, no stamp, no cache and no .migr/ directory. Node loads its own
// source, and the SDK transpiles a .ts migration as it imports it, so there is
// nothing to generate and nothing whose staleness needs deciding.
//
// It also leaves the environment alone, which is §9's rule. Clearing NODE_OPTIONS to
// defend the protocol stream was considered and rejected: it would break a legitimate
// --max-old-space-size, and a print from a preloaded module fails loudly as a line the
// engine cannot frame, which is the hole ADR 0044 already documents for module scope.
//
// Implemented in M7-T6 (see TASKS.md).
package node
