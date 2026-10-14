import { defineMigration, literal, t } from "@migr/sdk";

/** ShapePeople is the fixture that writes rows rather than schema, which irVersion 2 added. */
export default defineMigration({
  async up(s) {
    await s
      .table("people")
      .backfill("nickname", t.string(64), literal("anonymous"))
      .transform("email_key", t.string(255), "email", "lower")
      .copyInto("people_archive", { from: "id", to: "id" }, { from: "email", to: "email" })
      .apply();
  },

  irreversible: "every operation here writes rows, and what they replaced is recorded nowhere",
});
