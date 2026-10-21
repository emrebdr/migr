package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/capability"
)

// captureQuery reads the three things logical decoding needs: the WAL level, a role allowed to
// replicate, and how many replication slots are still free.
const captureQuery = `SELECT current_setting('wal_level'), r.rolsuper OR r.rolreplication,
  current_setting('max_replication_slots')::int - (SELECT count(*) FROM pg_replication_slots)::int,
  coalesce(current_setting('max_slot_wal_keep_size', true), '')
FROM pg_roles r WHERE r.rolname = current_user`

// logicalLevel is the one wal_level at which the server decodes its WAL into row changes.
const logicalLevel = "logical"

// captureAnswer is what the server reported about log-based change capture.
type captureAnswer struct {
	// level is wal_level.
	level string
	// replicates reports a superuser or a role with the REPLICATION attribute.
	replicates bool
	// free is how many replication slots max_replication_slots leaves unused.
	free int
	// keep is max_slot_wal_keep_size, how much WAL a slot may hold before the server invalidates it.
	keep string
}

// readCapture asks whether this server permits logical decoding, reading a failed answer as not permitted.
func readCapture(ctx context.Context, conn *pgx.Conn) capability.Capture {
	var a captureAnswer

	if err := conn.QueryRow(ctx, captureQuery).Scan(&a.level, &a.replicates, &a.free, &a.keep); err != nil {
		return capability.Lacks(fmt.Sprintf("an answer migr could read about wal_level, the "+
			"REPLICATION attribute and free replication slots (%v)", err))
	}

	return a.capture()
}

// capture names every setting and privilege the answer lacks, and permits capture where it lacks none.
func (a captureAnswer) capture() capability.Capture {
	var missing []string

	if a.level != logicalLevel {
		missing = append(missing, fmt.Sprintf("wal_level = logical (it is %s, and changing it "+
			"restarts the server)", a.level))
	}

	if !a.replicates {
		missing = append(missing, "a role with the REPLICATION attribute (this role has neither it "+
			"nor SUPERUSER)")
	}

	if a.free < 1 {
		missing = append(missing, "a free replication slot (max_replication_slots is used up)")
	}

	if len(missing) > 0 {
		return capability.Lacks(missing...)
	}

	return capability.Permits().Retains(a.retention())
}

// retention says how long a slot's WAL is kept for a stopped migration, which is what bounds carrying it on.
func (a captureAnswer) retention() string {
	switch a.keep {
	case "", "-1":
		return "a replication slot keeps every byte of WAL since it was last advanced, with no limit " +
			"(max_slot_wal_keep_size = -1), so a stopped migration can be carried on for as long as the " +
			"disk lasts -- and fills it if nothing does"
	default:
		return "a replication slot keeps at most " + a.keep + " of WAL (max_slot_wal_keep_size), past " +
			"which the server invalidates it and a stopped migration can only be abandoned"
	}
}
