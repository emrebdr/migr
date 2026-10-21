package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"

	"github.com/emrebdr/migr/internal/capture"
)

// batchCap is how many changes one read returns before it stops at the next transaction boundary.
const batchCap = 1000

// heartbeat is how often an idle server tells the reader it is still there.
const heartbeat = time.Second

// readTimeout is how long the reader waits for any packet, a heartbeat included, before it gives up.
const readTimeout = 30 * time.Second

// replicaIDBase is the high half of the server-id range, where a reader's id is derived and real replicas rarely are.
const replicaIDBase = uint32(1) << 31

// errPurged is ER_MASTER_FATAL_ERROR_READING_BINLOG, which a dump from a position the server no longer has ends in.
const errPurged = 1236

// gtidModeOn is how @@gtid_mode spells a server recording a GTID for every transaction.
const gtidModeOn = "ON"

// Capture opens a stream of one table's row changes read from the server's binary log, on connections of its own.
func (d *mysqlDriver) Capture(ctx context.Context, dsn string, req capture.Request) (capture.Stream, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	if len(req.Tables) != 1 {
		return nil, fmt.Errorf("the mysql driver reads one table's changes per stream, and %q names %d",
			req.Name, len(req.Tables))
	}

	source, err := parseDSN(dsn, d.schema)
	if err != nil {
		return nil, err
	}

	connector, err := source.connector()
	if err != nil {
		return nil, err
	}

	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)

	stream, err := openStream(ctx, db, source, req)
	if err != nil {
		return nil, errors.Join(err, closeDB(db))
	}

	return stream, nil
}

// binlogStream reads one table's committed row changes from the binary log, a transaction at a time.
type binlogStream struct {
	// name is the capture the engine asked for, which a MySQL reader holds nothing on the server under.
	name string
	// db is the pool the stream reads where the log ends through, never the migration's session.
	db *sql.DB
	// version is the server's, which decides how where the log ends is asked.
	version serverVersion
	// syncer is the replication connection the log arrives over.
	syncer *replication.BinlogSyncer
	// streamer hands over the events the syncer reads.
	streamer *replication.BinlogStreamer
	// database is the schema the table lives in, as the server's catalog holds it.
	database string
	// table is the table whose changes the stream carries, as the catalog holds it.
	table string
	// folding is lower_case_table_names, which decides how a name in an event is matched.
	folding int
	// columns are the table's columns in the order a row event carries them.
	columns []sourceColumn
	// key are the indexes of the primary key's columns in columns.
	key []int
	// gtid reports a server recording GTIDs, whose position is a GTID set.
	gtid bool
	// start is where the stream began.
	start capture.Position
	// at is the file and offset of the last event read.
	at logPoint
	// boundary is the file and offset just past the last transaction read whole.
	boundary logPoint
	// committed is every transaction read whole, on a server recording GTIDs.
	committed gomysql.GTIDSet
	// open reports a transaction begun and not yet committed in what has been read.
	open bool
	// next is the GTID of the transaction being read, empty before its GTID event.
	next string
	// pending are the changes of the transaction being read, handed over only once it commits.
	pending []capture.Change
	// stamp is the server's timestamp on the last commit read, which how far behind is measured from.
	stamp uint32
	// seen is the server's timestamp on the last event read.
	seen uint32
}

// openStream reads what a stream needs about the server and the table, and starts reading the log.
func openStream(ctx context.Context, db *sql.DB, source parsed, req capture.Request) (*binlogStream, error) {
	s := &binlogStream{name: req.Name, db: db}

	if err := s.readServer(ctx); err != nil {
		return nil, err
	}

	if err := s.readTable(ctx, req.Tables[0]); err != nil {
		return nil, err
	}

	from, err := s.startingPoint(ctx, req.From)
	if err != nil {
		return nil, err
	}

	id, err := s.replicaID(ctx)
	if err != nil {
		return nil, err
	}

	config, err := syncerConfig(source, id)
	if err != nil {
		return nil, err
	}

	s.syncer = replication.NewBinlogSyncer(config)

	if err := s.begin(from); err != nil {
		s.syncer.Close()

		return nil, err
	}

	return s, nil
}

// readServer reads the server's version and whether it records GTIDs, refusing a server this reader cannot read.
func (s *binlogStream) readServer(ctx context.Context) error {
	var reported, comment, mode string

	if err := s.db.QueryRowContext(ctx, versionQuery).Scan(&reported, &comment); err != nil {
		return fmt.Errorf("reading the server's version for a change stream: %w", err)
	}

	version, err := interpretVersion(reported, comment)
	if err != nil {
		return err
	}

	if refusal := streamRefusal(version); refusal != "" {
		return fmt.Errorf("%w: %s", capture.ErrUnsupported, refusal)
	}

	if err := s.db.QueryRowContext(ctx, "SELECT @@global.gtid_mode, @@lower_case_table_names").
		Scan(&mode, &s.folding); err != nil {
		return fmt.Errorf("reading whether the server records GTIDs: %w", err)
	}

	s.version, s.gtid = version, strings.EqualFold(mode, gtidModeOn)

	return nil
}

// streamRefusal says why this reader cannot read a server's log, and nothing where it can.
func streamRefusal(v serverVersion) string {
	switch {
	case v.Variant() == MariaDB:
		return "the mysql driver reads MySQL's binary log and not MariaDB's, whose GTIDs and cutover " +
			"locking differ, so a shadow-table migration is refused on MariaDB"
	case !v.AtLeastPatch(MySQL, 8, 0, 13):
		return fmt.Sprintf("a shadow table's cutover renames it under LOCK TABLES, which MySQL allows "+
			"from 8.0.13, and this server is %s", v)
	default:
		return ""
	}
}

// readTable reads the table's columns and primary key as the server's catalog holds them.
func (s *binlogStream) readTable(ctx context.Context, table string) error {
	if err := s.db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&s.database); err != nil {
		return fmt.Errorf("reading the database a change stream reads: %w", err)
	}

	s.table = storedName(table, s.folding)

	columns, err := readSourceColumns(ctx, s.db, s.table)
	if err != nil {
		return err
	}

	key, err := readPrimaryKey(ctx, s.db, s.table)
	if err != nil {
		return err
	}

	for _, name := range key {
		index := columnIndex(columns, name)
		if index < 0 {
			return fmt.Errorf("the primary key of %s names %s, which is not one of its columns", table, name)
		}

		s.key = append(s.key, index)
	}

	if bad := unreplayableColumns(columns); len(bad) > 0 {
		return fmt.Errorf("%w: %s has columns whose values a change stream cannot replay: %s",
			capture.ErrUnsupported, table, strings.Join(bad, ", "))
	}

	s.columns = columns

	return nil
}

// columnIndex returns where a column is among a table's columns, and -1 where it is not one.
func columnIndex(columns []sourceColumn, name string) int {
	for i, column := range columns {
		if strings.EqualFold(column.name, name) {
			return i
		}
	}

	return -1
}

// logEnd is where the server's binary log ended at one moment, and the server's clock at it.
type logEnd struct {
	// point is the file and offset of the log's end.
	point logPoint
	// gtid is every transaction the server had executed, nil where it records none.
	gtid gomysql.GTIDSet
	// now is the server's clock, in seconds since the epoch.
	now int64
}

// readEnd reads where the log ends now, the way this server's release asks.
func (s *binlogStream) readEnd(ctx context.Context) (logEnd, error) {
	query := "SHOW MASTER STATUS"
	if s.version.AtLeast(MySQL, 8, 2) {
		query = "SHOW BINARY LOG STATUS"
	}

	var (
		file, offset, doDB, ignoreDB, executed sql.NullString
		end                                    logEnd
	)

	if err := s.db.QueryRowContext(ctx, query).Scan(&file, &offset, &doDB, &ignoreDB, &executed); err != nil {
		return logEnd{}, fmt.Errorf("reading where the binary log ends (%s): %w", query, err)
	}

	at, err := strconv.ParseUint(offset.String, 10, 32)
	if err != nil {
		return logEnd{}, fmt.Errorf("reading where the binary log ends: %q is not an offset", offset.String)
	}

	end.point = logPoint{file: file.String, offset: uint32(at)}

	if s.gtid {
		if end.gtid, err = gomysql.ParseMysqlGTIDSet(executed.String); err != nil {
			return logEnd{}, fmt.Errorf("reading the server's executed GTIDs %q: %w", executed.String, err)
		}
	}

	if err := s.db.QueryRowContext(ctx, "SELECT UNIX_TIMESTAMP()").Scan(&end.now); err != nil {
		return logEnd{}, fmt.Errorf("reading the server's clock: %w", err)
	}

	return end, nil
}

// startingPoint returns where the stream starts: the recorded position, or where the log ends now.
func (s *binlogStream) startingPoint(ctx context.Context, from capture.Position) (position, error) {
	if from == "" {
		end, err := s.readEnd(ctx)
		if err != nil {
			return position{}, err
		}

		if s.gtid {
			return position{gtid: end.gtid}, nil
		}

		return position{point: end.point}, nil
	}

	recorded, err := parsePosition(from)
	if err != nil {
		return position{}, err
	}

	if (recorded.gtid != nil) != s.gtid {
		return position{}, fmt.Errorf("the recorded position %s was read from a server that %s GTIDs, "+
			"and this one %s -- abandon the migration with migr resolve --rolled-back and run it again",
			from, recordedOrNot(recorded.gtid != nil), recordedOrNot(s.gtid))
	}

	return recorded, nil
}

// recordedOrNot says whether a server records GTIDs, for a refusal naming both.
func recordedOrNot(records bool) string {
	if records {
		return "records"
	}

	return "does not record"
}

// replicaID derives the server id the reader registers with, moving past every id this server or a replica holds.
func (s *binlogStream) replicaID(ctx context.Context) (uint32, error) {
	taken, err := s.takenIDs(ctx)
	if err != nil {
		return 0, err
	}

	return freeID(s.name, taken), nil
}

// freeID derives an id from a capture's name in the high range, stepping past every one already taken.
func freeID(name string, taken map[uint32]bool) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))

	id := replicaIDBase | (h.Sum32() &^ replicaIDBase)
	for taken[id] || id == 0 {
		id = replicaIDBase | ((id + 1) &^ replicaIDBase)
	}

	return id
}

// takenIDs reads this server's own id and every registered replica's, which a reader must never reuse.
func (s *binlogStream) takenIDs(ctx context.Context) (map[uint32]bool, error) {
	var own uint32
	if err := s.db.QueryRowContext(ctx, "SELECT @@server_id").Scan(&own); err != nil {
		return nil, fmt.Errorf("reading this server's id: %w", err)
	}

	query := "SHOW SLAVE HOSTS"
	if s.version.AtLeastPatch(MySQL, 8, 0, 22) {
		query = "SHOW REPLICAS"
	}

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("reading the ids this server's replicas hold (%s): %w", query, err)
	}

	defer func() { _ = rows.Close() }()

	taken, err := replicaIDs(rows)
	if err != nil {
		return nil, fmt.Errorf("reading the ids this server's replicas hold (%s): %w", query, err)
	}

	taken[own] = true

	return taken, nil
}

// replicaIDs collects the Server_Id column of every row a replica listing returns.
func replicaIDs(rows *sql.Rows) (map[uint32]bool, error) {
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	taken := map[uint32]bool{}
	values := make([]sql.NullString, len(names))
	targets := make([]any, len(names))

	for i := range values {
		targets[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}

		for i, name := range names {
			if id, err := strconv.ParseUint(values[i].String, 10, 32); strings.EqualFold(name, "server_id") && err == nil {
				taken[uint32(id)] = true
			}
		}
	}

	return taken, rows.Err()
}

// syncerConfig is the replication connection's configuration, read from the connection string the session uses.
func syncerConfig(source parsed, id uint32) (replication.BinlogSyncerConfig, error) {
	c := source.config
	config := replication.BinlogSyncerConfig{
		ServerID: id, Flavor: gomysql.MySQLFlavor, User: c.User, Password: c.Passwd,
		TLSConfig: c.TLS, TimestampStringLocation: time.UTC, HeartbeatPeriod: heartbeat,
		ReadTimeout: readTimeout, DisableRetrySync: true, Logger: slog.New(slog.DiscardHandler),
	}

	if c.Net == "unix" {
		socket := c.Addr
		config.Host, config.Port = "localhost", 0
		config.Dialer = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}

		return config, nil
	}

	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return replication.BinlogSyncerConfig{}, fmt.Errorf("reading the server's address %q: %w", c.Addr, err)
	}

	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return replication.BinlogSyncerConfig{}, fmt.Errorf("reading the server's port %q: %w", port, err)
	}

	config.Host, config.Port = host, uint16(number)

	return config, nil
}

// begin starts reading the log from a position, recording it as where the stream began.
func (s *binlogStream) begin(from position) error {
	var err error

	if from.gtid != nil {
		s.committed = from.gtid.Clone()
		s.streamer, err = s.syncer.StartSyncGTID(from.gtid.Clone())
	} else {
		s.at, s.boundary = from.point, from.point
		s.streamer, err = s.syncer.StartSync(gomysql.Position{Name: from.point.file, Pos: from.point.offset})
	}

	if err != nil {
		return fmt.Errorf("starting to read the binary log at %s: %w", from.spell(), err)
	}

	s.start = from.spell()

	return nil
}

// Start returns the position the stream began reading at.
func (s *binlogStream) Start() capture.Position {
	return s.start
}

// through returns the position just past the last transaction read whole.
func (s *binlogStream) through() capture.Position {
	if s.gtid {
		return position{gtid: s.committed}.spell()
	}

	return position{point: s.boundary}.spell()
}

// reached reports whether every transaction the log held at its end has been read whole.
func (s *binlogStream) reached(end logEnd) bool {
	if s.open {
		return false
	}

	if s.gtid {
		return s.committed.Contain(end.gtid)
	}

	return s.boundary.reached(end.point)
}

// Next reads every transaction committed up to where the log ends now, stopping early at a boundary past the cap.
func (s *binlogStream) Next(ctx context.Context) (capture.Batch, error) {
	end, err := s.readEnd(ctx)
	if err != nil {
		return capture.Batch{}, err
	}

	var changes []capture.Change

	for !s.reached(end) && (s.open || len(changes) < batchCap) {
		event, err := s.streamer.GetEvent(ctx)
		if err != nil {
			return capture.Batch{}, s.readFailure(err)
		}

		committed, err := s.consume(event)
		if err != nil {
			return capture.Batch{}, err
		}

		changes = append(changes, committed...)
	}

	batch := capture.Batch{Changes: changes, Through: s.through()}
	if !s.reached(end) && s.stamp > 0 {
		batch.Behind = time.Duration(max(end.now-int64(s.stamp), 0)) * time.Second
	}

	return batch, nil
}

// readFailure explains a failed read, naming the retention setting where the position is no longer on the server.
func (s *binlogStream) readFailure(err error) error {
	var server *gomysql.MyError
	if errors.As(err, &server) && server.Code == errPurged {
		return fmt.Errorf("the binary log no longer holds %s, where this shadow-table migration's copy "+
			"was last kept current -- the server purged it (binlog_expire_logs_seconds bounds how long a "+
			"stopped migration can be carried on), so abandon it with migr resolve --rolled-back and run "+
			"it again: %w", s.through(), err)
	}

	return fmt.Errorf("reading the binary log after %s: %w", s.through(), err)
}

// Acknowledge is nothing on MySQL, whose server keeps its log for binlog_expire_logs_seconds and for no reader.
func (s *binlogStream) Acknowledge(context.Context, capture.Position) error {
	return nil
}

// Close stops reading, which on MySQL holds nothing on the server to keep.
func (s *binlogStream) Close(context.Context) error {
	s.syncer.Close()

	return s.db.Close()
}

// Drop stops reading and releases what the stream holds, which on MySQL is its own connections alone.
func (s *binlogStream) Drop(ctx context.Context) error {
	return s.Close(ctx)
}

// compile-time proof that a binary-log reader is a change stream.
var _ capture.Stream = (*binlogStream)(nil)
