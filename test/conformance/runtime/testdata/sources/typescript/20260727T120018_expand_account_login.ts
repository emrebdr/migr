import { literal, replaceColumn, t } from "@migr/sdk";

/** The expand of renaming accounts.handle to login, which carries a default from the start. */
export default replaceColumn("accounts", "handle", t.string(64))
  .wasNull()
  .wasDefault(literal("anon"))
  .renameTo("login")
  .default(literal("guest"))
  .through("lower")
  .expand();
