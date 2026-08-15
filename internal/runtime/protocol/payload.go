package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/emrebdr/migr/internal/ir"
)

// Mode is whether a migration.execute is a run or a preview, which §6 says changes one callback.
type Mode string

// The two modes docs/protocol.md §4 closes migration.execute to.
const (
	ModeApply Mode = "apply"
	ModePlan  Mode = "plan"
)

// modes lists every accepted mode.
var modes = []Mode{ModeApply, ModePlan}

// Modes returns every mode this protocol version defines, as a copy.
func Modes() []Mode {
	return slices.Clone(modes)
}

// Valid reports whether a mode is one of the two this protocol version defines.
func (m Mode) Valid() bool {
	return slices.Contains(modes, m)
}

// Level is a log line's severity, which §5 closes to four values.
type Level string

// The four levels host.log carries, which the engine maps onto its own logger.
const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// levels lists every accepted level.
var levels = []Level{LevelDebug, LevelInfo, LevelWarn, LevelError}

// Levels returns every log level this protocol version defines, as a copy.
func Levels() []Level {
	return slices.Clone(levels)
}

// Valid reports whether a level is one of the four this protocol version defines.
func (l Level) Valid() bool {
	return slices.Contains(levels, l)
}

// InitializeParams is the handshake: every version the engine speaks, and the project it opened.
type InitializeParams struct {
	ProtocolVersions []int        `json:"protocolVersions"`
	IRVersion        int          `json:"irVersion"`
	Client           Client       `json:"client"`
	Migrations       MigrationsAt `json:"migrations"`
	Database         DatabaseAt   `json:"database"`
}

// Client names the engine and its version, which a runtime reports in its own diagnostics.
type Client struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MigrationsAt names where the migrations are and which language they were written in.
type MigrationsAt struct {
	Dir      string `json:"dir"`
	Language string `json:"language"`
}

// DatabaseAt names the target a migration may branch on, and carries no DSN, ever.
type DatabaseAt struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
}

// InitializeResult is the version the runtime picked, and what implements it.
type InitializeResult struct {
	ProtocolVersion int       `json:"protocolVersion"`
	Runtime         Component `json:"runtime"`
	SDK             Component `json:"sdk"`
}

// Component names one half of what answered the handshake: the language runtime, or the SDK.
type Component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DiscoverResult is every migration a runtime can execute, with what each one declares.
type DiscoverResult struct {
	Migrations []Discovered `json:"migrations"`
}

// Discovered is one migration a runtime loaded, named by the version its filename carries.
type Discovered struct {
	Version      string   `json:"version"`
	File         string   `json:"file"`
	Dependencies []string `json:"dependencies"`
}

// ExecuteParams names one migration, one direction, and whether this call runs it or asks it.
type ExecuteParams struct {
	ID        string       `json:"id"`
	Direction ir.Direction `json:"direction"`
	Mode      Mode         `json:"mode"`
}

// Validate reports a call that could not name what it is asking for.
func (p ExecuteParams) Validate() error {
	if p.ID == "" {
		return errors.New("id is empty, and a migration.execute names one migration")
	}

	if !p.Direction.Valid() {
		return fmt.Errorf("direction is %q, and a migration runs up or down", p.Direction)
	}

	if !p.Mode.Valid() {
		return fmt.Errorf("mode is %q, and this protocol version defines %v", p.Mode, Modes())
	}

	return nil
}

// ExecuteResult reports how many operations were emitted, which the engine checks its own count against.
type ExecuteResult struct {
	Operations int `json:"operations"`
}

// ValidateParams names the migration whose own check is being run.
type ValidateParams struct {
	ID string `json:"id"`
}

// CancelParams names a request the sender made and is still waiting on.
type CancelParams struct {
	ID int64 `json:"id"`
}

// SchemaApplyParams carries the operations a migration performs, as the IR array alone.
type SchemaApplyParams struct {
	Operations ir.Operations `json:"operations"`
}

// SchemaApplyResult reports how many operations this call added to the migration's list.
type SchemaApplyResult struct {
	Recorded int `json:"recorded"`
}

// DBQueryParams is a migration's own read: its statement and its bound parameters.
type DBQueryParams struct {
	SQL  string   `json:"sql"`
	Args []string `json:"args"`
}

// Validate reports a read with no statement to run.
func (p DBQueryParams) Validate() error {
	return checkSQL(p.SQL)
}

// DBQueryResult is what a read returned, every value text or null for the reason §5 gives.
type DBQueryResult struct {
	Columns []string    `json:"columns"`
	Rows    [][]*string `json:"rows"`
}

// DBExecParams is a migration's own statement, the escape hatch the Operation IR cannot express.
type DBExecParams struct {
	SQL  string   `json:"sql"`
	Args []string `json:"args"`
}

// Validate reports a statement with nothing to run.
func (p DBExecParams) Validate() error {
	return checkSQL(p.SQL)
}

// DBExecResult reports what the statement changed, and whether it ran at all.
type DBExecResult struct {
	RowsAffected string `json:"rowsAffected"`
	Executed     bool   `json:"executed"`
}

// CapabilityGetParams asks about a whole operation, since a driver refuses payloads and not kinds.
type CapabilityGetParams struct {
	Operation ir.Operation `json:"operation"`
}

// Validate reports a question that names no operation to ask about.
func (p CapabilityGetParams) Validate() error {
	if p.Operation == nil {
		return errors.New("operation is missing, and a capability is answered per operation")
	}

	return nil
}

// UnmarshalJSON decodes the operation through the IR's own discriminated decoder.
func (p *CapabilityGetParams) UnmarshalJSON(data []byte) error {
	var raw struct {
		Operation json.RawMessage `json:"operation"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("reading the operation: %w", err)
	}

	if len(raw.Operation) == 0 {
		return nil
	}

	op, err := ir.UnmarshalOperation(raw.Operation)
	if err != nil {
		return fmt.Errorf("reading the operation: %w", err)
	}

	p.Operation = op

	return nil
}

// CapabilityGetResult is what the target engine makes of one operation, on both axes at once.
type CapabilityGetResult struct {
	Support  string `json:"support"`
	Strategy string `json:"strategy,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Lock     string `json:"lock"`
}

// InfoResult reports the facts about this run a migration may branch on.
type InfoResult struct {
	Database         string `json:"database"`
	Driver           string `json:"driver"`
	Server           string `json:"server"`
	TransactionalDDL bool   `json:"transactionalDDL"`
	Mode             Mode   `json:"mode"`
}

// LogParams is one structured line addressed to the engine's own logger.
type LogParams struct {
	Level   Level             `json:"level"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// Validate reports a log line the engine could not file or could not print.
func (p LogParams) Validate() error {
	if !p.Level.Valid() {
		return fmt.Errorf("level is %q, and this protocol version defines %v", p.Level, Levels())
	}

	if p.Message == "" {
		return errors.New("message is empty, and a log line with nothing to say is not one")
	}

	return nil
}

// MarshalPayload renders a params or result payload the way this protocol spells one.
func MarshalPayload(v any) (json.RawMessage, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding the payload: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// UnmarshalPayload decodes a payload, ignoring members it does not know, as §10.2 requires.
func UnmarshalPayload(data []byte, target any) error {
	if len(data) == 0 {
		data = json.RawMessage("{}")
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("reading the parameters: %w", err)
	}

	return nil
}

// checkSQL reports a call that carries no statement, which no engine can be asked to run.
func checkSQL(sql string) error {
	if sql == "" {
		return errors.New("sql is empty, and there is nothing to run")
	}

	return nil
}
