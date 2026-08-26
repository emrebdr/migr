package golang

// Ext is the extension a Go migration is stored under, and the one migr create writes.
const Ext = ".go"

// SDK is the module a Go migration imports, which a new project adds with go get.
const SDK = "github.com/emrebdr/migr/sdk/go"

// Template is the file migr create writes, which compiles once the SDK is in go.mod.
const Template = `package migrations

import (
	"context"

	migr "` + SDK + `"
)

func init() { migr.Register(Migration{}) }

// Migration is what this file does. Rename it to something the next reader will recognize.
type Migration struct{}

// Up performs the change. Every operation it emits is recorded, compiled for the target
// engine, and run in one transaction:
//
//	return s.CreateTable("users").
//		Column("id", migr.UUID()).
//		Column("email", migr.String(255)).
//		Column("created_at", migr.TimestampTZ()).Default(migr.Call(migr.FuncCurrentTimestamp)).
//		PrimaryKey("id").
//		Apply(ctx)
//
// s.Table(name) does the same for a table that already exists, and s.View(name) for a
// view. A column is NOT NULL unless the chain says Null.
//
// Nothing is emitted until Apply, and a chain built without it emits nothing at all --
// so migr fails such a migration rather than reporting a change it never made.
func (m Migration) Up(ctx context.Context, s *migr.Schema) error {
	return nil
}

// Down undoes it. Delete this method and migr derives the inverse from what Up emitted.
//
// One thing has no inverse to derive: a statement this migration sends itself with
// s.Exec. That is committed on its own, before the transaction the operations run in,
// and a later failure does not undo it -- so a migration that calls s.Exec must write
// this method, or declare itself irreversible:
//
//	func (m Migration) Irreversible() string { return "the reason a rollback halts with" }
//
// A migration declares one or the other. Both is a registration error.
func (m Migration) Down(ctx context.Context, s *migr.Schema) error {
	return nil
}
`
