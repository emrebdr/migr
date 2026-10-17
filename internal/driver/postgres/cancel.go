package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
)

// cancelFallback is how long the server has to answer a cancel request before the socket is given up on.
//
// Answering is measured in milliseconds; this bound only matters when the server cannot be reached,
// and then the session is lost either way (ADR 0083).
const cancelFallback = 5 * time.Second

// pgx's default puts a deadline on the socket, and the closed connection sends its cancel request
// afterwards, from a goroutine: measured at M13-T3, the statement did stop, but only after Exec had
// returned, and the session was gone -- so the failure could not be recorded and nothing else could
// run on it. The cancel-request handler sends the request first and waits for the backend's 57014.

// pinCancellation makes a canceled context stop the statement on the server and keep the session.
func pinCancellation(cfg *pgx.ConnConfig) {
	cfg.BuildContextWatcherHandler = func(conn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: conn, DeadlineDelay: cancelFallback}
	}
}

// stoppedBy reports a round trip whose context ended as that ending, beside what the server answered.
func stoppedBy(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil {
		return err
	}

	return fmt.Errorf("%w: %w", ctx.Err(), err)
}
