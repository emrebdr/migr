import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s
      .createTable("orders")
      .column("id", t.int64())
      .column("customer", t.string(100))
      .column("total", t.decimal(10, 2))
      .column("placed_at", t.timestamptz())
      .default({ function: "current_timestamp" })
      .primaryKey("id")
      .apply();
  },
});
