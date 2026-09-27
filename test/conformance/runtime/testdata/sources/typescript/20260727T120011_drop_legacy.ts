import { defineMigration } from "@migr/sdk";

/** DropLegacy is the fixture whose author states it cannot be undone, and says why. */
export default defineMigration({
  async up(s) {
    await s.table("legacy").drop().apply();
  },
  irreversible: "the rows in legacy are not recorded anywhere; restore from a backup",
});
