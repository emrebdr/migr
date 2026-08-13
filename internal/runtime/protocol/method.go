package protocol

import "slices"

// Version is the protocol version this build speaks, which docs/protocol.md specifies.
const Version = 1

// The methods the engine calls on a runtime, spelled as docs/protocol.md spells them.
const (
	Initialize        = "initialize"
	MigrationDiscover = "migration.discover"
	MigrationExecute  = "migration.execute"
	MigrationValidate = "migration.validate"
	Shutdown          = "shutdown"
)

// Cancel is the one method either side may send, and the only request that carries no id.
const Cancel = "$/cancel"

// The methods a runtime calls back on the host, spelled as docs/protocol.md spells them.
const (
	HostSchemaApply   = "host.schema.apply"
	HostDBQuery       = "host.db.query"
	HostDBExec        = "host.db.exec"
	HostCapabilityGet = "host.capability.get"
	HostInfo          = "host.info"
	HostLog           = "host.log"
)

// engineMethods are what the engine calls, in the order the specification documents them.
var engineMethods = []string{
	Initialize,
	MigrationDiscover,
	MigrationExecute,
	MigrationValidate,
	Shutdown,
}

// hostMethods are what a runtime calls back, in the order the specification documents them.
var hostMethods = []string{
	HostSchemaApply,
	HostDBQuery,
	HostDBExec,
	HostCapabilityGet,
	HostInfo,
	HostLog,
}

// EngineMethods returns every method the engine calls on a runtime, as a copy.
func EngineMethods() []string {
	return slices.Clone(engineMethods)
}

// HostMethods returns every method a runtime calls back on the host, as a copy.
func HostMethods() []string {
	return slices.Clone(hostMethods)
}

// Methods returns every method this protocol version defines, in both directions, as a copy.
func Methods() []string {
	return slices.Concat(engineMethods, []string{Cancel}, hostMethods)
}

// Calls reports whether a method is one this protocol version defines.
func Calls(method string) bool {
	return slices.Contains(Methods(), method)
}
