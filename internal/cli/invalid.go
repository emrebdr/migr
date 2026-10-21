package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
)

// invalidIndex is one index the server maintains on every write and lets no query use, as a report states it.
type invalidIndex struct {
	// Table is the table it is on.
	Table string `json:"table"`
	// Index is its name.
	Index string `json:"index"`
	// Definition is the server's own rendering of it.
	Definition string `json:"definition"`
}

// invalidIndexesOf reads the INVALID indexes a session's namespace holds, none where the engine has no such state.
func invalidIndexesOf(ctx context.Context, conn driver.Conn) ([]invalidIndex, error) {
	reader, reads := conn.(driver.InvalidIndexReader)
	if !reads {
		return nil, nil
	}

	found, err := reader.InvalidIndexes(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]invalidIndex, 0, len(found))
	for _, one := range found {
		out = append(out, invalidIndex{Table: one.Table, Index: one.Index, Definition: one.Definition})
	}

	return out, nil
}

// writeInvalidIndexes lists every INVALID index under a heading, each with both ways to fix it, and nothing where there are none.
func writeInvalidIndexes(b *strings.Builder, found []invalidIndex) {
	if len(found) == 0 {
		return
	}

	fmt.Fprintf(b, "\nINVALID indexes -- each is maintained on every write and used by no query, left by a "+
		"concurrent build or drop that failed:\n")

	for _, one := range found {
		fmt.Fprintf(b, "  !  %s on %s: rebuild it with REINDEX INDEX CONCURRENTLY %s, or remove it with "+
			"DROP INDEX CONCURRENTLY %s\n", one.Index, one.Table, quoted(one.Index), quoted(one.Index))
	}
}

// quoted renders a name the way both remedies spell it.
func quoted(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// warnInvalidIndexes logs every INVALID index a run left behind it, since a run that succeeds must not be silent about one.
func warnInvalidIndexes(ctx context.Context, log *slog.Logger, conn driver.Conn) {
	found, err := invalidIndexesOf(ctx, conn)
	if err != nil {
		log.Warn("could not read whether any index is INVALID", "error", err.Error())

		return
	}

	for _, one := range found {
		log.Warn("an INVALID index is maintained on every write and used by no query", "table", one.Table,
			"index", one.Index, "remedy", "REINDEX INDEX CONCURRENTLY "+quoted(one.Index)+
				" or DROP INDEX CONCURRENTLY "+quoted(one.Index))
	}
}
