package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/history"
)

// shadowColumns is the order the shadow store binds and scans, held to history.ShadowSchema.
var shadowColumns = []string{"version", "table_name", "shadow", "retired", "capture", "stage",
	"cursor", "position", "copied", "applied", "behind", "confirmed", "updated_at"}

// shadowSaved are the columns a save moves, which leaves the confirmation as someone else wrote it.
var shadowSaved = []string{"stage", "cursor", "position", "copied", "applied", "behind", "updated_at"}

// shadowTable names the table this store's shadow-table migrations live in.
func (h pgHistory) shadowTable() string {
	return history.ShadowTable(h.table)
}

// ensureShadows creates the shadow-table state, or checks the name is held by a table and nothing else.
func (h pgHistory) ensureShadows(ctx context.Context) error {
	kind, found, err := h.relkindOf(ctx, h.shadowTable())
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

// checkShadowKind refuses a shadow-state table name held by a view, an index, or a sequence.
func (h pgHistory) checkShadowKind(kind string) error {
	if kind == ordinaryTable || kind == partitionedTable {
		return nil
	}

	return &HistoryError{Table: h.shadowTable(), Err: ErrHistoryNotATable, Detail: fmt.Sprintf(
		"%v: the name of the shadow-table state beside %q is held by a relation of kind %q",
		ErrHistoryNotATable, h.table, kind)}
}

// BeginShadow records a new shadow-table migration, unconfirmed, inside the open transaction.
func (h pgHistory) BeginShadow(ctx context.Context, s history.ShadowState) error {
	if err := s.Validate(); err != nil {
		return err
	}

	cursor, err := history.EncodeCursor(s.Cursor)
	if err != nil {
		return err
	}

	_, err = h.session.runner().Exec(ctx, "INSERT INTO "+quote(h.shadowTable())+" ("+
		quoteAll(shadowColumns)+")\nVALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, false, $12)",
		s.Version, s.Table, s.Shadow, s.Retired, s.Capture, string(s.Stage), cursor, s.Position,
		s.Copied, s.Applied, history.BehindMillis(s.Behind), s.UpdatedAt)
	if err != nil {
		return h.wrapShadow("recording a shadow-table migration", err)
	}

	return nil
}

// SaveShadow records how far one got inside the open transaction, leaving its confirmation as it is.
func (h pgHistory) SaveShadow(ctx context.Context, s history.ShadowState) error {
	if err := s.Validate(); err != nil {
		return err
	}

	cursor, err := history.EncodeCursor(s.Cursor)
	if err != nil {
		return err
	}

	_, err = h.session.runner().Exec(ctx, "UPDATE "+quote(h.shadowTable())+" SET ("+
		quoteAll(shadowSaved)+") = ($1, $2, $3, $4, $5, $6, $7)\nWHERE \"version\" = $8",
		string(s.Stage), cursor, s.Position, s.Copied, s.Applied, history.BehindMillis(s.Behind),
		s.UpdatedAt, s.Version)
	if err != nil {
		return h.wrapShadow("recording how far a shadow-table migration got", err)
	}

	return nil
}

// ConfirmShadow records that the cutover of one is confirmed, where it is recorded at all.
func (h pgHistory) ConfirmShadow(ctx context.Context, version string) error {
	_, err := h.session.runner().Exec(ctx, "UPDATE "+quote(h.shadowTable())+
		` SET "confirmed" = true WHERE "version" = $1`, version)
	if err != nil {
		return h.wrapShadow("confirming a cutover", err)
	}

	return nil
}

// ClearShadow removes the record of one, where there is one.
func (h pgHistory) ClearShadow(ctx context.Context, version string) error {
	_, err := h.session.runner().Exec(ctx, "DELETE FROM "+quote(h.shadowTable())+
		` WHERE "version" = $1`, version)
	if err != nil {
		return h.wrapShadow("clearing a shadow-table migration", err)
	}

	return nil
}

// ShadowStates returns every one recorded, and none where the table does not exist yet.
func (h pgHistory) ShadowStates(ctx context.Context) ([]history.ShadowState, error) {
	_, found, err := h.relkindOf(ctx, h.shadowTable())
	if err != nil || !found {
		return nil, err
	}

	rows, err := h.session.runner().Query(ctx, "SELECT "+quoteAll(shadowColumns)+
		"\nFROM "+quote(h.shadowTable())+"\nORDER BY \"version\"")
	if err != nil {
		return nil, h.wrapShadow("reading the shadow-table migrations", err)
	}

	defer rows.Close()

	return h.collectShadows(rows)
}

// collectShadows scans every state one read returned.
func (h pgHistory) collectShadows(rows pgx.Rows) ([]history.ShadowState, error) {
	var found []history.ShadowState

	for rows.Next() {
		var (
			s      history.ShadowState
			stage  string
			cursor string
			behind int64
		)

		if err := rows.Scan(&s.Version, &s.Table, &s.Shadow, &s.Retired, &s.Capture, &stage,
			&cursor, &s.Position, &s.Copied, &s.Applied, &behind, &s.Confirmed, &s.UpdatedAt); err != nil {
			return nil, h.wrapShadow("reading a shadow-table migration", err)
		}

		decoded, err := history.DecodeCursor(cursor)
		if err != nil {
			return nil, h.wrapShadow("reading a shadow-table migration", err)
		}

		s.Stage, s.Cursor, s.UpdatedAt = history.Stage(stage), decoded, s.UpdatedAt.UTC()
		s.Behind = history.BehindOf(behind)
		found = append(found, s)
	}

	if err := rows.Err(); err != nil {
		return nil, h.wrapShadow("reading the shadow-table migrations", err)
	}

	return found, nil
}

// wrapShadow reports a shadow-state failure, naming that table rather than the history.
func (h pgHistory) wrapShadow(doing string, err error) error {
	return &HistoryError{Table: h.shadowTable(), Err: err, Detail: doing + ": " + err.Error()}
}

// compile-time proof that this store remembers where each shadow-table migration stands.
var _ history.Shadows = pgHistory{}
