package cli

import (
	"fmt"
	"log/slog"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
)

// repairerOf returns the session as a driver.Repairer, nil where it cleans up nothing.
func repairerOf(conn driver.Conn) driver.Repairer {
	if r, repairs := conn.(driver.Repairer); repairs {
		return r
	}

	return nil
}

// logRepair warns of every repair a run made, since migr changing the database on its own must never be quiet.
func logRepair(log *slog.Logger) func(engine.Cleanup) {
	return func(c engine.Cleanup) {
		attrs := []any{"migration", c.Migration.Filename(), "statement", c.Position, "when", string(c.When)}

		if c.Err != nil {
			log.Warn("could not clean up what a statement outside any transaction left behind",
				append(attrs, "error", c.Err.Error())...)

			return
		}

		log.Warn("cleaned up what a statement outside any transaction left behind",
			append(attrs, "action", c.Action, "skipped", c.Done)...)
	}
}

// repairsOf renders each repair a run made as the sentence a summary prints.
func repairsOf(repairs []engine.Cleanup) []string {
	var out []string

	for _, c := range repairs {
		switch {
		case c.Err != nil:
			out = append(out, fmt.Sprintf("statement %d: %v", c.Position, c.Err))
		case c.Done:
			out = append(out, fmt.Sprintf("statement %d: %s, so it was not run again", c.Position, c.Action))
		default:
			out = append(out, fmt.Sprintf("statement %d: %s", c.Position, c.Action))
		}
	}

	return out
}
