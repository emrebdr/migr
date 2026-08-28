package typescript

// Ext is the extension a TypeScript migration is stored under, and the one migr create writes.
const Ext = ".ts"

// SDK is the package a TypeScript migration imports, which a new project adds with npm install.
const SDK = "@migr/sdk"

// Template is the file migr create writes, which typechecks once the SDK is in node_modules.
const Template = `import { defineMigration, t } from "` + SDK + `";

/**
 * What this migration does. Rename the file to something the next reader will recognize.
 *
 * migr reads either of two styles and a module uses one of them, never both. This is the
 * default-export style; the other exports the parts one by one:
 *
 *   export async function up(s) { ... }
 *   export async function down(s) { ... }
 *   export const dependencies = ["20260727T153000"];
 *
 * The default-export style is the one that needs no annotations: s is inferred, and a
 * migration that both writes a down and declares it has none does not compile.
 */
export default defineMigration({
  /**
   * up performs the change. Every operation it emits is recorded, compiled for the target
   * engine, and run in one transaction:
   *
   *   await s.createTable("users")
   *     .column("id", t.uuid())
   *     .column("email", t.string(255))
   *     .column("nickname", t.text()).null()
   *     .primaryKey("id")
   *     .apply();
   *
   * s.table(name) does the same for a table that already exists, and s.view(name) for a
   * view. A column is NOT NULL unless the chain says null.
   *
   * Nothing is emitted until apply, and a chain built without it emits nothing at all --
   * so migr fails such a migration rather than reporting a change it never made.
   */
  async up(s) {},

  /**
   * down undoes it. Delete this member and migr derives the inverse from what up emitted:
   *
   *   await s.table("users").drop().was({ columns: [...] }).apply();
   *
   * One thing has no inverse to derive: a statement this migration sends the database
   * itself. That is committed on its own, before the transaction the operations run in,
   * and a later failure does not undo it -- so a migration that sends one must write this
   * member, or declare itself irreversible instead:
   *
   *   irreversible: "the reason a rollback halts with",
   *
   * A migration declares one or the other. Both is refused.
   */
  async down(s) {},
});
`
