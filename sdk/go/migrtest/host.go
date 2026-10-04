package migrtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	migr "github.com/emrebdr/migr/sdk/go"
	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

// The host a capture stands behind, whose answers are a copy of the engine's rather than an invention.
//
// Every answer here is the one internal/runtime/host gives for the same callback, which is what makes
// a capture a statement about a real run. The one callback that differs between the modes differs
// here too: host.db.exec is held under plan and answered as performed under apply, and in neither
// case did a statement reach a database -- there is none.
//
// Where it deliberately refuses rather than answers: a read, a capability, or a host.info this stage
// did not answer. A migration may branch on any of the three, and a zero value answered in place of a
// missing one sends it down a branch the test never meant to ask about -- which is the failure a
// capture exists to catch, not one it should manufacture.

// ErrUnstaged marks a question a migration asked that the stage it ran against does not answer.
var ErrUnstaged = errors.New("this capture staged no answer")

// Statement is one statement a migration sent itself, with whatever it bound to it.
type Statement struct {
	// SQL is the statement as the migration wrote it, never interpolated.
	SQL string
	// Args are the values bound to it, in the order they were bound.
	Args []string
}

// Line is one structured line a migration filed on the engine's own logger.
type Line struct {
	// Level is the severity the migration chose.
	Level migr.Level
	// Message is the line itself.
	Message string
	// Fields are whatever the migration attached to it.
	Fields map[string]string
}

// recorder answers the six callbacks docs/protocol.md §5 defines, and keeps what it was asked.
type recorder struct {
	// stage is what this host answers a migration's own questions with.
	stage Stage
	// mode is what this call is, which is the one thing an exec's answer depends on.
	mode migr.Mode

	// mu guards everything below, since a migration may emit from a goroutine of its own.
	mu sync.Mutex
	// operations are the IR documents this host recorded, in the order they were sent.
	operations []string
	// reads are the statements the migration read with, in order.
	reads []Statement
	// statements are the statements the migration sent itself, in order.
	statements []Statement
	// logs are the lines it filed.
	logs []Line
	// refusal is the first answer this host would not give, which fails the capture either way.
	refusal error
}

// Call answers one callback the way the engine's host answers it, recording what it was asked.
func (r *recorder) Call(_ context.Context, method string, params, target any) error {
	switch method {
	case wire.HostSchemaApply:
		return r.apply(params, target)
	case wire.HostDBQuery:
		return r.query(params, target)
	case wire.HostDBExec:
		return r.exec(params, target)
	case wire.HostCapabilityGet:
		return r.capability(params, target)
	case wire.HostInfo:
		return r.info(target)
	default:
		return r.refuse(fmt.Errorf("%q is not a callback this protocol version defines", method))
	}
}

// Notify records the one notification a migration sends, which nothing answers.
func (r *recorder) Notify(method string, params any) error {
	if method != wire.HostLog {
		return r.refuse(fmt.Errorf("%q is not a notification this protocol version defines", method))
	}

	p, ok := params.(wire.LogParams)
	if !ok {
		return r.refuse(carried(method, params))
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.logs = append(r.logs, Line{
		Level: migr.Level(p.Level), Message: p.Message, Fields: p.Fields,
	})

	return nil
}

// apply records the operations a migration emits, in both modes, as §6 requires of a host.
func (r *recorder) apply(params, target any) error {
	p, ok := params.(wire.SchemaApplyParams)
	if !ok {
		return r.refuse(carried(wire.HostSchemaApply, params))
	}

	recorded, ok := target.(*wire.SchemaApplyResult)
	if !ok {
		return r.refuse(expected(wire.HostSchemaApply, target))
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, document := range p.Operations {
		r.operations = append(r.operations, string(document))
	}

	recorded.Recorded = len(p.Operations)

	return nil
}

// query answers a migration's own read from the stage, refusing one the stage does not answer.
func (r *recorder) query(params, target any) error {
	p, ok := params.(wire.DBQueryParams)
	if !ok {
		return r.refuse(carried(wire.HostDBQuery, params))
	}

	rows, ok := target.(*wire.DBQueryResult)
	if !ok {
		return r.refuse(expected(wire.HostDBQuery, target))
	}

	r.note(&r.reads, Statement{SQL: p.SQL, Args: p.Args})

	answer, staged := r.stage.Reads[p.SQL]
	if !staged {
		return r.refuse(fmt.Errorf("%w for the read %q: stage it in Stage.Reads, so this "+
			"migration branches on what the test meant rather than on an empty result",
			ErrUnstaged, p.SQL))
	}

	rows.Columns = answer.Columns
	rows.Rows = answer.Values

	return nil
}

// exec records a migration's own statement and answers it as the engine's host does, per mode.
func (r *recorder) exec(params, target any) error {
	p, ok := params.(wire.DBExecParams)
	if !ok {
		return r.refuse(carried(wire.HostDBExec, params))
	}

	outcome, ok := target.(*wire.DBExecResult)
	if !ok {
		return r.refuse(expected(wire.HostDBExec, target))
	}

	r.note(&r.statements, Statement{SQL: p.SQL, Args: p.Args})

	if r.mode == migr.ModePlan {
		outcome.RowsAffected = "0"
		outcome.Executed = false

		return nil
	}

	outcome.RowsAffected = r.affected(p.SQL)
	outcome.Executed = true

	return nil
}

// affected reports what a statement is staged as having changed, which is nothing by default.
func (r *recorder) affected(sql string) string {
	if staged, ok := r.stage.Affected[sql]; ok {
		return staged
	}

	return "0"
}

// capability answers what the target engine makes of one operation, from the stage alone.
func (r *recorder) capability(params, target any) error {
	p, ok := params.(wire.CapabilityGetParams)
	if !ok {
		return r.refuse(carried(wire.HostCapabilityGet, params))
	}

	verdict, ok := target.(*wire.CapabilityGetResult)
	if !ok {
		return r.refuse(expected(wire.HostCapabilityGet, target))
	}

	kind := kindOf(string(p.Operation))

	staged, answered := r.stage.Verdicts[kind]
	if !answered {
		return r.refuse(fmt.Errorf("%w for a capability question about %s: stage it in "+
			"Stage.Verdicts, since only a connected engine knows the answer", ErrUnstaged, kind))
	}

	verdict.Support = staged.Support
	verdict.Strategy = staged.Strategy
	verdict.Reason = staged.Reason
	verdict.Lock = staged.Lock

	return nil
}

// info answers what this run is against, with the mode this call is rather than a staged one.
func (r *recorder) info(target any) error {
	facts, ok := target.(*wire.InfoResult)
	if !ok {
		return r.refuse(expected(wire.HostInfo, target))
	}

	if r.stage.Info == nil {
		return r.refuse(fmt.Errorf("%w for host.info: stage it in Stage.Info, since a migration "+
			"that branches on the driver or the server version would otherwise read empty strings",
			ErrUnstaged))
	}

	facts.Database = r.stage.Info.Database
	facts.Driver = r.stage.Info.Driver
	facts.Server = r.stage.Info.Server
	facts.TransactionalDDL = r.stage.Info.TransactionalDDL
	facts.Mode = wire.Mode(r.mode)

	return nil
}

// note appends one callback to the list that holds it, which a migration may reach concurrently.
func (r *recorder) note(into *[]Statement, s Statement) {
	r.mu.Lock()
	defer r.mu.Unlock()

	*into = append(*into, s)
}

// refuse records the first answer this host could not give, and reports it to the migration.
func (r *recorder) refuse(err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.refusal == nil {
		r.refusal = err
	}

	return err
}

// captured reports everything this host was asked, which is what one half of a migration did.
func (r *recorder) captured() Capture {
	r.mu.Lock()
	defer r.mu.Unlock()

	return Capture{
		Operations: r.operations,
		Reads:      r.reads,
		Statements: r.statements,
		Logs:       r.logs,
	}
}

// problem reports the first answer this host refused, whatever the migration made of it.
func (r *recorder) problem() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.refusal
}

// kindOf names the operation a document is, which is the one member every one of them carries.
func kindOf(document string) migr.Kind {
	var named struct {
		Op migr.Kind `json:"op"`
	}

	_ = json.Unmarshal([]byte(document), &named)

	return named.Op
}

// carried reports a callback whose parameters were not the ones that method carries.
func carried(method string, params any) error {
	return fmt.Errorf("%s carried %T, which is not the payload that method sends", method, params)
}

// expected reports a callback that asked for an answer of a type that method does not return.
func expected(method string, target any) error {
	return fmt.Errorf("%s asked for %T back, which is not the result that method returns",
		method, target)
}
