import { defineMigration, t } from "@migr/sdk";

/** AddNote is the fixture that authors no undo at all, which the engine derives one for. */
export default defineMigration({
  async up(s) {
    await s.table("people").addColumn("note", t.text()).null().apply();
  },
});
