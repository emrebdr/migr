import { columns, defineMigration, SHADOW_TABLE } from "@migr/sdk";

/** IndexPeopleOnline is the fixture for the online declaration, which every SDK reports identically. */
export default defineMigration({
  online: SHADOW_TABLE,
  async up(s) {
    await s.table("people").addIndex("idx_people_region", "region").apply();
  },
  async down(s) {
    await s.table("people").dropIndex("idx_people_region").was(columns("region")).apply();
  },
});
