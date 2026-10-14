import { replaceColumn, t } from "@migr/sdk";

/** The swap of widening accounts.email: both columns change nullability, then names. */
export default replaceColumn("accounts", "email", t.string(255))
  .with(t.string(320))
  .swap();
