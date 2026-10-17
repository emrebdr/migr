package cli

import (
	"crypto/rand"
	"log/slog"
	"math/big"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
)

// retrying gives a run the backoff migr.yaml's safety block describes, and logs every retry it takes.
func (t *target) retrying(options engine.Options, log *slog.Logger) engine.Options {
	options.Backoff = engine.Exponential{
		Base:    t.cfg.Safety.LockBackoff.Duration(),
		Cap:     driver.MaxLockBackoff,
		Retries: t.cfg.Safety.Retries(),
		Jitter:  jitter,
	}
	options.Retried = logRetry(log, t.cfg.Safety.Retries())

	return options
}

// jitter draws a duration in [0, span], the random half of every backoff, and the whole span on failure.
func jitter(span time.Duration) time.Duration {
	if span <= 0 {
		return 0
	}

	drawn, err := rand.Int(rand.Reader, big.NewInt(int64(span)+1))
	if err != nil {
		return span
	}

	return time.Duration(drawn.Int64())
}

// logRetry warns of every lock wait spent, since contention is what an operator has to see unasked.
func logRetry(log *slog.Logger, retries int) func(engine.Retry) {
	return func(r engine.Retry) {
		attrs := []any{
			"migration", r.Migration.Filename(), "direction", r.Direction,
			"retry", r.Retry, "of", retries, "backoff", r.Wait.Round(time.Millisecond).String(),
		}

		switch {
		case r.Chunk > 0:
			attrs = append(attrs, "chunk", r.Chunk)
		case r.Statement > 0:
			attrs = append(attrs, "statement", r.Statement)
		default:
			attrs = append(attrs, "statement", "the commit")
		}

		log.Warn("gave up waiting for a lock, and will try again", attrs...)
	}
}
