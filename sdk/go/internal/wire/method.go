package wire

import (
	"slices"
	"strings"
)

// Version is the protocol version this SDK speaks, which docs/protocol.md specifies.
const Version = 1

// IRVersion is the Operation IR version this SDK emits, which initialize states rather than negotiates.
const IRVersion = 1

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

// hostMethods are what this SDK calls back, in the order the specification documents them.
var hostMethods = []string{
	HostSchemaApply,
	HostDBQuery,
	HostDBExec,
	HostCapabilityGet,
	HostInfo,
	HostLog,
}

// notifications are the only two methods that may arrive without an id, per §2.4.
var notifications = []string{Cancel, HostLog}

// Notifications returns every method that may be sent without an id, as a copy.
func Notifications() []string {
	return slices.Clone(notifications)
}

// Notifies reports whether a method may be sent as a notification rather than as a request.
func Notifies(method string) bool {
	return slices.Contains(notifications, method)
}

// describeNotifications names the notification set for a message a human reads.
func describeNotifications() string {
	return strings.Join(notifications, " and ")
}

// EngineMethods returns every method the engine calls on a runtime, as a copy.
func EngineMethods() []string {
	return slices.Clone(engineMethods)
}

// HostMethods returns every method this SDK calls back on the host, as a copy.
func HostMethods() []string {
	return slices.Clone(hostMethods)
}

// Methods returns every method this protocol version defines, in both directions, as a copy.
func Methods() []string {
	return slices.Concat(engineMethods, []string{Cancel}, hostMethods)
}
