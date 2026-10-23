package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

func TestARuntimeIsSpawnedShakesHandsAndShutsDown(t *testing.T) {
	s, _ := start(t, actNormal)

	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !gone(s.proc.pid()) {
		t.Error("the runtime is still running after a clean shutdown")
	}

	if err := s.Close(t.Context()); err != nil {
		t.Errorf("closing twice reported %v, and the ladder runs once", err)
	}
}

func TestARuntimeThatNeverAnswersTheHandshakeIsKilled(t *testing.T) {
	c := newConn(t)

	s, err := Start(t.Context(), Options{
		Runtime:  &fakeRuntime{act: actSilent},
		Project:  project(t),
		Conn:     c,
		Log:      quiet(),
		Timeouts: Timeouts{Handshake: 50 * time.Millisecond, ShutdownGrace: brief},
	})
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("Start returned %v, want %v", err, ErrHandshake)
	}

	if !gone(s.proc.pid()) {
		t.Error("a runtime that never started is still running")
		t.Log("§8.2: exceeding the handshake bound is a kill, not a wait. A runtime that never")
		t.Log("answers holds the migration lock for as long as the engine is polite to it.")
	}
}

func TestARuntimeThatPicksAVersionTheEngineNeverOfferedEndsTheSession(t *testing.T) {
	c := newConn(t)

	s, err := Start(t.Context(), Options{
		Runtime: &fakeRuntime{act: actWrongVersion}, Project: project(t), Conn: c, Log: quiet(),
		Timeouts: Timeouts{ShutdownGrace: brief},
	})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("Start returned %v, want %v", err, ErrProtocol)
	}

	if !strings.Contains(err.Error(), "99") {
		t.Errorf("the refusal does not name the version it answered with: %v", err)
	}

	if !gone(s.proc.pid()) {
		t.Error("a runtime speaking another version is still running")
	}
}

func TestARuntimeThatSupportsNoOfferedVersionIsReportedAsItself(t *testing.T) {
	_, _, err := open2(t, actUnsupported, &kept{})
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("Start returned %v, want %v", err, ErrHandshake)
	}

	if !strings.Contains(err.Error(), "unsupported_protocol") {
		t.Errorf("the refusal loses the slug an SDK branched on: %v", err)
		t.Log("§3.2: an SDK from a registry and a CLI from a release move on different clocks.")
		t.Log("The one message that says which versions it does speak is the runtime's own.")
	}
}

func TestAMigrationsCallbacksReachTheHostAndItsOperationsComeBack(t *testing.T) {
	s, c := start(t, actCallbacks)

	emitted, err := s.Emit(t.Context(), fixture, ir.Up, engine.ModeApply)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if len(emitted.Operations) != 1 || emitted.Operations[0].Kind() != ir.KindDropTable {
		t.Fatalf("the migration emitted %v", emitted.Operations)
	}

	if len(c.reads) != 1 {
		t.Errorf("host.db.query reached the driver %d times, want once", len(c.reads))
	}
}

func TestOperationsAreAskedForInTheModeTheEngineStated(t *testing.T) {
	s, _ := start(t, actNormal)

	for _, mode := range engine.Modes() {
		if _, err := s.Emit(t.Context(), fixture, ir.Up, mode); err != nil {
			t.Fatalf("Emit(%s): %v", mode, err)
		}
	}

	if wire(engine.ModePlan) != protocol.ModePlan || wire(engine.ModeApply) != protocol.ModeApply {
		t.Error("the engine modes do not reach the wire as themselves")
	}
}

func TestACountThatDisagreesWithWhatTheHostRecordedIsAProtocolError(t *testing.T) {
	s, _ := start(t, actMiscount)

	_, err := s.Emit(t.Context(), fixture, ir.Up, engine.ModeApply)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("Emit returned %v, want %v", err, ErrProtocol)
	}

	t.Log("§4: a disagreement between that number and what the host recorded means one side")
	t.Log("dropped a call, which would otherwise be a migration that ran half of itself.")
}

func TestTheTwoRefusalsComeBackAsTheAnswersTheyAre(t *testing.T) {
	s, _ := start(t, actRefuses)

	if _, err := s.Emit(t.Context(), fixture, ir.Down, engine.ModeApply); !errors.Is(
		err, engine.ErrNoDownAuthored) {
		t.Errorf("Emit returned %v, want %v", err, engine.ErrNoDownAuthored)
		t.Log("§7.4: reported as an ordinary failure, every derivable rollback becomes a failed")
		t.Log("one -- the engine never asks for the up it would have inverted.")
	}

	other, _ := start(t, actIrreversible)

	_, err := other.Emit(t.Context(), fixture, ir.Down, engine.ModeApply)
	if !errors.Is(err, engine.ErrDeclaredIrreversible) {
		t.Fatalf("Emit returned %v, want %v", err, engine.ErrDeclaredIrreversible)
	}

	if !strings.Contains(err.Error(), "restore from a backup") {
		t.Errorf("the refusal is not the author's reason: %v", err)
	}
}

func TestARuntimeReportsWhatItCanRun(t *testing.T) {
	s, _ := start(t, actNormal)

	found, err := s.Discovered(t.Context())
	if err != nil {
		t.Fatalf("Discovered: %v", err)
	}

	if len(found) != 2 || found[0].Version != fixture.Version {
		t.Fatalf("the runtime reported %v", found)
	}
}

func TestWhatEachMigrationDeclaredComesBackWithWhatWasLoaded(t *testing.T) {
	s, _ := start(t, actNormal)

	loaded, declarations, err := s.Loaded(t.Context())
	declared := declarations.Dependencies
	if err != nil {
		t.Fatalf("Loaded: %v", err)
	}

	if len(loaded) != 2 || loaded[0].Filename() != fixture.Filename() {
		t.Fatalf("the runtime loaded %v", loaded)
	}

	if len(declared) != 1 || !slices.Equal(declared[dependent], []string{fixture.Version}) {
		t.Errorf("the declarations came back as %v", declared)
		t.Log("The engine already makes this call to reconcile what a runtime loaded against what it")
		t.Log("routed there, so the declarations it needs to order the run cost no second")
		t.Log("round trip. M7-T10 parsed the filename here and dropped this field.")
	}

	if _, declaring := declared[fixture.Version]; declaring {
		t.Errorf("a migration declaring nothing was recorded as declaring %v",
			declared[fixture.Version])
	}
}

func TestAMigrationsOwnCheckRunsInAWindowThatEmitsNothing(t *testing.T) {
	s, _ := start(t, actNormal)

	if err := s.Check(t.Context(), fixture); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestALineThatCannotBeFramedIsRefusedOnceAndEndsTheSession(t *testing.T) {
	log := &kept{}

	s, _, err := open2(t, actGarbage, log)
	if err == nil {
		t.Fatal("a runtime that wrote a line no codec can frame was allowed to carry on")
	}

	if !errors.Is(err, ErrHandshake) {
		t.Errorf("Start returned %v, want the handshake it never finished", err)
	}

	told := heard(log.messages())
	if !strings.Contains(told, `"id":null`) || !strings.Contains(told, "parse_error") {
		t.Errorf("the engine told the runtime %q", told)
		t.Log("§2.6: one best-effort error carrying id null is written first, and it is the only")
		t.Log("place null is an id. It exists so a peer that sent something malformed learns what,")
		t.Log("rather than finding a closed pipe and an exit code.")
	}

	if _, err := s.Discovered(t.Context()); err == nil {
		t.Error("a call after the framing failure was answered")
		t.Log("The reader is spent. Reading on would leave whoever was waiting on the")
		t.Log("line that could not be read waiting forever, with the migration lock held.")
	}
}

func TestAResponseTheEngineIsNotWaitingOnEndsTheSession(t *testing.T) {
	s, _, err := open2(t, actStranger, &kept{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { _ = s.Close(context.Background()) })

	waitUntil(t, "the session to end", s.over)

	if !errors.Is(s.reason(), ErrProtocol) {
		t.Errorf("the session ended with %v, want %v", s.reason(), ErrProtocol)
		t.Log("§2.5: an id is unique within its own direction. A response nothing asked for means")
		t.Log("the two sequences crossed, and discarding it is how a mismatch stays silent.")
	}
}

func TestStderrIsFiledLineByLineAndReportedWithTheExit(t *testing.T) {
	log := &kept{}

	s, _, err := open2(t, actNoisy, log)
	if err == nil {
		t.Fatal("a runtime that died without answering was reported as having started")
	}

	waitUntil(t, "stderr to be filed", func() bool {
		return contains(log.messages(), "line 0 from a runtime")
	})

	closed := s.Close(context.Background())

	var exit *ExitError
	if !errors.As(errors.Join(err, closed), &exit) {
		t.Fatalf("neither Start nor Close reported the exit: %v, %v", err, closed)
	}

	if exit.Code != 3 {
		t.Errorf("the exit was reported as %d, want 3", exit.Code)
	}

	if len(exit.Stderr) != keptLines || !contains(exit.Stderr, "line 29") {
		t.Errorf("the failure carries %d lines: %v", len(exit.Stderr), exit.Stderr)
		t.Log("§9: the engine reports the exit code together with the last lines of stderr, which")
		t.Log("is the only reason stderr is buffered at all. The last lines are the ones that say")
		t.Log("why, and a bound is what keeps a runaway runtime from being kept in memory.")
	}
}

func TestTheEngineNumbersItsOwnRequestsFromOne(t *testing.T) {
	log := &kept{}

	s, _, err := open2(t, actNormal, log)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := s.Discovered(t.Context()); err != nil {
		t.Fatalf("Discovered: %v", err)
	}

	if err := s.Check(t.Context(), fixture); err != nil {
		t.Fatalf("Check: %v", err)
	}

	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	want := []string{
		"request 1 initialize",
		"request 2 migration.discover",
		"request 3 migration.validate",
		"request 4 shutdown",
	}

	if got := requests(log.messages()); !slices.Equal(got, want) {
		t.Errorf("the runtime was asked %v, want %v", got, want)
		t.Log("§2.4: both sides number from 1 and increment. An engine that reused one id would")
		t.Log("hand an SDK two live requests under one number, and §2.5's whole correlation rule")
		t.Log("rests on an id being unique within its own direction.")
	}
}

func TestARuntimeThatIgnoresSIGTERMIsKilled(t *testing.T) {
	c := newConn(t)

	s, err := Start(t.Context(), Options{
		Runtime: &fakeRuntime{act: actIgnores}, Project: project(t), Conn: c, Log: quiet(),
		Timeouts: Timeouts{ShutdownGrace: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	pid := s.proc.pid()

	if err := s.Close(t.Context()); err == nil {
		t.Error("a runtime that answered neither shutdown nor SIGTERM reported a clean close")
	}

	if !gone(pid) {
		t.Error("a runtime that trapped SIGTERM is still running")
		t.Log("§8.2: shutdown grace exceeded is SIGTERM, then SIGKILL 5s later. A ladder that")
		t.Log("stopped at the rung a process can ignore is not a ladder.")
	}
}

func TestACanceledCallIsAskedToStopAndTheSessionSurvivesIt(t *testing.T) {
	log := &kept{}
	c := newConn(t)

	s, err := Start(t.Context(), Options{
		Runtime: &fakeRuntime{act: actSlow}, Project: project(t), Conn: c, Log: slog.New(log),
		Timeouts: Timeouts{CancelGrace: 10 * time.Second, ShutdownGrace: brief},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { _ = s.Close(context.Background()) })

	ctx, stop := context.WithCancel(t.Context())
	stop()

	if _, err := s.Emit(ctx, fixture, ir.Up, engine.ModeApply); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Emit returned %v, want %v", err, ErrCanceled)
	}

	if !contains(log.messages(), "canceled ") {
		t.Error("the runtime was never told to stop")
		t.Log("§8: the receiver stops as soon as it can and answers the named request. Without")
		t.Log("the notification, a migration backfilling in chunks has no chance to stop between")
		t.Log("them and its resume point has to be reconstructed.")
	}

	if _, err := s.Discovered(t.Context()); err != nil {
		t.Errorf("the session did not survive a call the runtime agreed to abandon: %v", err)
		t.Log("§8.1: the kill is the guarantee and the notification is the courtesy. A runtime")
		t.Log("that stops when asked keeps its session, so the grace is never spent at all.")
	}
}

func TestTheCloseLadderDoesNotInheritTheCancellationThatTriggeredIt(t *testing.T) {
	log := &kept{}
	c := newConn(t)

	s, err := Start(t.Context(), Options{
		Runtime: &fakeRuntime{act: actSluggish}, Project: project(t), Conn: c, Log: slog.New(log),
		Timeouts: Timeouts{ShutdownGrace: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, stop := context.WithCancel(t.Context())
	stop()

	if err := s.Close(ctx); err != nil {
		t.Log("The unlock path had this discipline first: a release that inherits the")
		t.Log("cancellation fails exactly when it matters most. Ctrl-C is the common case here,")
		t.Log("and it is the one where the courtesy is worth most.")
		t.Fatalf("Close on a canceled context: %v", err)
	}

	if contains(log.messages(), "canceled ") {
		t.Error("closing canceled the shutdown it had just sent")
	}

	if !gone(s.proc.pid()) {
		t.Error("the runtime outlived a close on a canceled context")
	}
}

func TestARuntimeThatIgnoresShutdownIsEnded(t *testing.T) {
	c := newConn(t)

	s, err := Start(t.Context(), Options{
		Runtime: &fakeRuntime{act: actDeaf}, Project: project(t), Conn: c, Log: quiet(),
		Timeouts: Timeouts{ShutdownGrace: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	pid := s.proc.pid()

	if err := s.Close(t.Context()); err == nil {
		t.Error("a runtime that ignored shutdown reported a clean close")
	}

	if !gone(pid) {
		t.Error("a runtime that ignored shutdown is still running")
	}
}

func TestACallTheRuntimeAbandonsIsCanceledAndThenKilled(t *testing.T) {
	c := newConn(t)

	s, err := Start(t.Context(), Options{
		Runtime: &fakeRuntime{act: actStubborn}, Project: project(t), Conn: c, Log: quiet(),
		Timeouts: Timeouts{CancelGrace: 100 * time.Millisecond, ShutdownGrace: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	pid := s.proc.pid()
	ctx, stop := context.WithCancel(t.Context())

	go func() {
		time.Sleep(50 * time.Millisecond)
		stop()
	}()

	if _, err := s.Emit(ctx, fixture, ir.Up, engine.ModeApply); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Emit returned %v, want %v", err, ErrCanceled)
	}

	waitUntil(t, "the killed runtime to close its stdout", s.over)

	if !s.proc.waitFor(time.Second) || !gone(pid) {
		t.Error("the runtime that ignored $/cancel is still running")
	}

	t.Log("§8.1: the guarantee is the kill and the notification is the courtesy. A runtime that")
	t.Log("ignores $/cancel holds the migration lock until lock.timeout otherwise.")
}

func TestAMigrationExecuteIsBoundedByTheCallerAndByNothingElse(t *testing.T) {
	c := newConn(t)

	s, err := Start(t.Context(), Options{
		Runtime: &fakeRuntime{act: actSlow}, Project: project(t), Conn: c, Log: quiet(),
		Timeouts: Timeouts{Request: 50 * time.Millisecond, CancelGrace: time.Second},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { _ = s.Close(context.Background()) })

	done := make(chan error, 1)

	go func() {
		_, err := s.Emit(t.Context(), fixture, ir.Up, engine.ModeApply)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("migration.execute was bounded by a timer here and returned %v", err)
		t.Log("§8.2: a backfill over a large table legitimately runs for hours, and a default")
		t.Log("that killed it would be a data-loss bug wearing a safety flag.")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestARuntimeCannotOutliveTheEngineOrLeaveAChildBehind(t *testing.T) {
	log := &kept{}

	s, _, err := open2(t, actForks, log)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitUntil(t, "the grandchild to be reported", func() bool {
		return contains(log.messages(), "grandchild ")
	})

	grandchild := reportedPid(t, log.messages())
	pid := s.proc.pid()

	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !gone(pid) {
		t.Error("the runtime outlived its session")
	}

	waitUntil(t, "the grandchild to be ended with its group", func() bool { return gone(grandchild) })

	t.Log("§9: an orphaned runtime holding a database connection through host.* callbacks that")
	t.Log("nothing will answer is a leak with a lock behind it. The group is what dies, not the")
	t.Log("one process, so a runtime that forked cannot leave anything behind either.")
}

func TestASessionRefusesWhatItCouldNotStart(t *testing.T) {
	for _, c := range []struct {
		name string
		opts Options
		want error
	}{
		{"no runtime", Options{Conn: newConn(t), Log: quiet()}, ErrNoRuntime},
		{"no session", Options{Runtime: &fakeRuntime{}, Log: quiet()}, ErrNoConn},
		{"no logger", Options{Runtime: &fakeRuntime{}, Conn: newConn(t)}, ErrNoLog},
		{"no project", Options{
			Runtime: &fakeRuntime{}, Conn: newConn(t), Log: quiet(),
		}, ErrNoRoot},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Start(t.Context(), c.opts); !errors.Is(err, c.want) {
				t.Errorf("Start returned %v, want %v", err, c.want)
			}
		})
	}
}

func TestARuntimeThatCannotBeStartedIsReportedRatherThanWaitedFor(t *testing.T) {
	_, err := Start(t.Context(), Options{
		Runtime: &fakeRuntime{path: "/nonexistent/runtime"},
		Project: project(t), Conn: newConn(t), Log: quiet(),
	})
	if err == nil {
		t.Fatal("spawning a program that is not there was reported as a session")
	}

	if !strings.Contains(err.Error(), "/nonexistent/runtime") {
		t.Errorf("the failure does not name what could not be started: %v", err)
	}
}

// gone reports whether a process has ended, which is what signal 0 asks the kernel.
func gone(pid int) bool {
	return syscall.Kill(pid, 0) != nil
}

// reportedPid reads the grandchild's pid out of the line the fake wrote to stderr.
func reportedPid(t *testing.T, lines []string) int {
	t.Helper()

	for _, line := range lines {
		rest, found := strings.CutPrefix(line, "grandchild ")
		if !found {
			continue
		}

		var pid int
		if _, err := fmt.Sscan(rest, &pid); err == nil {
			return pid
		}
	}

	t.Fatal("the fake never reported a grandchild")

	return 0
}

func TestAPlannedMigrationsOwnStatementsComeBackHeldRatherThanRun(t *testing.T) {
	s, c := start(t, actBackfills)

	emitted, err := s.Emit(t.Context(), fixture, ir.Up, engine.ModePlan)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	t.Log("host.Call.Statements() had no reader until M6-T6, so migr plan over a Go migration")
	t.Log("would have omitted a statement migr up commits on its own.")

	if len(emitted.Direct) != 1 {
		t.Fatalf("Emit carried out %d statements, want the one the migration sent", len(emitted.Direct))
	}

	if emitted.Direct[0].SQL != "UPDATE users SET status = 'active'" {
		t.Errorf("the held statement is %q", emitted.Direct[0].SQL)
	}

	if !slices.Equal(emitted.Direct[0].Args, []string{"active"}) {
		t.Errorf("the held statement bound %v, want the arguments the migration sent",
			emitted.Direct[0].Args)
	}

	if len(c.performed) != 0 {
		t.Errorf("plan mode reached the database with %v", c.performed)
	}
}

func TestAnAppliedMigrationsOwnStatementsRunAndAreNotHeld(t *testing.T) {
	s, c := start(t, actBackfills)

	emitted, err := s.Emit(t.Context(), fixture, ir.Up, engine.ModeApply)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if len(emitted.Direct) != 0 {
		t.Errorf("an applied migration held %v, having already run it", emitted.Direct)
	}

	if len(c.performed) != 1 {
		t.Fatalf("apply mode reached the database %d times, want once", len(c.performed))
	}
}

func TestAnAnswerThatArrivedBeforeTheStreamEndedIsTheAnswer(t *testing.T) {
	for range 200 {
		s := &Session{done: make(chan struct{})}
		close(s.done)

		replies := make(chan *protocol.Message, 1)
		replies <- &protocol.Message{}

		if _, err := s.await(t.Context(), 1, replies, protocol.NoTimeout); err != nil {
			t.Fatalf("a runtime that answered and then exited was reported as having ended: %v", err)
		}
	}
}
