import { defineMigration, t } from "@migr/sdk";

/** EveryLogicalType is the fixture that spells all eighteen logical types once each. */
export default defineMigration({
  async up(s) {
    await s
      .createTable("specimens")
      .column("c_string", t.string(255))
      .column("c_text", t.text())
      .column("c_bool", t.bool())
      .column("c_int16", t.int16())
      .column("c_int32", t.int32())
      .column("c_int64", t.int64())
      .column("c_float32", t.float32())
      .column("c_float64", t.float64())
      .column("c_decimal", t.decimal(10, 2))
      .column("c_uuid", t.uuid())
      .column("c_json", t.json())
      .column("c_timestamp", t.timestamp())
      .column("c_timestamptz", t.timestamptz())
      .column("c_date", t.date())
      .column("c_time", t.time())
      .column("c_binary", t.binary())
      .column("c_enum", t.enum("draft", "live"))
      .column("c_array", t.array(t.string(64)))
      .primaryKey("c_uuid")
      .apply();
  },
  async down(s) {
    await s.table("specimens").drop().apply();
  },
});
