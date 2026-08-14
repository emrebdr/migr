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
