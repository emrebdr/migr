import assert from "node:assert/strict";
import { test } from "node:test";

import { capture, captureDown, project, reversal } from "@migr/sdk/testing";

import createOrders from "../migrations/20261101T110000_create_orders.ts";
import addStatus from "../migrations/20261101T110100_add_orders_status.ts";
import backfillStatus from "../migrations/20261101T110200_backfill_orders_status.ts";

test("create_orders has its down derived", async () => {
  assert.equal((await reversal(createOrders)).undo, "derived");
});

test("add_orders_status authors a down that drops the column it added", async () => {
  assert.deepEqual((await capture(addStatus)).kinds(), ["add_column"]);
  assert.deepEqual((await captureDown(addStatus)).kinds(), ["drop_column"]);
});

test("backfill_orders_status writes rows and its down emits nothing", async () => {
  assert.deepEqual((await capture(backfillStatus)).kinds(), ["data_backfill"]);
  assert.deepEqual((await captureDown(backfillStatus)).kinds(), []);
});

test("the migrations apply and roll back, leaving nothing behind", () => {
  const p = project({ dir: "migrations" });
  try {
    assert.deepEqual(p.validate().problems, []);
    p.roundTrip();
  } finally {
    p.remove();
  }
});
