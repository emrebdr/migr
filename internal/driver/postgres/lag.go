package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/driver"
)

// lagQuery reads every standby this primary streams to: its name, its state, its measured replay lag,
// and how many bytes of WAL it has still to replay.
const lagQuery = `SELECT coalesce(application_name, ''), coalesce(host(client_addr), ''), state,
  extract(epoch FROM replay_lag)::float8, pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)::float8
FROM pg_stat_replication
ORDER BY application_name, client_addr`

// streaming is the one state a standby reports while it applies the primary's WAL as it arrives.
const streaming = "streaming"

// ErrLagHidden marks a standby whose state this user may not read, which only a monitoring role may.
var ErrLagHidden = errors.New("pg_stat_replication hides this standby's state from this user -- " +
	"grant it pg_read_all_stats (or pg_monitor) so migr can read the lag")

// ErrLagUnmeasured marks a standby with WAL still to replay and no replay lag measured yet.
var ErrLagUnmeasured = errors.New("the standby has WAL still to replay and its replay lag is not " +
	"measured yet")

// standby is one row of pg_stat_replication, as read for its lag.
type standby struct {
	// name is its application_name, which is what primary_conninfo calls it.
	name string
	// addr is where it connects from, the name of last resort.
	addr string
	// state is what it is doing, NULL where this user may not see it.
	state *string
	// lag is its measured replay lag in seconds, NULL where none is measured.
	lag *float64
	// pending is how many bytes of WAL it has still to replay, NULL where this user may not see it.
	pending *float64
}

// ReplicaLag reads how far behind each standby of this primary is, from pg_stat_replication.
func (c *pgConn) ReplicaLag(ctx context.Context) ([]driver.Lag, error) {
	rows, err := c.runner().Query(ctx, lagQuery)
	if err != nil {
		return nil, fmt.Errorf("reading pg_stat_replication: %w", err)
	}

	defer rows.Close()

	standbys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (standby, error) {
		var s standby

		return s, row.Scan(&s.name, &s.addr, &s.state, &s.lag, &s.pending)
	})
	if err != nil {
		return nil, fmt.Errorf("reading pg_stat_replication: %w", err)
	}

	lags := make([]driver.Lag, 0, len(standbys))
	for _, s := range standbys {
		lags = append(lags, s.read())
	}

	return lags, nil
}

// The order the rules are read in is what was measured on 16 and 17. A standby that has replayed
// every byte is caught up whatever replay_lag says: that column keeps its last measurement for about
// ten seconds after the writes stop -- 2.8s was read with nothing outstanding -- and a chunk held on a
// stale reading would wait for nothing. Only where WAL is outstanding is replay_lag the answer, and
// where it is outstanding and not yet measured the answer is unknown, which a pacer waits out.

// read turns one row into how far that standby trails, or why that cannot be said.
func (s standby) read() driver.Lag {
	lag := driver.Lag{Replica: s.named()}

	switch {
	case s.state == nil:
		lag.Err = ErrLagHidden
	case *s.state != streaming:
		lag.Err = fmt.Errorf("the standby is %s rather than streaming, so how far behind it is "+
			"cannot be read", *s.state)
	case s.pending != nil && *s.pending <= 0:
	case s.lag == nil:
		lag.Err = ErrLagUnmeasured
	default:
		lag.Behind = time.Duration(*s.lag * float64(time.Second))
	}

	return lag
}

// named is what an operator calls the standby: its application_name, else where it connects from.
func (s standby) named() string {
	switch {
	case s.name != "" && s.addr != "":
		return fmt.Sprintf("%s (%s)", s.name, s.addr)
	case s.name != "":
		return s.name
	case s.addr != "":
		return s.addr
	default:
		return "a standby with no name"
	}
}

// compile-time proof that this session reads its standbys' lag.
var _ driver.LagReader = (*pgConn)(nil)
