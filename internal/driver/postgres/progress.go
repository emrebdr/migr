package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// progressColumns is the order the checkpoint store binds and scans, held to history.ProgressSchema.
var progressColumns = []string{"version", "direction", "step", "cursor", "chunks", "written"}

// progressTable names the table this store's checkpoints live in.
func (h pgHistory) progressTable() string {
	return history.ProgressTable(h.table)
}

// ensureProgress creates the checkpoint table, or checks the name is held by a table and nothing else.
func (h pgHistory) ensureProgress(ctx context.Context) error {
	kind, found, err := h.relkindOf(ctx, h.progressTable())
	if err != nil {
		return err
	}

	if found {
		return h.checkProgressKind(kind)
	}

	stmts, err := h.compileSchema(history.ProgressSchema(h.table))
	if err != nil {
		return err
	}

	return h.session.inTransaction(ctx, func(ctx context.Context) error {
		return h.run(ctx, stmts)
	})
}

// checkProgressKind refuses a checkpoint table name held by a view, an index, or a sequence.
func (h pgHistory) checkProgressKind(kind string) error {
	if kind == ordinaryTable || kind == partitionedTable {
		return nil
	}

	return &HistoryError{Table: h.progressTable(), Err: ErrHistoryNotATable, Detail: fmt.Sprintf(
		"%v: the name of the checkpoint table beside %q is held by a relation of kind %q",
		ErrHistoryNotATable, h.table, kind)}
}

// SaveCheckpoint replaces the checkpoint of one migration and direction, inside the open transaction.
func (h pgHistory) SaveCheckpoint(ctx context.Context, cp history.Checkpoint) error {
	if err := cp.Validate(); err != nil {
		return err
	}

	cursor, err := history.EncodeCursor(cp.Cursor)
	if err != nil {
		return err
	}

	if err := h.ClearCheckpoint(ctx, cp.Version, cp.Direction); err != nil {
		return err
	}

	_, err = h.session.runner().Exec(ctx, "INSERT INTO "+quote(h.progressTable())+" ("+
		quoteAll(progressColumns)+")\nVALUES ($1, $2, $3, $4, $5, $6)", cp.Version,
		string(cp.Direction), cp.Step, cursor, cp.Chunks, cp.Written)
	if err != nil {
		return h.wrapProgress("recording a checkpoint", err)
	}

	return nil
}

// ClearCheckpoint removes the checkpoint of one migration and direction, where there is one.
func (h pgHistory) ClearCheckpoint(ctx context.Context, version string, dir ir.Direction) error {
	_, err := h.session.runner().Exec(ctx, "DELETE FROM "+quote(h.progressTable())+
		` WHERE "version" = $1 AND "direction" = $2`, version, string(dir))
	if err != nil {
		return h.wrapProgress("clearing a checkpoint", err)
	}

	return nil
}

// Checkpoints returns every checkpoint recorded, and none where the table does not exist yet.
func (h pgHistory) Checkpoints(ctx context.Context) ([]history.Checkpoint, error) {
	_, found, err := h.relkindOf(ctx, h.progressTable())
	if err != nil || !found {
		return nil, err
	}

	rows, err := h.session.runner().Query(ctx, "SELECT "+quoteAll(progressColumns)+
		"\nFROM "+quote(h.progressTable())+"\nORDER BY \"version\", \"direction\"")
	if err != nil {
		return nil, h.wrapProgress("reading the checkpoints", err)
	}

	defer rows.Close()

	return h.collectCheckpoints(rows)
}

// collectCheckpoints scans every checkpoint one read returned.
func (h pgHistory) collectCheckpoints(rows pgx.Rows) ([]history.Checkpoint, error) {
	var cps []history.Checkpoint

	for rows.Next() {
		cp, err := h.scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}

		cps = append(cps, cp)
	}

	if err := rows.Err(); err != nil {
		return nil, h.wrapProgress("reading the checkpoints", err)
	}

	return cps, nil
}

// scanCheckpoint reads one checkpoint, decoding the cursor it was stored with.
func (h pgHistory) scanCheckpoint(rows pgx.Rows) (history.Checkpoint, error) {
	var (
		cp        history.Checkpoint
		direction string
		cursor    string
	)

	if err := rows.Scan(&cp.Version, &direction, &cp.Step, &cursor, &cp.Chunks,
		&cp.Written); err != nil {
		return history.Checkpoint{}, h.wrapProgress("reading a checkpoint", err)
	}

	cp.Direction = ir.Direction(direction)

	decoded, err := history.DecodeCursor(cursor)
	if err != nil {
		return history.Checkpoint{}, h.wrapProgress("reading a checkpoint", err)
	}

	cp.Cursor = decoded

	return cp, nil
}

// wrapProgress reports a checkpoint failure, naming the checkpoint table rather than the history.
func (h pgHistory) wrapProgress(doing string, err error) error {
	return &HistoryError{Table: h.progressTable(), Err: err, Detail: doing + ": " + err.Error()}
}

// compile-time proof that this store remembers how far a chunked operation got.
var _ history.Checkpointer = pgHistory{}
