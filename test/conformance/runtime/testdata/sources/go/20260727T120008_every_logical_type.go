package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(EveryLogicalType{}) }

// EveryLogicalType is the fixture that spells all eighteen logical types once each.
type EveryLogicalType struct{}

// Up creates a table carrying one column of every type the IR defines.
func (m EveryLogicalType) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("specimens").
		Column("c_string", migr.String(255)).
		Column("c_text", migr.Text()).
		Column("c_bool", migr.Bool()).
		Column("c_int16", migr.Int16()).
		Column("c_int32", migr.Int32()).
		Column("c_int64", migr.Int64()).
		Column("c_float32", migr.Float32()).
		Column("c_float64", migr.Float64()).
		Column("c_decimal", migr.Decimal(10, 2)).
		Column("c_uuid", migr.UUID()).
		Column("c_json", migr.JSON()).
		Column("c_timestamp", migr.Timestamp()).
		Column("c_timestamptz", migr.TimestampTZ()).
		Column("c_date", migr.Date()).
		Column("c_time", migr.Time()).
		Column("c_binary", migr.Binary()).
		Column("c_enum", migr.Enum("draft", "live")).
		Column("c_array", migr.Array(migr.String(64))).
		PrimaryKey("c_uuid").
		Apply(ctx)
}

// Down drops the table, which needs no record of what it held to be a valid operation.
func (m EveryLogicalType) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("specimens").Drop().Apply(ctx)
}
