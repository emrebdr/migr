import { defineMigration, t } from "@migr/sdk";

export default defineMigration({
  dependencies: ["20261101T090100"],
  async up(s) {
    await s
      .table("books")
      .addColumn("isbn", t.string(13))
      .null()
      .addUnique("uq_books_isbn", "isbn")
      .apply();
  },
});
