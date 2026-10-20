package mysql

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	lib "github.com/go-sql-driver/mysql"

	"github.com/emrebdr/migr/internal/driver"
)

// lagWait bounds one replica's lag read, so an unreachable replica holds a chunk rather than hanging it.
const lagWait = 5 * time.Second

// errNoPrivilege is ER_SPECIFIC_ACCESS_DENIED_ERROR, which SHOW REPLICA STATUS answers a user without the grant.
const errNoPrivilege = 1227

// ErrNotAReplica marks a configured replica that reports replicating from nothing.
var ErrNotAReplica = errors.New("SHOW REPLICA STATUS answers no row, so this server is not a replica")

// ErrNoReplicaGrant marks a replica the configured user may not ask for its status.
var ErrNoReplicaGrant = errors.New("this user may not read the replica's status -- grant it " +
	"REPLICATION CLIENT on the replica")

// ErrReplicationStopped marks a replica whose IO or SQL thread is not running.
var ErrReplicationStopped = errors.New("replication is stopped")

// ErrNoLagReported marks a running replica that reports no lag at all.
var ErrNoLagReported = errors.New("the replica reports no lag")

// replica is one server migr.yaml names as a replica of this one, opened the first time its lag is read.
type replica struct {
	// name is its address, which is what an operator recognizes and carries no credential.
	name string
	// connector is how to reach it, built from its connection string at Open.
	connector sqldriver.Connector
	// db is its pool, nil until its lag is first read.
	db *sql.DB
}

// replicasOf parses every configured replica's connection string, naming each by its address alone.
func replicasOf(dsns []string) ([]*replica, error) {
	found := make([]*replica, 0, len(dsns))

	for i, dsn := range dsns {
		config, err := lib.ParseDSN(dsn)
		if err != nil {
			return nil, &DSNError{Err: err, Detail: fmt.Sprintf("replica %d could not be read; the form "+
				"is user:password@tcp(host:port)/database", i+1)}
		}

		config.Timeout = lagWait

		connector, err := lib.NewConnector(config)
		if err != nil {
			return nil, &DSNError{Err: err, Detail: fmt.Sprintf("replica %d could not be read", i+1)}
		}

		found = append(found, &replica{name: config.Addr, connector: connector})
	}

	return found, nil
}

// ReplicaLag reads how far behind each configured replica is, from that replica's own status.
func (c *mysqlConn) ReplicaLag(ctx context.Context) ([]driver.Lag, error) {
	lags := make([]driver.Lag, 0, len(c.replicas))

	for _, r := range c.replicas {
		lags = append(lags, r.read(ctx))
	}

	return lags, nil
}

// read asks one replica how far behind it is, or says why it could not.
func (r *replica) read(ctx context.Context) driver.Lag {
	bound, cancel := context.WithTimeout(ctx, lagWait)
	defer cancel()

	status, err := r.status(bound)
	if err != nil {
		return driver.Lag{Replica: r.name, Err: describeUnread(err)}
	}

	return readStatus(r.name, status)
}

// describeUnread turns a failed status read into the reason an operator can act on.
func describeUnread(err error) error {
	if hasNumber(err, errNoPrivilege) {
		return ErrNoReplicaGrant
	}

	return fmt.Errorf("it could not be reached: %w", err)
}

// status opens the replica where it is not yet open and reads its status row, empty where it has none.
func (r *replica) status(ctx context.Context) (map[string]string, error) {
	if r.db == nil {
		r.db = sql.OpenDB(r.connector)
		r.db.SetMaxOpenConns(1)
	}

	var reported, comment string
	if err := r.db.QueryRowContext(ctx, versionQuery).Scan(&reported, &comment); err != nil {
		return nil, err
	}

	version, err := parseVersion(reported, comment)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, statusQuery(version))
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	return firstRow(rows)
}

// statusQuery is the statement a replica of this release answers with its status.
func statusQuery(v serverVersion) string {
	if v.AtLeastPatch(MySQL, 8, 0, 22) || v.AtLeastPatch(MariaDB, 10, 5, 1) {
		return "SHOW REPLICA STATUS"
	}

	return "SHOW SLAVE STATUS"
}

// firstRow reads a status row by column name, which differs between releases and variants.
func firstRow(rows *sql.Rows) (map[string]string, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	if !rows.Next() {
		return map[string]string{}, rows.Err()
	}

	values := make([]sql.NullString, len(columns))
	targets := make([]any, len(columns))

	for i := range values {
		targets[i] = &values[i]
	}

	if err := rows.Scan(targets...); err != nil {
		return nil, err
	}

	row := make(map[string]string, len(columns))
	for i, column := range columns {
		if values[i].Valid {
			row[column] = values[i].String
		}
	}

	return row, rows.Err()
}

// Seconds_Behind_Source is what MySQL from 8.0.22 calls the column every MariaDB release and every
// earlier MySQL calls Seconds_Behind_Master, and the threads are renamed with it -- so a row is read
// by whichever spelling it carries. Measured on 8.0 with SOURCE_DELAY 3: the lag climbs a second a
// second under writes and drops to 0 three seconds after them, a stopped SQL thread reads NULL, and
// a user without REPLICATION CLIENT is refused with 1227.

// readStatus turns one replica's status row into how far behind it is, or why that cannot be said.
func readStatus(name string, row map[string]string) driver.Lag {
	lag := driver.Lag{Replica: name}

	if len(row) == 0 {
		lag.Err = ErrNotAReplica

		return lag
	}

	io, sqlThread := either(row, "Replica_IO_Running", "Slave_IO_Running"),
		either(row, "Replica_SQL_Running", "Slave_SQL_Running")
	if io != "Yes" || sqlThread != "Yes" {
		lag.Err = stoppedError(io, sqlThread, either(row, "Last_SQL_Error", "Last_IO_Error"))

		return lag
	}

	seconds, err := strconv.ParseInt(either(row, "Seconds_Behind_Source", "Seconds_Behind_Master"), 10, 64)
	if err != nil {
		lag.Err = ErrNoLagReported

		return lag
	}

	lag.Behind = time.Duration(seconds) * time.Second

	return lag
}

// stoppedError names which thread is not running and what the replica says stopped it.
func stoppedError(io, sqlThread, last string) error {
	err := fmt.Errorf("%w (IO thread: %s, SQL thread: %s)", ErrReplicationStopped, orUnknown(io),
		orUnknown(sqlThread))
	if strings.TrimSpace(last) != "" {
		err = fmt.Errorf("%w: %s", err, last)
	}

	return err
}

// either returns the first of two spellings of one column a row carries.
func either(row map[string]string, current, former string) string {
	if v, ok := row[current]; ok {
		return v
	}

	return row[former]
}

// orUnknown renders a thread state, saying so where the row carries none.
func orUnknown(state string) string {
	if state == "" {
		return "unknown"
	}

	return state
}

// closeReplicas closes every replica pool this session opened.
func (c *mysqlConn) closeReplicas() error {
	var closed error

	for _, r := range c.replicas {
		if r.db != nil {
			closed = errors.Join(closed, closeDB(r.db))
		}
	}

	return closed
}

// compile-time proof that this session reads its replicas' lag.
var _ driver.LagReader = (*mysqlConn)(nil)
