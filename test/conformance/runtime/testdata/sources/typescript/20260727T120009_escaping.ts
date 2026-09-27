import { defineMigration } from "@migr/sdk";

/** expression carries every character two SDKs are most likely to render differently. */
const expression = "total >= 0 AND note <> 'a&b' AND label <> 'naïve — \"ok\"'";

/** query carries a comparison a JSON encoder that escapes HTML would rewrite. */
const query = "SELECT id FROM orders WHERE total < 100 AND note <> 'a&b'";

/** Escaping is the fixture that pins docs/ir.md §3.3: <, > and & cross the wire unescaped. */
export default defineMigration({
  async up(s) {
    await s.table("orders").addCheck("ck_orders_sane", expression).apply();

    await s.view("small_orders").create(query).apply();
  },
  async down(s) {
    await s.view("small_orders").drop().was(query).apply();

    await s.table("orders").dropCheck("ck_orders_sane").was({ expression }).apply();
  },
});
