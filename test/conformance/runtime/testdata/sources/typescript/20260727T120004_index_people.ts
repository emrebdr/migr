import { columns, defineMigration } from "@migr/sdk";

/** IndexPeople is the fixture for add_index, drop_index, add_unique, and drop_unique. */
export default defineMigration({
  async up(s) {
    await s
      .table("people")
      .addIndex("idx_people_tier", "tier")
      .addUnique("uq_people_address", "address")
      .apply();
  },
  async down(s) {
    await s
      .table("people")
      .dropUnique("uq_people_address")
      .was(columns("address"))
      .dropIndex("idx_people_tier")
      .was(columns("tier"))
      .apply();
  },
});
