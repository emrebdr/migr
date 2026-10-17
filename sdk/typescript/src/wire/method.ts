// The methods docs/protocol.md defines, spelled as it spells them.
//
// Every method is a request except the two §2.4 names, because a caller that does not wait cannot
// know whether the work happened. `host.*` is reserved to the runtime-to-engine direction
// permanently: a runtime never serves one and the engine never sends one (§5.1).

/** PROTOCOL_VERSION is the version of this protocol the SDK speaks, which §10.1 describes. */
export const PROTOCOL_VERSION = 1;

/** IR_VERSION is the newest Operation IR version this SDK emits; an engine stating an older one still runs it. */
export const IR_VERSION = 3;

/** The methods the engine calls on a runtime, in the order §3 and §4 document them. */
export const INITIALIZE = "initialize";
export const MIGRATION_DISCOVER = "migration.discover";
export const MIGRATION_EXECUTE = "migration.execute";
export const MIGRATION_VALIDATE = "migration.validate";
export const SHUTDOWN = "shutdown";

/** CANCEL is the one method either side may send, and one of the two carrying no id. */
export const CANCEL = "$/cancel";

/** The methods a runtime calls back on the host, in the order §5 documents them. */
export const HOST_SCHEMA_APPLY = "host.schema.apply";
export const HOST_DB_QUERY = "host.db.query";
export const HOST_DB_EXEC = "host.db.exec";
export const HOST_CAPABILITY_GET = "host.capability.get";
export const HOST_INFO = "host.info";
export const HOST_LOG = "host.log";

/** ENGINE_METHODS are what the engine calls, in the order the specification documents them. */
export const ENGINE_METHODS = [
  INITIALIZE,
  MIGRATION_DISCOVER,
  MIGRATION_EXECUTE,
  MIGRATION_VALIDATE,
  SHUTDOWN,
] as const;

/** HOST_METHODS are what this SDK calls back, in the order the specification documents them. */
export const HOST_METHODS = [
  HOST_SCHEMA_APPLY,
  HOST_DB_QUERY,
  HOST_DB_EXEC,
  HOST_CAPABILITY_GET,
  HOST_INFO,
  HOST_LOG,
] as const;

/** NOTIFICATIONS are the only two methods that may arrive without an id, per §2.4. */
export const NOTIFICATIONS = [CANCEL, HOST_LOG] as const;

/** METHODS is every method this protocol version defines, in both directions. */
export const METHODS = [...ENGINE_METHODS, CANCEL, ...HOST_METHODS] as const;

/** notifies reports whether a method may be sent without an id rather than as a request. */
export function notifies(method: string): boolean {
  return (NOTIFICATIONS as readonly string[]).includes(method);
}

/** describeNotifications names the notification set for a message a human reads. */
export function describeNotifications(): string {
  return NOTIFICATIONS.join(" and ");
}
