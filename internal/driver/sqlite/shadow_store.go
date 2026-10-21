package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/history"
)

// shadowColumns is the order the shadow store binds and scans, held to history.ShadowSchema.
var shadowColumns = []string{"version", "table_name", "shadow", "retired", "capture", "stage",
	"cursor", "position", "copied", "applied", "behind", "confirmed", "updated_at"}

// shadowSaved are the columns a save moves, which leaves the confirmation as someone else wrote it.
var shadowSaved = []string{"stage", "cursor", "position", "copied", "applied", "behind", "updated_at"}

// shadowTable names the table this store's shadow-table migrations live in.
func (h sqliteHistory) shadowTable() string {
	return history.ShadowTable(h.table)
}

// ensureShadows creates the shadow-table state, or checks the name is held by a table and nothing else.
func (h sqliteHistory) ensureShadows(ctx context.Context) error {
	kind, found, err := h.kindOf(ctx, h.shadowTable())
	if err != nil {
		return err
	}

	if found {
		return h.checkShadowKind(kind)
	}

	stmts, err := h.compileSchema(history.ShadowSchema(h.table))
	if err != nil {
		return err
	}

	return h.session.inTransaction(ctx, func(ctx context.Context) error {
		return h.run(ctx, stmts)
	})
}

// checkShadowKind refuses a shadow-state table name held by a view, an index, or a trigger.
func (h sqliteHistory) checkShadowKind(kind string) error {
	if kind == ordinaryTable {
		return nil
	}

	return &HistoryError{Table: h.shadowTable(), Err: ErrHistoryNotATable, Detail: fmt.Sprintf(
		"%v: the name of the shadow-table state beside %q is held by a %s", ErrHistoryNotATable,
		h.table, kind)}
}

// BeginShadow records a new shadow-table migration, unconfirmed, inside the open transaction.
func (h sqliteHistory) BeginShadow(ctx context.Context, s history.ShadowState) error {
	if err := s.Validate(); err != nil {
		return err
	}

	cursor, err := history.EncodeCursor(s.Cursor)
	if err != nil {
		return err
	}

	_, err = h.session.runner().ExecContext(ctx, "INSERT INTO "+quote(h.shadowTable())+" ("+
		quoteAll(shadowColumns)+")\nVALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, false, ?)", s.Version,
		s.Table, s.Shadow, s.Retired, s.Capture, string(s.Stage), cursor, s.Position, s.Copied,
		s.Applied, history.BehindMillis(s.Behind), renderTime(s.UpdatedAt))
	if err != nil {
		return h.wrapShadow("recording a shadow-table migration", err)
	}

	return nil
}

// SaveShadow records how far one got inside the open transaction, leaving its confirmation as it is.
func (h sqliteHistory) SaveShadow(ctx context.Context, s history.ShadowState) error {
	if err := s.Validate(); err != nil {
		return err
	}

	cursor, err := history.EncodeCursor(s.Cursor)
	if err != nil {
		return err
	}

	assigned := make([]string, 0, len(shadowSaved))
	for _, column := range shadowSaved {
		assigned = append(assigned, quote(column)+" = ?")
	}

	_, err = h.session.runner().ExecContext(ctx, "UPDATE "+quote(h.shadowTable())+" SET "+
		strings.Join(assigned, ", ")+"\nWHERE \"version\" = ?", string(s.Stage), cursor, s.Position,
		s.Copied, s.Applied, history.BehindMillis(s.Behind), renderTime(s.UpdatedAt), s.Version)
	if err != nil {
		return h.wrapShadow("recording how far a shadow-table migration got", err)
	}

	return nil
}

// ConfirmShadow records that the cutover of one is confirmed, where it is recorded at all.
func (h sqliteHistory) ConfirmShadow(ctx context.Context, version string) error {
	_, err := h.session.runner().ExecContext(ctx, "UPDATE "+quote(h.shadowTable())+
		` SET "confirmed" = true WHERE "version" = ?`, version)
	if err != nil {
		return h.wrapShadow("confirming a cutover", err)
	}

	return nil
}

// ClearShadow removes the record of one, where there is one.
func (h sqliteHistory) ClearShadow(ctx context.Context, version string) error {
	_, err := h.session.runner().ExecContext(ctx, "DELETE FROM "+quote(h.shadowTable())+
		` WHERE "version" = ?`, version)
	if err != nil {
		return h.wrapShadow("clearing a shadow-table migration", err)
	}

	return nil
}

// ShadowStates returns every one recorded, and none where the table does not exist yet.
func (h sqliteHistory) ShadowStates(ctx context.Context) ([]history.ShadowState, error) {
	_, found, err := h.kindOf(ctx, h.shadowTable())
	if err != nil || !found {
		return nil, err
	}

	rows, err := h.session.runner().QueryContext(ctx, "SELECT "+quoteAll(shadowColumns)+
		"\nFROM "+quote(h.shadowTable())+"\nORDER BY \"version\"")
	if err != nil {
		return nil, h.wrapShadow("reading the shadow-table migrations", err)
	}

	defer rows.Close()

	return h.collectShadows(rows)
}

// collectShadows scans every state one read returned.
func (h sqliteHistory) collectShadows(rows *sql.Rows) ([]history.ShadowState, error) {
	var found []history.ShadowState

	for rows.Next() {
		s, err := h.scanShadow(rows)
		if err != nil {
			return nil, err
		}

		found = append(found, s)
	}

	if err := rows.Err(); err != nil {
		return nil, h.wrapShadow("reading the shadow-table migrations", err)
	}

	return found, nil
}

// scanShadow reads one state, decoding the cursor and the instant it was stored with.
func (h sqliteHistory) scanShadow(rows *sql.Rows) (history.ShadowState, error) {
	var (
		s       history.ShadowState
		stage   string
		cursor  string
		updated string
		behind  int64
	)

	if err := rows.Scan(&s.Version, &s.Table, &s.Shadow, &s.Retired, &s.Capture, &stage, &cursor,
		&s.Position, &s.Copied, &s.Applied, &behind, &s.Confirmed, &updated); err != nil {
		return history.ShadowState{}, h.wrapShadow("reading a shadow-table migration", err)
	}

	decoded, err := history.DecodeCursor(cursor)
	if err != nil {
		return history.ShadowState{}, h.wrapShadow("reading a shadow-table migration", err)
	}

	at, err := parseTime(updated)
	if err != nil {
		return history.ShadowState{}, h.wrapShadow("reading a shadow-table migration", err)
	}

	s.Stage, s.Cursor, s.UpdatedAt, s.Behind = history.Stage(stage), decoded, at, history.BehindOf(behind)

	return s, nil
}

// wrapShadow reports a shadow-state failure, naming that table rather than the history.
func (h sqliteHistory) wrapShadow(doing string, err error) error {
	return &HistoryError{Table: h.shadowTable(), Err: err, Detail: doing + ": " + err.Error()}
}

// compile-time proof that this store remembers where each shadow-table migration stands.
var _ history.Shadows = sqliteHistory{}
