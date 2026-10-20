package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/history"
)

// releaseColumns is the order the release store binds and scans, held to history.ReleaseSchema.
var releaseColumns = []string{"batch", "release", "recorded_at"}

// releaseTable names the table this store's releases live in.
func (h pgHistory) releaseTable() string {
	return history.ReleaseTable(h.table)
}

// ensureReleases creates the release table, or checks the name is held by a table and nothing else.
func (h pgHistory) ensureReleases(ctx context.Context) error {
	kind, found, err := h.relkindOf(ctx, h.releaseTable())
	if err != nil {
		return err
	}

	if found {
		return h.checkReleaseKind(kind)
	}

	stmts, err := h.compileSchema(history.ReleaseSchema(h.table))
	if err != nil {
		return err
	}

	return h.session.inTransaction(ctx, func(ctx context.Context) error {
		return h.run(ctx, stmts)
	})
}

// checkReleaseKind refuses a release table name held by a view, an index, or a sequence.
func (h pgHistory) checkReleaseKind(kind string) error {
	if kind == ordinaryTable || kind == partitionedTable {
		return nil
	}

	return &HistoryError{Table: h.releaseTable(), Err: ErrHistoryNotATable, Detail: fmt.Sprintf(
		"%v: the name of the release table beside %q is held by a relation of kind %q", ErrHistoryNotATable,
		h.table, kind)}
}

// RecordRelease records the release of one batch, inside the open transaction.
func (h pgHistory) RecordRelease(ctx context.Context, r history.Release) error {
	if err := r.Validate(); err != nil {
		return err
	}

	_, err := h.session.runner().Exec(ctx, "INSERT INTO "+quote(h.releaseTable())+" ("+
		quoteAll(releaseColumns)+")\nVALUES ($1, $2, $3)", r.Batch, r.Name, r.RecordedAt)
	if err != nil {
		return h.wrapRelease("recording a release", err)
	}

	return nil
}

// Releases returns every release recorded, and none where the table does not exist yet.
func (h pgHistory) Releases(ctx context.Context) ([]history.Release, error) {
	_, found, err := h.relkindOf(ctx, h.releaseTable())
	if err != nil || !found {
		return nil, err
	}

	rows, err := h.session.runner().Query(ctx, "SELECT "+quoteAll(releaseColumns)+
		"\nFROM "+quote(h.releaseTable())+"\nORDER BY \"batch\"")
	if err != nil {
		return nil, h.wrapRelease("reading the releases", err)
	}

	defer rows.Close()

	return h.collectReleases(rows)
}

// collectReleases scans every release one read returned.
func (h pgHistory) collectReleases(rows pgx.Rows) ([]history.Release, error) {
	var found []history.Release

	for rows.Next() {
		var r history.Release

		if err := rows.Scan(&r.Batch, &r.Name, &r.RecordedAt); err != nil {
			return nil, h.wrapRelease("reading a release", err)
		}

		r.RecordedAt = r.RecordedAt.UTC()
		found = append(found, r)
	}

	if err := rows.Err(); err != nil {
		return nil, h.wrapRelease("reading the releases", err)
	}

	return found, nil
}

// wrapRelease reports a release failure, naming the release table rather than the history.
func (h pgHistory) wrapRelease(doing string, err error) error {
	return &HistoryError{Table: h.releaseTable(), Err: err, Detail: doing + ": " + err.Error()}
}

// compile-time proof that this store remembers which deploy each batch ran under.
var _ history.Releaser = pgHistory{}
