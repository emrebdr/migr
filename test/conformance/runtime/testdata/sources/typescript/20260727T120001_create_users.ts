// The conformance fixture set expressed in TypeScript.
//
// One file per manifest, under the same stem, written the way an author writes one: the builder
// rather than operation literals, and the default export migr create --lang ts writes.

import { call, defineMigration, t } from "@migr/sdk";

/** CreateUsers is the fixture for create_table and drop_table. */
export default defineMigration({
  async up(s) {
    await s
      .createTable("users")
      .column("id", t.uuid())
      .column("email", t.string(255))
      .column("created_at", t.timestamptz())
      .null()
      .default(call("current_timestamp"))
      .primaryKey("id")
      .apply();
  },
  async down(s) {
    await s
      .table("users")
      .drop()
      .was({
        columns: [
          { name: "id", type: t.uuid() },
          { name: "email", type: t.string(255) },
          {
            name: "created_at",
            type: t.timestamptz(),
            nullable: true,
            default: call("current_timestamp"),
          },
        ],
        primary_key: ["id"],
      })
      .apply();
  },
});
