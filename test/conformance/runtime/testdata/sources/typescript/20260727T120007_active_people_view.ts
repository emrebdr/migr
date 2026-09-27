import { defineMigration } from "@migr/sdk";

/** ActivePeopleView is the fixture for create_view and drop_view, whose queries are engine SQL. */
export default defineMigration({
  async up(s) {
    await s
      .view("active_people")
      .create("SELECT id, address FROM people WHERE tier = 'paid'")
      .apply();

    await s
      .view("recent_people")
      .replace("SELECT id FROM people ORDER BY created_at DESC")
      .was("SELECT id FROM people")
      .apply();
  },
  async down(s) {
    await s
      .view("recent_people")
      .drop()
      .was("SELECT id FROM people ORDER BY created_at DESC")
      .apply();

    await s
      .view("active_people")
      .drop()
      .was("SELECT id, address FROM people WHERE tier = 'paid'")
      .apply();
  },
});
