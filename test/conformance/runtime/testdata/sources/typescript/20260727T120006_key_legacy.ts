import { columns, defineMigration } from "@migr/sdk";

/** KeyLegacy is the fixture for add_primary_key and drop_primary_key, which key a table that exists. */
export default defineMigration({
  async up(s) {
    await s.table("legacy").addPrimaryKey("pk_legacy", "id").apply();
  },
  async down(s) {
    await s.table("legacy").dropPrimaryKey("pk_legacy").was(columns("id")).apply();
  },
});
