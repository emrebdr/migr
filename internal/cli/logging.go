package cli

import (
	"io"
	"log/slog"

	"github.com/emrebdr/migr/internal/version"
)

// newLogger builds the logger for a run from the global flags.
func newLogger(opts *options, w io.Writer) *slog.Logger {
	handlerOpts := &slog.HandlerOptions{Level: levelFor(opts.verbosity)}

	var h slog.Handler
	if opts.jsonOut {
		h = slog.NewJSONHandler(w, handlerOpts)
	} else {
		h = slog.NewTextHandler(w, handlerOpts)
	}

	return slog.New(h).With("migr", version.Version)
}

// levelFor maps -v repetitions onto a log level.
func levelFor(verbosity int) slog.Level {
	switch {
	case verbosity >= 2:
		return slog.LevelDebug
	case verbosity == 1:
		return slog.LevelInfo
	default:
		return slog.LevelWarn
	}
}
