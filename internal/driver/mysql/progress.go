package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// progressColumns is the order the checkpoint store binds and scans, held to history.ProgressSchema.
var progressColumns = []string{"version", "direction", "step", "cursor", "chunks", "written"}

// progressTable names the table this store's checkpoints live in, as every statement names it.
func (h mysqlHistory) progressTable() string {
	return history.ProgressTable(h.table)
}

// progressStored is that name as this server's catalog holds it.
func (h mysqlHistory) progressStored() string {
	return storedName(h.progressTable(), h.session.folding)
}

// ensureProgress creates the checkpoint table, or checks the name is held by a table and nothing else.
func (h mysqlHistory) ensureProgress(ctx context.Context) error {
	kind, found, err := h.kindOf(ctx, h.progressStored())
	if err != nil {
		return err
	}

	if found {
		return h.checkProgressKind(kind)
	}

	stmts, err := h.compile(history.ProgressSchema(h.table))
	if err != nil {
		return err
	}

	for _, stmt := range stmts {
		if err := h.session.Exec(ctx, stmt); err != nil && !hasNumber(err, errTableExists) {
			return h.wrapProgress("creating the checkpoint table", err)
		}
	}

	return nil
}

// checkProgressKind refuses a checkpoint table name held by a view rather than by a table.
func (h mysqlHistory) checkProgressKind(kind string) error {
	if kind == ordinaryTable {
		return nil
	}

	return &HistoryError{Table: h.progressTable(), Err: ErrHistoryNotATable, Detail: fmt.Sprintf(
		"%v: the name of the checkpoint table beside %q is reported as %s", ErrHistoryNotATable,
		h.table, kind)}
}

// SaveCheckpoint replaces the checkpoint of one migration and direction, inside the open transaction.
func (h mysqlHistory) SaveCheckpoint(ctx context.Context, cp history.Checkpoint) error {
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

	_, err = h.session.runner().ExecContext(ctx, "INSERT INTO "+quote(h.progressTable())+" ("+
		quoteAll(progressColumns)+")\nVALUES (?, ?, ?, ?, ?, ?)", cp.Version, string(cp.Direction),
		cp.Step, cursor, cp.Chunks, cp.Written)
	if err != nil {
		return h.wrapProgress("recording a checkpoint", err)
	}

	return nil
}

// ClearCheckpoint removes the checkpoint of one migration and direction, where there is one.
func (h mysqlHistory) ClearCheckpoint(ctx context.Context, version string, dir ir.Direction) error {
	_, err := h.session.runner().ExecContext(ctx, "DELETE FROM "+quote(h.progressTable())+
		" WHERE `version` = ? AND `direction` = ?", version, string(dir))
	if err != nil {
		return h.wrapProgress("clearing a checkpoint", err)
	}

	return nil
}

// Checkpoints returns every checkpoint recorded, and none where the table does not exist yet.
func (h mysqlHistory) Checkpoints(ctx context.Context) ([]history.Checkpoint, error) {
	_, found, err := h.kindOf(ctx, h.progressStored())
	if err != nil || !found {
		return nil, err
	}

	rows, err := h.session.runner().QueryContext(ctx, "SELECT "+quoteAll(progressColumns)+
		"\nFROM "+quote(h.progressTable())+"\nORDER BY `version`, `direction`")
	if err != nil {
		return nil, h.wrapProgress("reading the checkpoints", err)
	}

	defer rows.Close()

	return h.collectCheckpoints(rows)
}

// collectCheckpoints scans every checkpoint one read returned.
func (h mysqlHistory) collectCheckpoints(rows *sql.Rows) ([]history.Checkpoint, error) {
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
func (h mysqlHistory) scanCheckpoint(rows *sql.Rows) (history.Checkpoint, error) {
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
func (h mysqlHistory) wrapProgress(doing string, err error) error {
	return &HistoryError{Table: h.progressTable(), Err: err, Detail: doing + ": " + err.Error()}
}

// compile-time proof that this store remembers how far a chunked operation got.
var _ history.Checkpointer = mysqlHistory{}
