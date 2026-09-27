import { defineMigration } from "@migr/sdk";

/** ConstrainOrders is the fixture for add_check, drop_check, add_foreign_key, and drop_foreign_key. */
export default defineMigration({
  async up(s) {
    await s
      .table("orders")
      .addCheck("ck_orders_total", "total >= 0")
      .addForeignKey("fk_orders_person", ["person_id"], "people", ["id"])
      .onDelete("cascade")
      .onUpdate("restrict")
      .apply();
  },
  async down(s) {
    await s
      .table("orders")
      .dropForeignKey("fk_orders_person")
      .was({
        columns: ["person_id"],
        references_table: "people",
        references_columns: ["id"],
        on_delete: "cascade",
        on_update: "restrict",
      })
      .dropCheck("ck_orders_total")
      .was({ expression: "total >= 0" })
      .apply();
  },
});
