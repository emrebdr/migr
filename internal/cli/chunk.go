package cli

import (
	"log/slog"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
)

// chunkerOf returns the session that runs a data operation a chunk at a time, where the engine has one.
func chunkerOf(conn driver.Conn) driver.Chunker {
	chunker, chunks := conn.(driver.Chunker)
	if !chunks {
		return nil
	}

	return chunker
}

// paced gives a run its chunk size, its pacer, and the progress it logs, all from migr.yaml.
func (t *target) paced(options engine.Options, conn driver.Conn, log *slog.Logger) engine.Options {
	rows := t.cfg.Safety.Rows()

	options.Planner.ChunkRows = rows
	options.Chunker = chunkerOf(conn)
	options.Pacer = t.pacerFor(engine.Steady{Rows: rows, Pause: t.cfg.Safety.ChunkPause.Duration()}, conn)
	options.Progress = logProgress(log)

	return options
}

// pacerFor holds chunks back on replica lag where the session reads it, and paces steadily otherwise.
func (t *target) pacerFor(steady engine.Steady, conn driver.Conn) engine.Pacer {
	reader, reads := conn.(driver.LagReader)
	if !reads {
		return steady
	}

	return engine.Lagged{Steady: steady, Reader: reader,
		Threshold: t.cfg.Safety.ReplicaLagThreshold.Duration()}
}

// logProgress logs every committed chunk at progress level, which is what -v asks for.
func logProgress(log *slog.Logger) func(engine.Progress) {
	return func(p engine.Progress) {
		attrs := []any{
			"migration", p.Migration.Filename(), "operation", p.Index, "kind", p.Op.Kind(),
			"table", p.Table, "chunk", p.Chunks, "written", p.Written,
			"through", strings.Join(p.Cursor, ", "), "done", p.Done,
		}

		if p.Throttle != "" {
			attrs = append(attrs, "throttle", p.Throttle)
		}

		if p.Resumed {
			attrs = append(attrs, "resumed", true)
		}

		if p.Waiting {
			log.Info("holding the next chunk back", attrs...)

			return
		}

		log.Info("committed a chunk", attrs...)
	}
}
