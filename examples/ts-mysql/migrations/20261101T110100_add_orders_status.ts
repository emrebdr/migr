import { defineMigration, t } from "@migr/sdk";

const status = t.enum("pending", "paid", "shipped");

export default defineMigration({
  async up(s) {
    await s.table("orders").addColumn("status", status).null().apply();
  },
  async down(s) {
    await s
      .table("orders")
      .dropColumn("status")
      .was({ name: "status", type: status, nullable: true })
      .apply();
  },
});
