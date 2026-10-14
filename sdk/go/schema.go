package migr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

// Mode is whether this call runs the migration or asks it what it would do.
type Mode string

// The two modes the engine asks a migration in, spelled as docs/protocol.md §6 spells them.
const (
	ModeApply Mode = "apply"
	ModePlan  Mode = "plan"
)

// Direction is which way a migration is being run.
type Direction string

// The two directions every migration has.
const (
	Up   Direction = "up"
	Down Direction = "down"
)

// Level is a log line's severity, which the engine maps onto its own logger.
type Level string

// The four levels host.log carries.
const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Schema is the window a migration acts through, and every callback it may make.
type Schema struct {
	host Host
	// migration is the version this window was opened for.
	migration string
	// direction is which way it is being run.
	direction Direction
	// mode is whether this call runs the migration or asks it, read from this call alone.
	mode Mode

	// mu guards the emitted count, the builders handed out, and whether the window is still open.
	mu sync.Mutex
	// emitted is how many operations this window has recorded, which the result reports.
	emitted int
	// built is every builder this window handed out, which seal holds to having been applied.
	built []*builder
	// closed reports whether the window has ended, after which no callback is valid.
	closed bool
}

// newSchema opens the window one migration.execute acts through.
func newSchema(h Host, migration string, dir Direction, mode Mode) *Schema {
	return &Schema{host: h, migration: migration, direction: dir, mode: mode}
}

// Migration returns the version this call is running, which is what every message names it by.
func (s *Schema) Migration() string {
	return s.migration
}

// Direction reports which way this call is running the migration.
func (s *Schema) Direction() Direction {
	return s.direction
}

// Mode reports whether this call runs the migration or asks it, read from this call and no other.
func (s *Schema) Mode() Mode {
	return s.mode
}

// Planning reports whether this call is a question, which no invocation turns into a run.
func (s *Schema) Planning() bool {
	return s.mode == ModePlan
}

// Table opens a change to an existing table, which its own Apply emits.
func (s *Schema) Table(name string) *Table {
	return &Table{build: s.builder(fmt.Sprintf("table %q", name)), name: name}
}

// CreateTable opens a table being created, which states its key inline (docs/ir.md §7.2).
func (s *Schema) CreateTable(name string) *NewTable {
	op := &CreateTable{Table: name}
	build := s.builder(fmt.Sprintf("table %q", name))
	build.add(op)

	return &NewTable{build: build, op: op}
}

// View opens a change to a view, which its own Apply emits.
func (s *Schema) View(name string) *View {
	return &View{build: s.builder(fmt.Sprintf("view %q", name)), name: name}
}

// Raw opens raw statements addressed to one engine by name, which no other engine will run.
func (s *Schema) Raw(driver, sql string) *Statements {
	statements := &Statements{build: s.builder("raw statements")}

	return statements.Raw(driver, sql)
}

// builder registers a builder with this window, so seal can hold it to having been applied.
func (s *Schema) builder(subject string) *builder {
	build := &builder{schema: s, subject: subject}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.built = append(s.built, build)

	return build
}

// Apply emits operations, which the engine records in both modes and compiles for its own driver.
func (s *Schema) Apply(ctx context.Context, ops ...Operation) error {
	if len(ops) == 0 {
		return nil
	}

	if err := s.carried(ops); err != nil {
		return err
	}

	documents, err := documentsOf(ops)
	if err != nil {
		return err
	}

	var recorded wire.SchemaApplyResult

	if err := s.call(ctx, wire.HostSchemaApply,
		wire.SchemaApplyParams{Operations: documents}, &recorded); err != nil {
		return err
	}

	return s.record(recorded.Recorded, len(ops))
}

// ErrNewerIR marks an operation newer than the Operation IR the engine on the other end reads.
var ErrNewerIR = errors.New("the engine reads an older Operation IR than this operation needs")

// irStater is a host that knows which irVersion its engine stated, which a session always is.
type irStater interface {
	statedIR() int
}

// carried refuses an operation the engine stated it cannot read, before anything is sent.
func (s *Schema) carried(ops []Operation) error {
	stated := wire.IRVersion
	if host, knows := s.host.(irStater); knows {
		stated = host.statedIR()
	}

	for i, op := range ops {
		if op != nil && op.Kind().Since() > stated {
			return fmt.Errorf("%w: operation %d is %s, which needs irVersion %d, and the engine "+
				"stated %d -- upgrade migr to run it", ErrNewerIR, i+1, op.Kind(), op.Kind().Since(),
				stated)
		}
	}

	return nil
}

// documentsOf renders each operation as the IR document that crosses the wire verbatim.
func documentsOf(ops []Operation) ([]json.RawMessage, error) {
	documents := make([]json.RawMessage, 0, len(ops))

	for i, op := range ops {
		document, err := documentOf(op)
		if err != nil {
			return nil, fmt.Errorf("%w %d%s: %w", ErrInvalidOperation, i+1, labelOf(op), err)
		}

		documents = append(documents, document)
	}

	return documents, nil
}

// record adds what the host accepted to this window's count, refusing a count that disagrees.
func (s *Schema) record(recorded, sent int) error {
	if recorded != sent {
		return fmt.Errorf("the host recorded %d of the %d operations sent", recorded, sent)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.emitted += recorded

	return nil
}

// Rows is what a read returned: every value as the server's own text, and nil where it was null.
type Rows struct {
	// Columns names each column, in the order the statement selected them.
	Columns []string
	// Values holds one slice per row, each as wide as Columns.
	Values [][]*string
}

// Len reports how many rows came back.
func (r Rows) Len() int {
	return len(r.Values)
}

// Query runs this migration's own read, which the driver holds to being one, in both modes.
func (s *Schema) Query(ctx context.Context, sql string, args ...string) (Rows, error) {
	var answered wire.DBQueryResult

	if err := s.call(ctx, wire.HostDBQuery,
		wire.DBQueryParams{SQL: sql, Args: bound(args)}, &answered); err != nil {
		return Rows{}, err
	}

	return Rows{Columns: answered.Columns, Values: answered.Rows}, nil
}

// Result is what a statement changed, and whether it ran at all.
type Result struct {
	// RowsAffected is what the server reported, as its own text, since a count may exceed 2^53.
	RowsAffected string
	// Executed reports whether the statement reached the database, which a preview does not.
	Executed bool
}

// Exec runs this migration's own statement, which a preview records rather than runs.
func (s *Schema) Exec(ctx context.Context, sql string, args ...string) (Result, error) {
	var answered wire.DBExecResult

	if err := s.call(ctx, wire.HostDBExec,
		wire.DBExecParams{SQL: sql, Args: bound(args)}, &answered); err != nil {
		return Result{}, err
	}

	return Result{RowsAffected: answered.RowsAffected, Executed: answered.Executed}, nil
}

// Verdict is what the target engine makes of one operation, on both axes at once.
type Verdict struct {
	// Support is whether the engine performs it directly, achieves it another way, or refuses it.
	Support string
	// Strategy is how an emulation is achieved, and empty for an operation performed directly.
	Strategy string
	// Reason is why an operation is refused, and empty for one that is not.
	Reason string
	// Lock is what the operation costs a live database, which may be unknown.
	Lock string
}

// Capability asks what the target engine makes of one whole operation, before emitting it.
func (s *Schema) Capability(ctx context.Context, op Operation) (Verdict, error) {
	document, err := documentOf(op)
	if err != nil {
		return Verdict{}, fmt.Errorf("%w%s: %w", ErrInvalidOperation, labelOf(op), err)
	}

	var answered wire.CapabilityGetResult

	if err := s.call(ctx, wire.HostCapabilityGet,
		wire.CapabilityGetParams{Operation: document}, &answered); err != nil {
		return Verdict{}, err
	}

	return Verdict{
		Support: answered.Support, Strategy: answered.Strategy,
		Reason: answered.Reason, Lock: answered.Lock,
	}, nil
}

// Info is what this run is against, which a migration may branch on.
type Info struct {
	// Database is the name migr.yaml holds the target under.
	Database string
	// Driver is the engine that will compile the operations.
	Driver string
	// Server is the version that engine reported at connect.
	Server string
	// TransactionalDDL reports whether this engine can roll a failed schema change back.
	TransactionalDDL bool
	// Mode is whether this call runs the migration or asks it.
	Mode Mode
}

// Info asks what this run is against, which the host answers from the connection it holds.
func (s *Schema) Info(ctx context.Context) (Info, error) {
	var answered wire.InfoResult

	if err := s.call(ctx, wire.HostInfo, struct{}{}, &answered); err != nil {
		return Info{}, err
	}

	return Info{
		Database: answered.Database, Driver: answered.Driver, Server: answered.Server,
		TransactionalDDL: answered.TransactionalDDL, Mode: Mode(answered.Mode),
	}, nil
}

// Log files one line on the engine's own logger, which nothing answers and nothing waits on.
func (s *Schema) Log(level Level, message string, fields map[string]string) {
	if s.over() {
		return
	}

	_ = s.host.Notify(wire.HostLog, wire.LogParams{
		Level: wire.Level(level), Message: message, Fields: fields,
	})
}

// call makes one callback, refusing it once the window this migration acts through has ended.
func (s *Schema) call(ctx context.Context, method string, params, target any) error {
	if s.over() {
		return fmt.Errorf("%w: %s after %s finished", ErrClosed, method, s.migration)
	}

	return s.host.Call(ctx, method, params, target)
}

// over reports whether the window has ended, after which no callback is valid (§5.1).
func (s *Schema) over() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closed
}

// seal closes the window, reporting how many operations it recorded and what it never applied.
func (s *Schema) seal() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true

	return s.emitted, unapplied(s.built)
}

// unapplied reports every change built and never applied, which is a migration that emits nothing.
func unapplied(built []*builder) error {
	var forgotten []string

	for _, build := range built {
		if build.held() > 0 {
			forgotten = append(forgotten, fmt.Sprintf("%d to %s", build.held(), build.subject))
		}
	}

	if len(forgotten) == 0 {
		return nil
	}

	return fmt.Errorf("%w: %s", ErrNotApplied, strings.Join(forgotten, ", "))
}

// bound renders a statement's arguments, since an absent list crosses the wire as an empty one.
func bound(args []string) []string {
	if args == nil {
		return []string{}
	}

	return args
}
