package wire

import "slices"

// Code is one of the application error codes docs/protocol.md §7 assigns.
type Code int

// The codes a runtime reports to the engine.
const (
	// CodeUnsupportedProtocol marks a runtime that speaks none of the versions the engine offered.
	CodeUnsupportedProtocol Code = -32001
	// CodeUnsupportedIR marks a runtime that cannot emit the irVersion the engine stated.
	CodeUnsupportedIR Code = -32002
	// CodeSessionState marks a method that arrived when the session could not answer it.
	CodeSessionState Code = -32003
	// CodeUnknownMigration marks an id naming no migration this runtime loaded.
	CodeUnknownMigration Code = -32010
	// CodeMigrationFailed marks a migration whose own code raised, which is the ordinary failure.
	CodeMigrationFailed Code = -32011
	// CodeNoDownAuthored marks a migration carrying no down of its own, for the engine to derive one.
	CodeNoDownAuthored Code = -32012
	// CodeDeclaredIrreversible marks a migration whose author stated it cannot be undone.
	CodeDeclaredIrreversible Code = -32013
	// CodeIrreversibleOperation marks an operation the runtime could not invert.
	CodeIrreversibleOperation Code = -32014
)

// The codes the host reports to a runtime.
const (
	// CodeNotReadOnly marks a host.db.query given a statement that could write.
	CodeNotReadOnly Code = -32020
	// CodeInvalidOperation marks a schema operation that is not a valid IR document.
	CodeInvalidOperation Code = -32021
	// CodeDatabase marks a statement the server itself refused.
	CodeDatabase Code = -32022
)

// CodeCanceled marks a request abandoned because the other side asked for it, and either may send it.
const CodeCanceled Code = -32030

// The codes JSON-RPC 2.0 defines for itself, used exactly as it defines them.
const (
	// CodeParse marks a line that is not JSON, which includes one that is not framed.
	CodeParse Code = -32700
	// CodeInvalidRequest marks a line that is JSON and is not a message this protocol defines.
	CodeInvalidRequest Code = -32600
	// CodeMethodNotFound marks a method this protocol version does not define.
	CodeMethodNotFound Code = -32601
	// CodeInvalidParams marks parameters of the wrong shape, or a required one missing.
	CodeInvalidParams Code = -32602
	// CodeInternal marks a failure with no better description.
	CodeInternal Code = -32603
)

// slugs name each code in a form an SDK can branch on without writing the number down.
var slugs = map[Code]string{
	CodeParse:                 "parse_error",
	CodeInvalidRequest:        "invalid_request",
	CodeMethodNotFound:        "method_not_found",
	CodeInvalidParams:         "invalid_params",
	CodeInternal:              "internal_error",
	CodeUnsupportedProtocol:   "unsupported_protocol",
	CodeUnsupportedIR:         "unsupported_ir",
	CodeSessionState:          "session_state",
	CodeUnknownMigration:      "unknown_migration",
	CodeMigrationFailed:       "migration_failed",
	CodeNoDownAuthored:        "no_down_authored",
	CodeDeclaredIrreversible:  "declared_irreversible",
	CodeIrreversibleOperation: "irreversible_operation",
	CodeNotReadOnly:           "not_read_only",
	CodeInvalidOperation:      "invalid_operation",
	CodeDatabase:              "database",
	CodeCanceled:              "canceled",
}

// codes lists every application code, in the order the specification tabulates them.
var codes = []Code{
	CodeUnsupportedProtocol,
	CodeUnsupportedIR,
	CodeSessionState,
	CodeUnknownMigration,
	CodeMigrationFailed,
	CodeNoDownAuthored,
	CodeDeclaredIrreversible,
	CodeIrreversibleOperation,
	CodeNotReadOnly,
	CodeInvalidOperation,
	CodeDatabase,
	CodeCanceled,
}

// Codes returns every application code this protocol version defines, as a copy.
func Codes() []Code {
	return slices.Clone(codes)
}

// String renders the slug §7.1 requires every error carry, or the number when there is none.
func (c Code) String() string {
	if slug, ok := slugs[c]; ok {
		return slug
	}

	return "unknown"
}
