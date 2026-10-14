import { replaceColumn, t } from "@migr/sdk";

/** The backfill of widening accounts.email, whose down has nothing of its own to undo. */
export default replaceColumn("accounts", "email", t.string(255))
  .with(t.string(320))
  .backfill();
