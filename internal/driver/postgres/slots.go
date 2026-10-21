package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/driver"
)

// slotsQuery lists the logical replication slots on this database whose names migr derives, and the WAL each keeps.
const slotsQuery = `SELECT slot_name::text, active,
  coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn), 0)::bigint, %s
 FROM pg_replication_slots
 WHERE database = current_database() AND slot_type = 'logical' AND slot_name LIKE 'migr\_%%'
 ORDER BY slot_name`

// walStatusFrom is the release that reports how a slot's WAL is kept, as wal_status.
const walStatusFrom = 13

// CaptureResources returns every replication slot on this database a shadow-table migration's name could have made.
func (c *pgConn) CaptureResources(ctx context.Context) ([]driver.CaptureResource, error) {
	status := "''"
	if c.version.AtLeast(walStatusFrom) {
		status = "coalesce(wal_status, '')"
	}

	rows, err := c.conn.Query(ctx, fmt.Sprintf(slotsQuery, status))
	if err != nil {
		return nil, fmt.Errorf("reading the replication slots: %w", err)
	}

	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (driver.CaptureResource, error) {
		var r driver.CaptureResource

		return r, row.Scan(&r.Name, &r.Active, &r.Retained, &r.Status)
	})
	if err != nil {
		return nil, fmt.Errorf("reading the replication slots: %w", err)
	}

	return found, nil
}

// ReleaseCapture drops a replication slot and the publication of its name, which is nothing where both are gone.
func (c *pgConn) ReleaseCapture(ctx context.Context, name string) error {
	return release(ctx, c.conn, name)
}

// compile-time proof that this session lists and releases the slots its shadow-table migrations make.
var _ driver.CaptureKeeper = (*pgConn)(nil)
