package postgres

import (
	"context"
	"fmt"

	"github.com/emrebdr/migr/internal/capture"
)

// errNoCapture is why this driver opens no change stream, which every shadow-table migration needs.
var errNoCapture = fmt.Errorf("%w: the postgres driver does not read PostgreSQL's logical decoding yet, so it cannot keep a shadow table current", capture.ErrUnsupported)

// Capture refuses to open a change stream, since this driver reads no replication log.
func (d *pgDriver) Capture(context.Context, string, capture.Request) (capture.Stream, error) {
	return nil, errNoCapture
}
