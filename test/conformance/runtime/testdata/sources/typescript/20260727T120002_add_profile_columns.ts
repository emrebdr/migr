// The one fixture carrying an enum, which is what proves the transpiler ran.
//
// Node strips types natively but refuses an enum, so a fixture set written in erasable syntax alone
// would pass with esbuild switched off. One file carrying one is enough: a file that cannot be
// imported fails the whole migration.discover, which takes the run with it.

import { defineMigration, literal, t } from "@migr/sdk";

/** Tier names the default a new account starts on. */
enum Tier {
  Free = "free",
}

/** AddProfileColumns is the fixture for add_column, drop_column, and alter_column. */
export default defineMigration({
  async up(s) {
    await s
      .table("users")
      .addColumn("bio", t.text())
      .null()
      .addColumn("tier", t.string(16))
      .default(literal(Tier.Free))
      .alterColumn("email")
      .type(t.string(320))
      .wasType(t.string(255))
      .apply();
  },
  async down(s) {
    await s
      .table("users")
      .alterColumn("email")
      .type(t.string(255))
      .wasType(t.string(320))
      .dropColumn("tier")
      .was({ name: "tier", type: t.string(16), default: literal(Tier.Free) })
      .dropColumn("bio")
      .was({ name: "bio", type: t.text(), nullable: true })
      .apply();
  },
});
