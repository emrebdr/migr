import assert from "node:assert/strict";
import { test } from "node:test";

import { distTag, publishOrder } from "../publish.mjs";

test("every platform package is published before the wrapper that names them", () => {
  const order = publishOrder(["cli", "cli-linux-amd64", "cli-darwin-arm64", "cli-windows-arm64"]);

  assert.equal(order.at(-1), "cli");
  assert.deepEqual(order.slice(0, -1), ["cli-darwin-arm64", "cli-linux-amd64", "cli-windows-arm64"]);
});

test("publishing with no wrapper built is refused", () => {
  assert.throws(() => publishOrder(["cli-linux-amd64"]), /run build\.mjs first/);
});

test("a pre-release is published under next, so npx @migr/cli keeps the latest final release", () => {
  assert.equal(distTag("1.2.3"), "latest");
  assert.equal(distTag("1.2.3-rc.1"), "next");
  assert.equal(distTag("0.0.1-next"), "next");
});
