import { defineMigration } from "@migr/sdk";

/** RenameUsers is the fixture for rename_table and rename_column, each its own inverse. */
export default defineMigration({
  async up(s) {
    await s.table("users").renameTo("people").renameColumn("email", "address").apply();
  },
  async down(s) {
    await s.table("people").renameColumn("address", "email").renameTo("users").apply();
  },
});
