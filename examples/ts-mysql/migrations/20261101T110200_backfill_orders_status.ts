import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s
      .table("orders")
      .backfill("status", t.enum("pending", "paid", "shipped"), { literal: "pending" })
      .apply();
  },
  async down() {},
});
