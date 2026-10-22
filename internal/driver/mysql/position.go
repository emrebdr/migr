package mysql

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"

	"github.com/emrebdr/migr/internal/capture"
)

// A recorded position is spelled by which kind of log the server keeps, and is always a transaction boundary.
//
// A server recording GTIDs is read from a GTID set, gtid:<set>: the set survives a failover to a server
// whose binary log files are named and numbered differently, which a file and offset does not. One that
// does not is read from file:<name>:<offset>. Both name the end of the last transaction applied, so a
// resume never starts inside one.

// The two prefixes a recorded position begins with.
const (
	gtidPrefix = "gtid:"
	filePrefix = "file:"
)

// errPosition marks a recorded position this driver cannot read back.
var errPosition = errors.New("this is not a binary-log position the mysql driver recorded")

// logPoint is a place in one binary log file.
type logPoint struct {
	// file is the binary log file's name.
	file string
	// offset is the byte offset in it.
	offset uint32
}

// String spells the point as a recorded file-and-offset position.
func (p logPoint) String() string {
	return filePrefix + p.file + ":" + strconv.FormatUint(uint64(p.offset), 10)
}

// reached reports whether this point is at or past another, comparing files by their sequence number.
func (p logPoint) reached(target logPoint) bool {
	if p.file == target.file {
		return p.offset >= target.offset
	}

	return sequenceOf(p.file) > sequenceOf(target.file)
}

// sequenceOf reads the number a binary log file's name ends in, which orders the files.
func sequenceOf(file string) int64 {
	dot := strings.LastIndexByte(file, '.')
	if dot < 0 {
		return -1
	}

	n, err := strconv.ParseInt(file[dot+1:], 10, 64)
	if err != nil {
		return -1
	}

	return n
}

// position is a recorded position read back: a GTID set, or a file and offset.
type position struct {
	// gtid is the set every applied transaction belongs to, nil for a file-and-offset position.
	gtid gomysql.GTIDSet
	// point is the file and offset, the zero point for a GTID position.
	point logPoint
}

// parsePosition reads a recorded position back, a GTID set in the flavor of the server it is read on.
func parsePosition(text capture.Position, mariadb bool) (position, error) {
	raw := string(text)

	switch {
	case strings.HasPrefix(raw, gtidPrefix):
		return parseGTIDPosition(raw, mariadb)
	case strings.HasPrefix(raw, filePrefix):
		return parseFilePosition(raw)
	default:
		return position{}, fmt.Errorf("%w: %q begins with neither %q nor %q", errPosition, raw, gtidPrefix,
			filePrefix)
	}
}

// parseGTIDPosition reads a gtid:<set> position, refusing a set the other flavor of server recorded.
func parseGTIDPosition(raw string, mariadb bool) (position, error) {
	text := strings.TrimPrefix(raw, gtidPrefix)

	parse, flavor, other := gomysql.ParseMysqlGTIDSet, "MySQL", gomysql.ParseMariadbGTIDSet
	if mariadb {
		parse, flavor, other = gomysql.ParseMariadbGTIDSet, "MariaDB", gomysql.ParseMysqlGTIDSet
	}

	set, err := parse(text)
	if err == nil {
		return position{gtid: set}, nil
	}

	if _, otherErr := other(text); otherErr == nil {
		return position{}, fmt.Errorf("%w: %q is a GTID set the other flavor of server records, and this "+
			"server is %s -- abandon the migration with migr resolve --rolled-back and run it again",
			errPosition, raw, flavor)
	}

	return position{}, fmt.Errorf("%w: %q is not a %s GTID set: %w", errPosition, raw, flavor, err)
}

// parseFilePosition reads a file:<name>:<offset> position back.
func parseFilePosition(raw string) (position, error) {
	rest := strings.TrimPrefix(raw, filePrefix)

	colon := strings.LastIndexByte(rest, ':')
	if colon <= 0 {
		return position{}, fmt.Errorf("%w: %q names no offset", errPosition, raw)
	}

	offset, err := strconv.ParseUint(rest[colon+1:], 10, 32)
	if err != nil || offset < 4 {
		return position{}, fmt.Errorf("%w: %q carries no offset a binary log has", errPosition, raw)
	}

	return position{point: logPoint{file: rest[:colon], offset: uint32(offset)}}, nil
}

// spell returns the position as recorded text, a GTID set where it has one.
func (p position) spell() capture.Position {
	if p.gtid != nil {
		return capture.Position(gtidPrefix + p.gtid.String())
	}

	return capture.Position(p.point.String())
}
