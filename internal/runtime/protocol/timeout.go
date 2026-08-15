package protocol

import "time"

// The bounds docs/protocol.md §8.2 tabulates, which migr.yaml surfaces and may override.
const (
	// DefaultHandshake bounds spawn to the initialize result, after which the runtime never started.
	DefaultHandshake = 30 * time.Second
	// DefaultRequest bounds migration.discover and migration.validate, and never migration.execute.
	DefaultRequest = 60 * time.Second
	// DefaultCancelGrace bounds a canceled request's answer before the guarantee becomes the kill.
	DefaultCancelGrace = 5 * time.Second
	// DefaultShutdownGrace bounds the shutdown result to the process exiting, before SIGTERM.
	DefaultShutdownGrace = 5 * time.Second
)

// NoTimeout is what bounds migration.execute: a backfill legitimately runs for hours (§8.2).
const NoTimeout time.Duration = 0
