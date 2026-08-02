// Package migration finds migration files and puts them in the order they must run.
//
// It is the first thing above the driver layer and the first code to read migrations.dir, which
// config has resolved since M0-T7 and nothing has ever used. It imports stdlib and nothing else,
// so internal/cli can list migrations for status and validate without reaching through an
// executor, and so M4-T2's dependency DAG and M4-T3's reconciliation build on a type rather than
// on a package that also opens databases.
//
// Status: discovery, version ordering, and the dependency overlay complete (M4-T1, M4-T2).
// Checksum verification is M4-T7.
//
// Reconciliation against history is internal/history, which imports this package (M4-T3). That is
// what turned the leaf property above from a review rule into a compiler-enforced one: importing
// history back is now an import cycle.
//
// # The grammar
//
//	<YYYYMMDDThhmmss>_<snake_case name>.<ext>      20260727T153000_add_users.sql
//
// The version is parsed as a real UTC instant, so 20260230T000000 and 20260727T253000 are refused
// rather than sorted — a filename that cannot be a time is a typo, and a typo in a version is a
// migration that runs in the wrong place. The name is lowercase letters and digits in words joined
// by single underscores, which is what snake_case means and what makes a leading, trailing, or
// doubled underscore a stated error rather than a silently accepted variant.
//
// The extension is recorded and never judged. Which runtime can execute a .sql, .ts, or .go file
// is the runtime registry's question and the registry arrives in M6 and M7; filtering here by the
// configured migrations.language would make a .ts file in a Go project vanish rather than be
// refused, which is the same failure this package's strictness exists to prevent.
//
// # The version stays a string
//
// Parsing is validation and nothing more: the value kept is the text the filename carried.
// history.Record.Version stores that string, `migr down --to <version>` compares against it, and
// the format is fixed width and zero padded, so lexical order is chronological order. Holding a
// time.Time beside it would be a second representation of one fact, and the two can disagree.
//
// # The dependency overlay
//
// Order takes what Discover found and what each migration declares it must follow, and returns one
// sequence. A timestamp is enough until two branches each write one and merge, which is what
// CLAUDE.md §9 means by parallel branches and merged feature work — so a declared dependency may
// point backwards in time, and that is the use case rather than a mistake.
//
// The sort is Kahn's, with the ready set drained in version order. That makes the timestamp the
// tiebreak among migrations nothing constrains relative to each other, and it makes the result a
// total sequence rather than a graph: M4-T5 aborts a queue and M4-T8 steps through one, and handing
// either a graph would leave both re-deriving this.
//
// An edge names a version, because that is the key discovery proves unique, the one a history row
// joins on, and the one `migr down --to` names. Every message renders a filename instead, since a
// loop printed as three fifteen-digit timestamps is the least useful form of the one error this
// exists to produce. ADR 0018 records both, and records that a declaration is read from a migration
// rather than computed by one — a graph that could branch on live query results would let plan and
// apply order the same migrations differently.
//
// Declared is a map, so nothing that decides an order may iterate it (invariant #8). Every decision
// walks the sorted migration list and looks up; each dependency list is walked sorted, so a
// caller's own slice order cannot choose which cycle gets reported; and the one place a map has to
// be walked — collecting declarations whose key names no discovered migration — sorts before
// reporting.
//
// # Everything wrong with a graph, in one pass
//
// GraphError carries every unknown dependency and every cycle rather than the first of either,
// because M5-T5's `migr validate` asks for exactly that and a graph with four broken edges is
// otherwise four commands.
//
// Reporting both together is sound rather than convenient: an edge naming a version nothing carries
// is dropped before the sort runs, and dropping an edge can only remove cycles, never create one.
// So every loop left is genuine and neither report sends an operator chasing the other.
//
// One report is made for each edge that closes a loop, so two loops through one migration are both
// reported — breaking one leaves the other — and no loop is reported twice, since the walk crosses
// each edge once. A migration merely waiting behind a cycle is not reported as being in one:
// nothing about it can be edited to fix anything. Each path is rotated to start at its earliest
// migration, so it reads the same however the walk happened to arrive.
//
// # A stray file is an error, and a near miss is why
//
// 20260727T153000-add-users.ts — hyphens where the grammar wants underscores — is a migration
// someone wrote, committed, and expects to run. Skipped quietly, `migr up` reports nothing to do
// and the schema change is found missing in production. So the only entries this package passes
// over are dotfiles: a .gitkeep or a .DS_Store was never meant to be a migration, and everything
// else in the directory has to parse or the command fails naming the file. A README.md that fails
// loudly is by a wide margin the cheaper mistake.
//
// A subdirectory is refused for the same reason rather than skipped. Discovery is flat, so
// migrations/2026/ would otherwise be a whole tree of migrations that silently does not exist.
//
// # Two files cannot claim one version
//
// history keys a run by (batch, ordinal), but M4-T3 joins the file to the row by version and
// `--to` names one. Two files with one timestamp make both questions unanswerable, so a duplicate
// is refused at discovery naming both files, rather than resolved by a tiebreak nobody can
// predict. `migr create` uses second resolution, so two creates in one second is the way this
// happens.
//
// # Discovery takes a filesystem, not a path
//
// Discover reads an fs.FS already rooted at the migrations directory, so every case this package
// has an opinion about — a hyphen, a February 30th, two files one second apart, a subdirectory —
// is an fstest.MapFS literal rather than a temporary tree. internal/cli passes
// os.DirFS(cfg.Migrations.Dir). A Migration holds no path for the same reason it holds no parsed
// time: Filename derives it, so nothing can disagree with the name the file is stored under, and
// fs.ReadFile(fsys, m.Filename()) is what M4-T7 will hash.
//
// A missing directory is an error and an empty one is not. Zero migrations is a fresh project;
// a directory that is not there is a project that was never initialized, or a migrations.dir
// pointing somewhere nobody meant. Reading the second as the first is how a fully migrated
// database would look like one with nothing to do.
//
// # The layout lives here
//
// versionLayout is Go's reference instant for the timestamp, and it is deliberately in this
// package rather than in the CLI that will format one. M5-T1's `migr create` has to produce
// exactly what Parse accepts, and a layout string written down in two places is a file format
// with two definitions. Build a Migration and call Filename; do not restate the layout.
package migration
