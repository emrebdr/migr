package mysql

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/capability"
)

// binlogQuery reads the two binary-log settings a row-image reader needs beyond log_bin itself.
const binlogQuery = `SELECT @@global.binlog_format, @@global.binlog_row_image`

// retentionQuery reads how long the server keeps a binary log file before purging it by age.
const retentionQuery = `SELECT @@global.binlog_expire_logs_seconds`

// rowValueQuery reads whether row images are sent whole, which MySQL alone can be told not to.
const rowValueQuery = `SELECT @@global.binlog_row_value_options`

// grantsQuery lists this session's own global privileges by name, which carries no password hash
// where SHOW GRANTS on MariaDB does.
const grantsQuery = `SELECT PRIVILEGE_TYPE FROM information_schema.USER_PRIVILEGES WHERE GRANTEE = ` +
	`CONCAT('''', SUBSTRING_INDEX(CURRENT_USER(), '@', 1), '''@''', ` +
	`SUBSTRING_INDEX(CURRENT_USER(), '@', -1), '''')`

// The privileges a binlog reader needs: one to stream the log, one to read where it is. MariaDB
// 10.5 renamed the second BINLOG MONITOR, and reports it under that name alone.
const (
	privReplicationSlave  = "REPLICATION SLAVE"
	privReplicationClient = "REPLICATION CLIENT"
	privBinlogMonitor     = "BINLOG MONITOR"
)

// privReplicaAdmin is what MariaDB from 10.5.2 asks of SHOW SLAVE HOSTS, which a reader lists every
// replica's server id through so it never takes one: MariaDB disconnects one of two readers sharing an id.
const privReplicaAdmin = "REPLICATION MASTER ADMIN"

// The values the two settings must hold for every change to arrive as a whole row.
const (
	rowFormat = "ROW"
	fullImage = "FULL"
)

// captureAnswer is what the server reported about log-based change capture.
type captureAnswer struct {
	// logBin is whether it writes a binary log.
	logBin bool
	// format is binlog_format.
	format string
	// image is binlog_row_image.
	image string
	// rowValues is binlog_row_value_options, which MariaDB has no variable for and reads as empty.
	rowValues string
	// expires is binlog_expire_logs_seconds, and -1 where it was not read.
	expires int64
	// privileges are this session's global privileges.
	privileges []string
	// mariadb reports a MariaDB server, which asks a further privilege of a reader.
	mariadb bool
}

// readCapture asks whether this server permits a binlog reader, reading a failed answer as not permitted.
func readCapture(ctx context.Context, c *mysqlConn, logBin bool, version serverVersion) capability.Capture {
	a := captureAnswer{logBin: logBin, mariadb: version.Variant() == MariaDB}

	if err := c.runner().QueryRowContext(ctx, binlogQuery).Scan(&a.format, &a.image); err != nil {
		return capability.Lacks(fmt.Sprintf("an answer migr could read about binlog_format and "+
			"binlog_row_image (%v)", err))
	}

	a.expires = -1

	if version.Variant() == MySQL {
		if err := c.runner().QueryRowContext(ctx, rowValueQuery).Scan(&a.rowValues); err != nil {
			return capability.Lacks(fmt.Sprintf("an answer migr could read about "+
				"binlog_row_value_options (%v)", err))
		}
	}

	if err := c.runner().QueryRowContext(ctx, retentionQuery).Scan(&a.expires); err != nil {
		a.expires = -1
	}

	privileges, err := readPrivileges(ctx, c)
	if err != nil {
		return capability.Lacks(fmt.Sprintf("an answer migr could read about this user's "+
			"replication privileges (%v)", err))
	}

	a.privileges = privileges

	return a.capture()
}

// readPrivileges lists this session's own global privileges.
func readPrivileges(ctx context.Context, c *mysqlConn) ([]string, error) {
	rows, err := c.runner().QueryContext(ctx, grantsQuery)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var found []string

	for rows.Next() {
		var privilege string
		if err := rows.Scan(&privilege); err != nil {
			return nil, err
		}

		found = append(found, strings.ToUpper(strings.TrimSpace(privilege)))
	}

	return found, rows.Err()
}

// capture names every setting and privilege the answer lacks, and permits capture where it lacks none.
func (a captureAnswer) capture() capability.Capture {
	var missing []string

	if !a.logBin {
		missing = append(missing, "log_bin = ON (it is OFF, and turning it on restarts the server)")
	}

	if !strings.EqualFold(a.format, rowFormat) {
		missing = append(missing, fmt.Sprintf("binlog_format = ROW (it is %s)", a.format))
	}

	if !strings.EqualFold(a.image, fullImage) {
		missing = append(missing, fmt.Sprintf("binlog_row_image = FULL (it is %s)", a.image))
	}

	if strings.TrimSpace(a.rowValues) != "" {
		missing = append(missing, fmt.Sprintf("binlog_row_value_options empty, so every update carries "+
			"its whole row (it is %s)", a.rowValues))
	}

	if !slices.Contains(a.privileges, privReplicationSlave) {
		missing = append(missing, "the REPLICATION SLAVE privilege on *.*")
	}

	if !slices.Contains(a.privileges, privReplicationClient) && !slices.Contains(a.privileges, privBinlogMonitor) {
		missing = append(missing, "the REPLICATION CLIENT privilege on *.* (BINLOG MONITOR on MariaDB)")
	}

	if a.mariadb && !slices.Contains(a.privileges, privReplicaAdmin) {
		missing = append(missing, "the REPLICATION MASTER ADMIN privilege on *.*, which MariaDB asks before "+
			"it lists the replicas whose server ids a reader must not take")
	}

	if len(missing) > 0 {
		return capability.Lacks(missing...)
	}

	return capability.Permits().Retains(a.retention())
}

// retention says how long the server keeps its binary log, and nothing where that was not read.
func (a captureAnswer) retention() string {
	switch {
	case a.expires < 0:
		return ""
	case a.expires == 0:
		return "binlog_expire_logs_seconds = 0, so no binary log is purged by age -- a manual PURGE " +
			"BINARY LOGS still ends it"
	default:
		return fmt.Sprintf("binlog_expire_logs_seconds = %d, so a binary log file is purged %s after it "+
			"is closed", a.expires, time.Duration(a.expires)*time.Second)
	}
}
