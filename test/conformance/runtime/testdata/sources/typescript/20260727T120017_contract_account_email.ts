import { replaceColumn, t } from "@migr/sdk";

/** The contract of widening accounts.email, whose down copies the old values back. */
export default replaceColumn("accounts", "email", t.string(255))
  .with(t.string(320))
  .contract();
