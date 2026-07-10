package core

import (
	"log/slog"
	"os"
)

// DefaultLogger returns a slog.Logger writing JSON to stderr at
// info level. cmd/main.go uses this when no logger is configured
// — production callers should swap in a logger tied to the
// existing logging pipeline (or to the Discord error webhook)
// before passing the Core to plugins.
//
// Kept here rather than in core.go so a future change to the
// logging backend (e.g. add OTLP) only touches this file.
func DefaultLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}
