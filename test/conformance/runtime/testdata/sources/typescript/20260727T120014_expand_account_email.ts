import { replaceColumn, t } from "@migr/sdk";

/** The expand of widening accounts.email: the column it is rebuilt in, added nullable. */
export default replaceColumn("accounts", "email", t.string(255))
  .with(t.string(320))
  .expand();
