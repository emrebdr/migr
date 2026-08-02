// Package migration finds migration files and puts them in the order they must run.
//
// It is the first thing above the driver layer and the first code to read migrations.dir, which
// config has resolved since M0-T7 and nothing has ever used. It imports stdlib and nothing else,
// so internal/cli can list migrations for status and validate without reaching through an
// executor, and so M4-T2's dependency DAG and M4-T3's reconciliation build on a type rather than
// on a package that also opens databases.
//
// Status: discovery and version ordering complete (M4-T1). The dependency DAG that overlays this
// order is M4-T2, reconciliation against history is M4-T3, and checksum verification is M4-T7.
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
