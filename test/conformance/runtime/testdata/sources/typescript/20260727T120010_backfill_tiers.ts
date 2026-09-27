import type { Rows } from "@migr/sdk";
import { defineMigration, literal, t } from "@migr/sdk";

/** counted reports whether a read found anything, which is the branch each pinned answer decides. */
function counted(rows: Rows): boolean {
  const row = rows.rows[0];

  if (rows.rows.length !== 1 || row === undefined || row.length !== 1) {
    return false;
  }

  const value = row[0];

  return value !== undefined && value !== null && value !== "0";
}

/** BackfillTiers is the fixture that reads, branches on what it read, and reaches the database itself. */
export default defineMigration({
  async up(s) {
    const tiers = await s.query("SELECT count(*) FROM people WHERE tier IS NULL");
    const notes = await s.query("SELECT count(*) FROM people WHERE note IS NULL");

    await s.exec("UPDATE people SET tier = 'free' WHERE tier IS NULL");

    await s.raw("postgres", "ANALYZE people").apply();

    const people = s.table("people").addColumn("tier_backfilled", t.bool());

    if (counted(tiers)) {
      people.default(literal("true"));
    }

    if (counted(notes)) {
      people.null();
    }

    await people.apply();
  },
  async down(s) {
    await s
      .table("people")
      .dropColumn("tier_backfilled")
      .was({ name: "tier_backfilled", type: t.bool(), default: literal("true") })
      .apply();

    await s.raw("postgres", "ANALYZE people").apply();
  },
});
