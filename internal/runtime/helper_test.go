package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/runtime/protocol"
)

// scriptVar names the environment variable a re-executed test binary reads its script out of.
const scriptVar = "MIGR_FAKE_RUNTIME"

// TestMain re-executes this binary as a fake runtime when the script variable is set.
func TestMain(m *testing.M) {
	script, playing := os.LookupEnv(scriptVar)
	if !playing {
		os.Exit(m.Run())
	}

	os.Exit(play(script))
}

// The behaviors a fake runtime is asked to play, each named by what it does wrong or right.
const (
	// actNormal answers everything the way a well-behaved SDK does.
	actNormal = "normal"
	// actSilent never answers anything, which is what the handshake timeout is for.
	actSilent = "silent"
	// actGarbage writes a line no codec can frame, which ends the session (§2.6).
	actGarbage = "garbage"
	// actWrongVersion picks a protocol version the engine never offered.
	actWrongVersion = "wrong-version"
	// actUnsupported refuses the handshake the way an SDK too new for this engine does.
	actUnsupported = "unsupported"
	// actNoisy writes to stderr and then exits non-zero without answering.
	actNoisy = "noisy"
	// actDeaf answers the handshake and ignores everything after it, shutdown included.
	actDeaf = "deaf"
	// actStranger answers a request the engine never sent, which crosses the two id sequences.
	actStranger = "stranger"
	// actCallbacks makes host callbacks, emits operations, and reports how many.
	actCallbacks = "callbacks"
	// actMiscount emits one operation and claims two, which §4 makes a protocol error.
	actMiscount = "miscount"
	// actBackfills reaches the database through the escape hatch as well as emitting an operation.
	actBackfills = "backfills"
	// actRefuses answers migration.execute with no_down_authored.
	actRefuses = "refuses"
	// actIrreversible answers with declared_irreversible and the author's reason.
	actIrreversible = "irreversible"
	// actSlow never answers migration.execute, so only a cancel ends it.
	actSlow = "slow"
	// actStubborn ignores $/cancel too, so only the kill ends it.
	actStubborn = "stubborn"
	// actForks leaves a grandchild running in the same group, to prove the group is what dies.
	actForks = "forks"
	// actOrphan is that grandchild: it reads nothing, so only a signal can end it.
	actOrphan = "orphan"
	// actIgnores answers the handshake and then traps SIGTERM, so only SIGKILL ends it.
	actIgnores = "ignores"
	// actSluggish answers everything, slowly, so a canceled context is not racing an answer.
	actSluggish = "sluggish"
)

// backfillParams is the escape-hatch statement actBackfills sends, which plan mode holds.
const backfillParams = `{"sql":"UPDATE users SET status = 'active'","args":["active"]}`

// dawdle is how long actSluggish takes to answer, which is long enough to lose no race.
const dawdle = 150 * time.Millisecond

// play runs the fake runtime named by a script and returns the status it should exit with.
func play(script string) int {
	fake := &fake{
		act: script,
		in:  protocol.NewReader(os.Stdin),
		out: protocol.NewWriter(os.Stdout),
	}

	return fake.run()
}

// fake is one scripted runtime: it reads the engine's requests and answers as its script says.
type fake struct {
	act string
	in  *protocol.Reader
	out *protocol.Writer
	id  int64
}

// run answers requests until the engine stops asking or the script says to stop.
func (f *fake) run() int {
	if f.act == actNoisy {
		return f.noisy()
	}

	if f.act == actOrphan {
		forever()
	}

	if f.act == actIgnores {
		signal.Notify(make(chan os.Signal, 1), syscall.SIGTERM)
	}

	if f.act == actForks {
		f.fork()
	}

	for {
		m, err := f.in.Read()
		if err != nil {
			return 0
		}

		if stop := f.answer(m); stop {
			return 0
		}
	}
}

// noisy writes to stderr and dies without answering, which is what §9 says the engine must report.
func (f *fake) noisy() int {
	for i := range 30 {
		fmt.Fprintf(os.Stderr, "line %d from a runtime that is about to die\n", i)
	}

	return 3
}

// forever blocks without deadlocking, since a goroutine in select{} panics the whole process.
func forever() {
	for {
		time.Sleep(time.Hour)
	}
}

// fork leaves a grandchild in this process group with nothing but a signal able to end it.
func (f *fake) fork() {
	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), scriptVar+"="+actOrphan)
	child.Stdin = nil

	if err := child.Start(); err != nil {
		return
	}

	fmt.Fprintf(os.Stderr, "grandchild %d\n", child.Process.Pid)
}

// answer replies to one request the way this script says to, reporting whether the session is over.
func (f *fake) answer(m *protocol.Message) bool {
	if id, numbered := m.ID.Number(); numbered {
		fmt.Fprintf(os.Stderr, "request %d %s\n", id, m.Method)
	} else {
		fmt.Fprintf(os.Stderr, "notified %s\n", m.Method)
	}

	switch m.Method {
	case protocol.Initialize:
		return f.initialize(m)
	case protocol.Shutdown:
		return f.shutdown(m)
	case protocol.MigrationDiscover:
		return f.reply(m, protocol.DiscoverResult{Migrations: []protocol.Discovered{{
			Version: "20260727T153000", File: "20260727T153000_add_users.go", Dependencies: []string{},
		}}})
	case protocol.MigrationExecute:
		return f.execute(m)
	case protocol.MigrationValidate:
		return f.reply(m, struct{}{})
	case protocol.Cancel:
		return f.canceled(m)
	default:
		return f.refuse(m, protocol.NewError(protocol.CodeMethodNotFound, m.Method))
	}
}

// initialize answers the handshake, or breaks it in whichever way this script was asked to.
func (f *fake) initialize(m *protocol.Message) bool {
	switch f.act {
	case actSilent, actSlow, actStubborn:
		if f.act == actSilent {
			forever()
		}
	case actGarbage:
		fmt.Fprintln(os.Stdout, "this is not a message")
		f.echo()

		return true
	case actWrongVersion:
		return f.reply(m, protocol.InitializeResult{ProtocolVersion: 99, Runtime: named(), SDK: named()})
	case actUnsupported:
		return f.refuse(m, protocol.NewError(protocol.CodeUnsupportedProtocol,
			"this SDK speaks protocol versions 2 and 3; the engine offered 1"))
	case actStranger:
		f.reply(m, protocol.InitializeResult{ProtocolVersion: 1, Runtime: named(), SDK: named()})
		f.out.Write(protocol.NewResponse(4711, json.RawMessage(`{}`)))

		return false
	}

	return f.reply(m, protocol.InitializeResult{ProtocolVersion: 1, Runtime: named(), SDK: named()})
}

// echo prints the next line the engine writes to stderr, so a test can read what it was told.
func (f *fake) echo() {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return
	}

	fmt.Fprintf(os.Stderr, "engine said %s", line)
}

// named is what this fake calls itself in the handshake, which the engine logs and never branches on.
func named() protocol.Component {
	return protocol.Component{Name: "fake", Version: "1.0.0"}
}

// shutdown answers and stops, unless this script is the one that ignores it.
func (f *fake) shutdown(m *protocol.Message) bool {
	if f.act == actDeaf || f.act == actStubborn || f.act == actIgnores {
		return false
	}

	if f.act == actSluggish {
		go f.dawdling(m)

		return false
	}

	f.reply(m, struct{}{})

	return true
}

// dawdling answers shutdown late, from a goroutine, so the main loop can still read what arrives.
func (f *fake) dawdling(m *protocol.Message) {
	time.Sleep(dawdle)
	f.reply(m, struct{}{})
	os.Exit(0)
}

// execute answers migration.execute the way this script says, callbacks and all.
func (f *fake) execute(m *protocol.Message) bool {
	switch f.act {
	case actSlow, actStubborn:
		return false
	case actRefuses:
		return f.refuse(m, protocol.NewError(protocol.CodeNoDownAuthored,
			"this migration authors no down of its own"))
	case actIrreversible:
		return f.refuse(m, irreversible())
	case actCallbacks, actMiscount, actBackfills:
		return f.emit(m)
	}

	return f.reply(m, protocol.ExecuteResult{Operations: 0})
}

// irreversible is the refusal §7.4 shows, reason and all, which the engine reports as the author's.
func irreversible() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeDeclaredIrreversible,
		Message: "this migration declares that it cannot be undone",
		Data: json.RawMessage(
			`{"slug":"declared_irreversible","reason":"the rows are gone; restore from a backup"}`),
	}
}

// emit makes the callbacks a real migration makes, then reports what it emitted.
func (f *fake) emit(m *protocol.Message) bool {
	f.callback(protocol.HostInfo, `{}`)
	f.callback(protocol.HostDBQuery, `{"sql":"SELECT count(*) AS n FROM users","args":[]}`)

	if f.act == actBackfills {
		f.callback(protocol.HostDBExec, backfillParams)
	}

	f.callback(protocol.HostSchemaApply, `{"operations":[{"op":"drop_table","table":"legacy"}]}`)
	f.notify(protocol.HostLog, `{"level":"info","message":"emitted"}`)

	emitted := 1
	if f.act == actMiscount {
		emitted = 2
	}

	return f.reply(m, protocol.ExecuteResult{Operations: emitted})
}

// canceled answers whatever the engine asked to abandon, unless this script is deaf to that too.
func (f *fake) canceled(m *protocol.Message) bool {
	if f.act == actStubborn {
		return false
	}

	var p protocol.CancelParams
	if err := protocol.UnmarshalPayload(m.Params, &p); err != nil {
		return false
	}

	f.notify(protocol.HostLog, fmt.Sprintf(`{"level":"info","message":"canceled %d"}`, p.ID))

	f.out.Write(protocol.NewFailure(p.ID,
		protocol.NewError(protocol.CodeCanceled, "the engine asked")))

	return false
}

// callback makes one host request and waits for its answer, the way an SDK that does not pipeline does.
func (f *fake) callback(method, params string) *protocol.Message {
	f.id++

	if err := f.out.Write(protocol.NewRequest(f.id, method, json.RawMessage(params))); err != nil {
		return nil
	}

	answer, err := f.in.Read()
	if err != nil {
		return nil
	}

	return answer
}

// notify sends one notification, which the host never answers.
func (f *fake) notify(method, params string) {
	f.out.Write(protocol.NewNotification(method, json.RawMessage(params)))
}

// reply answers one request with a result, reporting whether the session should end.
func (f *fake) reply(m *protocol.Message, result any) bool {
	payload, err := protocol.MarshalPayload(result)
	if err != nil {
		return true
	}

	id, _ := m.ID.Number()

	return f.out.Write(protocol.NewResponse(id, payload)) != nil
}

// refuse answers one request with an error, reporting whether the session should end.
func (f *fake) refuse(m *protocol.Message, e *protocol.Error) bool {
	id, _ := m.ID.Number()

	return f.out.Write(protocol.NewFailure(id, e)) != nil
}

// fakeRuntime is the adapter under test: it re-executes this test binary with a script to play.
type fakeRuntime struct {
	act string
	// path overrides the program, so a test can spawn something that is not there.
	path string
	// fail is what Command reports instead of a command, for the adapter that cannot prepare one.
	fail error
}

// Name is what this adapter is registered under.
func (r *fakeRuntime) Name() string { return "fake" }

// Discover reports that this machine can run the fake, which it can by construction.
func (r *fakeRuntime) Discover(context.Context) error { return r.fail }

// Command re-executes the test binary with the script this fake was built to play.
func (r *fakeRuntime) Command(_ context.Context, p Project) (Command, error) {
	if r.fail != nil {
		return Command{}, r.fail
	}

	path := r.path
	if path == "" {
		path = os.Args[0]
	}

	return Command{
		Path: path,
		Args: []string{"-test.run=TestMain"},
		Env:  append(os.Environ(), scriptVar+"="+r.act),
		Dir:  p.Root,
	}, nil
}

// compile-time proof that the fake is held to the contract every adapter is.
var _ Runtime = (*fakeRuntime)(nil)

// waitUntil polls a condition until it holds or the test gives up, for what a process does in its own time.
func waitUntil(t *testing.T, why string, holds func() bool) {
	t.Helper()

	deadline := time.After(5 * time.Second)

	for {
		if holds() {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", why)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// requests returns every request the fake reported being asked, in the order it was asked.
func requests(lines []string) []string {
	asked := make([]string, 0, len(lines))

	for _, line := range lines {
		if strings.HasPrefix(line, "request ") {
			asked = append(asked, line)
		}
	}

	return asked
}

// heard returns what the fake reported the engine said to it, which is how a test reads stdin.
func heard(lines []string) string {
	for _, line := range lines {
		if rest, found := strings.CutPrefix(line, "engine said "); found {
			return rest
		}
	}

	return ""
}

// contains reports whether any of the lines holds the text, for a stderr tail a test reads.
func contains(lines []string, text string) bool {
	for _, line := range lines {
		if strings.Contains(line, text) {
			return true
		}
	}

	return false
}
