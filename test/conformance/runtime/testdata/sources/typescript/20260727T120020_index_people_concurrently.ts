import { defineMigration } from "@migr/sdk";

/** IndexPeopleConcurrently is the fixture for raw's no_transaction, which irVersion 3 carries. */
export default defineMigration({
  async up(s) {
    await s
      .raw("postgres", "CREATE INDEX CONCURRENTLY idx_people_nickname ON people (nickname)")
      .noTransaction()
      .raw("postgres", "ANALYZE people")
      .apply();
  },
  async down(s) {
    await s.raw("postgres", "DROP INDEX CONCURRENTLY idx_people_nickname").noTransaction().apply();
  },
});
