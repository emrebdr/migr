package sqlite

import (
	"context"
	"fmt"

	"github.com/emrebdr/migr/internal/capture"
)

// errNoCapture is why this driver opens no change stream, which every shadow-table migration needs.
var errNoCapture = fmt.Errorf("%w: SQLite has no replication log to read, so no shadow table can be kept current", capture.ErrUnsupported)

// Capture refuses to open a change stream, since this driver reads no replication log.
func (d *sqliteDriver) Capture(context.Context, string, capture.Request) (capture.Stream, error) {
	return nil, errNoCapture
}
