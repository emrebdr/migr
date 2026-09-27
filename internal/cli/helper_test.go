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
	"github.com/emrebdr/migr/internal/runtime"
	"github.com/emrebdr/migr/internal/runtime/protocol"
	"github.com/emrebdr/migr/internal/source/golang"
	"github.com/emrebdr/migr/internal/source/typescript"
)

// scriptVar names the behavior this binary plays when it is re-executed as a runtime.
const scriptVar = "MIGR_CLI_FAKE_RUNTIME"

// TestMain re-executes this binary as a language runtime when the script variable is set.
func TestMain(m *testing.M) {
	script, playing := os.LookupEnv(scriptVar)
	if !playing {
		os.Exit(m.Run())
	}

	os.Exit(playRuntime(script))
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
)

// firstVersion is the migration actReadsFirst reads for, which is the one staged at index zero.
var firstVersion = versionAt(0)

// playRuntime answers the engine until it stops asking, as the script it was given says to.
func playRuntime(script string) int {
	r := &scriptedRuntime{
		act: script,
		in:  protocol.NewReader(os.Stdin),
		out: protocol.NewWriter(os.Stdout),
	}

	return r.run()
}

// scriptedRuntime is a language runtime with no language behind it, answering what the CLI asks.
type scriptedRuntime struct {
	act string
	in  *protocol.Reader
	out *protocol.Writer
	id  int64
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
		return r.reply(m, protocol.DiscoverResult{Migrations: []protocol.Discovered{}})
	case protocol.MigrationExecute:
		return r.execute(m)
	case protocol.MigrationValidate:
		return r.reply(m, struct{}{})
	case protocol.Shutdown:
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

	return r.reply(m, protocol.InitializeResult{ProtocolVersion: 1,
		Runtime: protocol.Component{Name: "scripted", Version: "1.0.0"},
		SDK:     protocol.Component{Name: "scripted", Version: "1.0.0"},
	})
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

	if r.act == actDerives && p.Direction == ir.Down {
		return r.refuse(m, protocol.NewError(protocol.CodeNoDownAuthored,
			"this migration authors no down of its own"))
	}

	r.callback(protocol.HostSchemaApply, operationsFor(p.Direction))

	return r.reply(m, protocol.ExecuteResult{Operations: 1})
}

// operationsFor renders the one operation a scripted migration emits for one direction.
func operationsFor(direction ir.Direction) string {
	statement := "CREATE TABLE scripted (id bigint)"
	if direction == ir.Down {
		statement = "DROP TABLE scripted"
	}

	return fmt.Sprintf(`{"operations":[{"op":"raw","driver":%q,"sql":%q}]}`, fakeName, statement)
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
		Env:  append(os.Environ(), scriptVar+"="+a.act),
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

	registry := runtime.NewRegistry()

	for _, lang := range scripted {
		name := string(lang)

		if err := registry.Register(name, func() (runtime.Runtime, error) {
			return &scriptedAdapter{name: name, act: act}, nil
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
