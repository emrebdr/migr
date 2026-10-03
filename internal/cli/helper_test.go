package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/runtime"
	"github.com/emrebdr/migr/internal/runtime/protocol"
	"github.com/emrebdr/migr/internal/source/golang"
	"github.com/emrebdr/migr/internal/source/typescript"
)

// scriptVar names the behavior this binary plays when it is re-executed as a runtime.
const scriptVar = "MIGR_CLI_FAKE_RUNTIME"

// declareVar names what the re-executed runtime has its migrations declare, which is its own axis.
const declareVar = "MIGR_CLI_FAKE_DECLARES"

// TestMain re-executes this binary as a language runtime when the script variable is set.
func TestMain(m *testing.M) {
	script, playing := os.LookupEnv(scriptVar)
	if !playing {
		os.Exit(m.Run())
	}

	os.Exit(playRuntime(script, os.Getenv(declareVar)))
}

// The behaviors a scripted runtime plays, each named by what a migration written in it does.
const (
	// actEmits emits one raw operation per direction, which is a migration that reads nothing.
	actEmits = "emits"
	// actReads asks the database a question first, which is what an offline session refuses.
	actReads = "reads"
	// actDerives refuses the down, so the engine derives one from what the up emitted.
	actDerives = "derives"
	// actReadsFirst reads only for the earliest migration, so the later one stays checkable.
	actReadsFirst = "reads-first"
	// actReadsDown reads only for the down, so the up stays checkable and the down does not.
	actReadsDown = "reads-down"
	// actAsks asks the host about an operation first, which is the callback that reaches the session.
	actAsks = "asks"
	// actLoadsNothing reports loading no migration at all, which the engine routed files to it.
	actLoadsNothing = "loads-nothing"
	// actLoadsExtra reports a migration nothing routed to it, so two runtimes would claim one file.
	actLoadsExtra = "loads-extra"
	// actTypes emits typed operations rather than raw ones, which is what a lint rule can read.
	actTypes = "types"
)

// The declarations a scripted runtime reports, which compose with every act above.
const (
	// declaresNothing is what a migration that declares no dependency reports, and is the default.
	declaresNothing = ""
	// declaresNext has each migration declare the version after it, so the run order is reversed.
	declaresNext = "next"
	// declaresCycle closes that chain, so the last migration declares the first and no order exists.
	declaresCycle = "cycle"
	// declaresUnknown has each migration declare a version no file in the directory carries.
	declaresUnknown = "unknown"
)

// strayVersion is the migration actLoadsExtra claims, which no file in any staged project carries.
const strayVersion = "20991231T235959"

// shutdownMarker opens the name a scripted runtime writes into the project root when it is stopped.
const shutdownMarker = ".migr-shutdown-"

// askedOperation is the document actAsks asks the host to resolve, which every driver answers for.
const askedOperation = `{"operation":{"op":"raw","driver":"fake","sql":"SELECT 1"}}`

// firstVersion is the migration actReadsFirst reads for, which is the one staged at index zero.
var firstVersion = versionAt(0)

// playRuntime answers the engine until it stops asking, as the script it was given says to.
func playRuntime(script, declares string) int {
	r := &scriptedRuntime{
		act:      script,
		declares: declares,
		in:       protocol.NewReader(os.Stdin),
		out:      protocol.NewWriter(os.Stdout),
	}

	return r.run()
}

// scriptedRuntime is a language runtime with no language behind it, answering what the CLI asks.
type scriptedRuntime struct {
	act string
	in  *protocol.Reader
	out *protocol.Writer
	id  int64
	// dir is the migrations directory initialize named, which discover reads to say what it loaded.
	dir string
	// lang is the language this process was started for, which decides which files are its own.
	lang config.Language
	// declares is what this process has each of its migrations declare it must follow.
	declares string
}

// run reads the engine's requests and answers them until the pipe closes or shutdown arrives.
func (r *scriptedRuntime) run() int {
	for {
		m, err := r.in.Read()
		if err != nil {
			return 0
		}

		if stop := r.answer(m); stop {
			return 0
		}
	}
}

// answer serves one request, reporting whether this process should stop reading afterwards.
func (r *scriptedRuntime) answer(m *protocol.Message) bool {
	switch m.Method {
	case protocol.Initialize:
		return r.initialize(m)
	case protocol.MigrationDiscover:
		return r.discover(m)
	case protocol.MigrationExecute:
		return r.execute(m)
	case protocol.MigrationValidate:
		return r.reply(m, struct{}{})
	case protocol.Shutdown:
		r.mark()
		r.reply(m, struct{}{})

		return true
	default:
		return r.refuse(m, protocol.NewError(protocol.CodeMethodNotFound, m.Method))
	}
}

// initialize answers the handshake, refusing a project this runtime was told wrongly about.
func (r *scriptedRuntime) initialize(m *protocol.Message) bool {
	var p protocol.InitializeParams
	if err := protocol.UnmarshalPayload(m.Params, &p); err != nil {
		return r.refuse(m, protocol.NewErrorf(protocol.CodeInvalidParams, "%v", err))
	}

	if wrong := describeProject(p); wrong != "" {
		return r.refuse(m, protocol.NewErrorf(protocol.CodeInvalidParams, "%s", wrong))
	}

	r.dir, r.lang = p.Migrations.Dir, config.Language(p.Migrations.Language)

	return r.reply(m, protocol.InitializeResult{ProtocolVersion: 1,
		Runtime: protocol.Component{Name: "scripted", Version: "1.0.0"},
		SDK:     protocol.Component{Name: "scripted", Version: "1.0.0"},
	})
}

// discover reports the files in the directory this process can execute, which is its own language's.
func (r *scriptedRuntime) discover(m *protocol.Message) bool {
	if r.act == actLoadsNothing {
		return r.reply(m, protocol.DiscoverResult{Migrations: []protocol.Discovered{}})
	}

	loaded, err := r.loadable()
	if err != nil {
		return r.refuse(m, protocol.NewErrorf(protocol.CodeInternal, "%v", err))
	}

	if r.act == actLoadsExtra {
		loaded = append(loaded, protocol.Discovered{
			Version: strayVersion, File: strayVersion + "_stray" + extensions[r.lang][0],
		})
	}

	return r.reply(m, protocol.DiscoverResult{Migrations: loaded})
}

// mark records in the project root that this runtime was shut down rather than merely killed.
func (r *scriptedRuntime) mark() {
	_ = os.WriteFile(shutdownMarker+string(r.lang), []byte(r.act), 0o600)
}

// loadable reads the migrations directory and keeps what this language's extensions name.
func (r *scriptedRuntime) loadable() ([]protocol.Discovered, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}

	found := migrationsIn(entries)

	loaded := []protocol.Discovered{}

	for _, m := range found {
		if !slices.Contains(extensions[r.lang], m.Ext) {
			continue
		}

		loaded = append(loaded, protocol.Discovered{Version: m.Version, File: m.Filename(),
			Dependencies: r.declaredBy(m, found)})
	}

	return loaded, nil
}

// migrationsIn returns every entry the grammar accepts, in version order, whatever language owns it.
func migrationsIn(entries []os.DirEntry) []migration.Migration {
	var found []migration.Migration

	for _, entry := range entries {
		if parsed, err := migration.Parse(entry.Name()); err == nil {
			found = append(found, parsed)
		}
	}

	slices.SortFunc(found, func(a, b migration.Migration) int {
		return strings.Compare(a.Version, b.Version)
	})

	return found
}

// declaredBy renders what this script has one migration declare, which may name another language's.
func (r *scriptedRuntime) declaredBy(m migration.Migration,
	found []migration.Migration) []string {
	switch r.declares {
	case declaresNext, declaresCycle:
		return r.chained(m, found)
	case declaresUnknown:
		return []string{strayVersion}
	}

	return []string{}
}

// chained declares the migration after this one, closing the loop for the last one only in a cycle.
func (r *scriptedRuntime) chained(m migration.Migration, found []migration.Migration) []string {
	if next := after(m, found); next != "" {
		return []string{next}
	}

	if r.declares == declaresCycle && len(found) > 1 {
		return []string{found[0].Version}
	}

	return []string{}
}

// after names the version that sorts directly after one, and is empty for the last migration.
func after(m migration.Migration, found []migration.Migration) string {
	at := slices.IndexFunc(found, func(other migration.Migration) bool {
		return other.Version == m.Version
	})

	if at < 0 || at == len(found)-1 {
		return ""
	}

	return found[at+1].Version
}

// describeProject reports a runtime told the wrong thing about where it is or what it runs.
func describeProject(p protocol.InitializeParams) string {
	if !slices.Contains(scripted, config.Language(p.Migrations.Language)) {
		return fmt.Sprintf("this runtime was started for %q", p.Migrations.Language)
	}

	if !filepath.IsAbs(p.Migrations.Dir) {
		return fmt.Sprintf("the migrations directory %q is not absolute", p.Migrations.Dir)
	}

	if _, err := os.Stat(filepath.Join(mustGetwd(), config.DefaultFilename)); err != nil {
		return fmt.Sprintf("the working directory holds no %s", config.DefaultFilename)
	}

	return ""
}

// mustGetwd reports where this process was started, which docs/protocol.md §9 makes the root.
func mustGetwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	return dir
}

// execute answers one direction of one migration, making the callbacks its script says it makes.
func (r *scriptedRuntime) execute(m *protocol.Message) bool {
	var p protocol.ExecuteParams
	if err := protocol.UnmarshalPayload(m.Params, &p); err != nil {
		return r.refuse(m, protocol.NewErrorf(protocol.CodeInvalidParams, "%v", err))
	}

	if r.act == actReads || (r.act == actReadsFirst && p.ID == firstVersion) ||
		(r.act == actReadsDown && p.Direction == ir.Down) {
		r.callback(protocol.HostDBQuery, `{"sql":"SELECT count(*) AS n FROM users","args":[]}`)
	}

	if r.act == actAsks {
		r.callback(protocol.HostCapabilityGet, askedOperation)
	}

	if r.act == actDerives && p.Direction == ir.Down {
		return r.refuse(m, protocol.NewError(protocol.CodeNoDownAuthored,
			"this migration authors no down of its own"))
	}

	r.callback(protocol.HostSchemaApply, r.operations(p.Direction))

	return r.reply(m, protocol.ExecuteResult{Operations: 1})
}

// operations renders the one operation this script emits for one direction.
func (r *scriptedRuntime) operations(direction ir.Direction) string {
	if r.act == actTypes {
		return typedOperationsFor(direction)
	}

	return operationsFor(direction)
}

// operationsFor renders the one operation a scripted migration emits for one direction.
func operationsFor(direction ir.Direction) string {
	statement := "CREATE TABLE scripted (id bigint)"
	if direction == ir.Down {
		statement = "DROP TABLE scripted"
	}

	return fmt.Sprintf(`{"operations":[{"op":"raw","driver":%q,"sql":%q}]}`, fakeName, statement)
}

// scriptedTable is the table a typed scripted migration creates, with no key and a text column.
const scriptedTable = "scripted"

// typedOperationsFor renders a create_table a lint rule can read, and the drop that undoes it.
func typedOperationsFor(direction ir.Direction) string {
	if direction == ir.Down {
		return fmt.Sprintf(`{"operations":[{"op":"drop_table","table":%q}]}`, scriptedTable)
	}

	return fmt.Sprintf(`{"operations":[{"op":"create_table","table":%q,"columns":`+
		`[{"name":"id","type":"int64","nullable":false},`+
		`{"name":"note","type":"text","nullable":false}]}]}`, scriptedTable)
}

// callback makes one host request and waits for its answer, as an SDK that does not pipeline does.
func (r *scriptedRuntime) callback(method, params string) {
	r.id++

	if err := r.out.Write(protocol.NewRequest(r.id, method, json.RawMessage(params))); err != nil {
		return
	}

	if _, err := r.in.Read(); err != nil {
		return
	}
}

// reply answers one request with a result, reporting whether the session should end.
func (r *scriptedRuntime) reply(m *protocol.Message, result any) bool {
	payload, err := protocol.MarshalPayload(result)
	if err != nil {
		return true
	}

	id, _ := m.ID.Number()

	return r.out.Write(protocol.NewResponse(id, payload)) != nil
}

// refuse answers one request with an error, reporting whether the session should end.
func (r *scriptedRuntime) refuse(m *protocol.Message, e *protocol.Error) bool {
	id, _ := m.ID.Number()

	return r.out.Write(protocol.NewFailure(id, e)) != nil
}

// scriptedAdapter is the adapter under test: it re-executes this binary with a script to play.
type scriptedAdapter struct {
	// name is the language this adapter claims, which is the key the registry holds it under.
	name string
	// act is the behavior the spawned process plays.
	act string
	// declares is what that process has its migrations declare, which every act composes with.
	declares string
}

// Name reports the language this adapter runs.
func (a *scriptedAdapter) Name() string { return a.name }

// Discover reports that this machine can run the script, which it can by construction.
func (a *scriptedAdapter) Discover(context.Context) error { return nil }

// Command re-executes this test binary with the script this adapter was built to play.
func (a *scriptedAdapter) Command(_ context.Context, p runtime.Project) (runtime.Command, error) {
	return runtime.Command{
		Path: os.Args[0],
		Args: []string{"-test.run=TestMain"},
		Env:  append(os.Environ(), scriptVar+"="+a.act, declareVar+"="+a.declares),
		Dir:  p.Root,
	}, nil
}

// compile-time proof that the scripted adapter is held to the contract every adapter is.
var _ runtime.Runtime = (*scriptedAdapter)(nil)

// scripted are the languages a registry of scripted adapters answers for, which is every one
// migr.yaml can name that needs a runtime at all.
var scripted = []config.Language{config.LanguageGo, config.LanguageTypeScript}

// scriptedRegistry returns a registry whose adapters start a process playing one script.
func scriptedRegistry(t *testing.T, act string) *runtime.Registry {
	t.Helper()

	return declaringRegistry(t, act, declaresNothing)
}

// declaringRegistry returns the same registry over migrations that declare what they are told to.
func declaringRegistry(t *testing.T, act, declares string) *runtime.Registry {
	t.Helper()

	registry := runtime.NewRegistry()

	for _, lang := range scripted {
		name := string(lang)

		if err := registry.Register(name, func() (runtime.Runtime, error) {
			return &scriptedAdapter{name: name, act: act, declares: declares}, nil
		}); err != nil {
			t.Fatalf("registering the scripted %s adapter: %v", name, err)
		}
	}

	return registry
}

// goProject stages a project whose migrations are Go files, which only a runtime can read.
func goProject(t *testing.T, names ...string) (string, *fake, *driver.Registry) {
	t.Helper()

	return runtimeProject(t, config.LanguageGo, golang.Ext, "package migrations\n", names...)
}

// tsProject stages a project whose migrations are TypeScript files, which a runtime also reads.
func tsProject(t *testing.T, names ...string) (string, *fake, *driver.Registry) {
	t.Helper()

	return runtimeProject(t, config.LanguageTypeScript, typescript.Ext, "export const up = 0;\n",
		names...)
}

// runtimeProject stages a project in one language, whose files no command here ever reads.
func runtimeProject(t *testing.T, lang config.Language, ext, body string,
	names ...string) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t)
	write(t, filepath.Join(dir, config.DefaultFilename),
		strings.Replace(projectConfig, "language: sql", "language: "+string(lang), 1))

	for i, name := range names {
		write(t, filepath.Join(dir, scaffoldDir, versionAt(i)+"_"+name+ext), body)
	}

	return dir, f, drivers
}

// runScripted runs the CLI against a project a runtime reads, over a driver that records what it ran.
func runScripted(t *testing.T, dir, act string, drivers *driver.Registry,
	args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return runOptions(t, dir, &options{clock: ticking(noon), drivers: drivers,
		runtimes: scriptedRegistry(t, act)}, args...)
}

// runDeclaring runs the CLI against a project whose migrations declare what they are told to.
func runDeclaring(t *testing.T, dir, act, declares string, drivers *driver.Registry,
	args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return runOptions(t, dir, &options{clock: ticking(noon), drivers: drivers,
		runtimes: declaringRegistry(t, act, declares)}, args...)
}
