package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/emrebdr/migr/internal/history"
)

// releaseColumns is the order the release store binds and scans, held to history.ReleaseSchema.
var releaseColumns = []string{"batch", "release", "recorded_at"}

// releaseTable names the table this store's releases live in, as every statement names it.
func (h mysqlHistory) releaseTable() string {
	return history.ReleaseTable(h.table)
}

// releaseStored is that name as this server's catalog holds it.
func (h mysqlHistory) releaseStored() string {
	return storedName(h.releaseTable(), h.session.folding)
}

// ensureReleases creates the release table, or checks the name is held by a table and nothing else.
func (h mysqlHistory) ensureReleases(ctx context.Context) error {
	kind, found, err := h.kindOf(ctx, h.releaseStored())
	if err != nil {
		return err
	}

	if found {
		return h.checkReleaseKind(kind)
	}

	stmts, err := h.compile(history.ReleaseSchema(h.table))
	if err != nil {
		return err
	}

	for _, stmt := range stmts {
		if err := h.session.Exec(ctx, stmt); err != nil && !hasNumber(err, errTableExists) {
			return h.wrapRelease("creating the release table", err)
		}
	}

	return nil
}

// checkReleaseKind refuses a release table name held by a view rather than by a table.
func (h mysqlHistory) checkReleaseKind(kind string) error {
	if kind == ordinaryTable {
		return nil
	}

	return &HistoryError{Table: h.releaseTable(), Err: ErrHistoryNotATable, Detail: fmt.Sprintf(
		"%v: the name of the release table beside %q is reported as %s", ErrHistoryNotATable,
		h.table, kind)}
}

// RecordRelease records the release of one batch, inside the open transaction.
func (h mysqlHistory) RecordRelease(ctx context.Context, r history.Release) error {
	if err := r.Validate(); err != nil {
		return err
	}

	_, err := h.session.runner().ExecContext(ctx, "INSERT INTO "+quote(h.releaseTable())+" ("+
		quoteAll(releaseColumns)+")\nVALUES (?, ?, ?)", r.Batch, r.Name, renderTime(r.RecordedAt))
	if err != nil {
		return h.wrapRelease("recording a release", err)
	}

	return nil
}

// Releases returns every release recorded, and none where the table does not exist yet.
func (h mysqlHistory) Releases(ctx context.Context) ([]history.Release, error) {
	_, found, err := h.kindOf(ctx, h.releaseStored())
	if err != nil || !found {
		return nil, err
	}

	rows, err := h.session.runner().QueryContext(ctx, "SELECT "+quoteAll(releaseColumns)+
		"\nFROM "+quote(h.releaseTable())+"\nORDER BY `batch`")
	if err != nil {
		return nil, h.wrapRelease("reading the releases", err)
	}

	defer rows.Close()

	return h.collectReleases(rows)
}

// collectReleases scans every release one read returned.
func (h mysqlHistory) collectReleases(rows *sql.Rows) ([]history.Release, error) {
	var found []history.Release

	for rows.Next() {
		var (
			r        history.Release
			recorded string
		)

		if err := rows.Scan(&r.Batch, &r.Name, &recorded); err != nil {
			return nil, h.wrapRelease("reading a release", err)
		}

		at, err := parseTime(recorded)
		if err != nil {
			return nil, h.wrapRelease("reading a release", err)
		}

		r.RecordedAt = at
		found = append(found, r)
	}

	if err := rows.Err(); err != nil {
		return nil, h.wrapRelease("reading the releases", err)
	}

	return found, nil
}

// wrapRelease reports a release failure, naming the release table rather than the history.
func (h mysqlHistory) wrapRelease(doing string, err error) error {
	return &HistoryError{Table: h.releaseTable(), Err: err, Detail: doing + ": " + err.Error()}
}

// compile-time proof that this store remembers which deploy each batch ran under.
var _ history.Releaser = mysqlHistory{}
