// Package sqlite implements the Driver contract for SQLite using
// modernc.org/sqlite, which is pure Go and so keeps CGO off and
// cross-compilation trivial.
//
// SQLite cannot drop or alter columns directly on older versions, so this is
// the driver that exercises the capability system's emulation path: the
// table-rebuild strategy must preserve indexes, triggers, views, and foreign
// keys. Version-dependent cases are detected at runtime — DROP COLUMN is native
// from 3.35 — never assumed. Its single-writer model means most operations
// block writers for their full duration, and the capability set says so.
//
// Status: scaffolding. Implemented across M8 (see TASKS.md).
package sqlite
