import { test } from "node:test";
import assert from "node:assert/strict";

import { next, cameBack } from "./updating.mjs";

test("what is asked next follows the state", () => {
  assert.deepEqual(next({ state: "checking" }), { ask: "status" });
  assert.deepEqual(next({ state: "waiting", installed: "v1.2.4" }), { ask: "status" });
  assert.deepEqual(next({ state: "restarting", installed: "v1.2.4" }), { ask: "health" });
  assert.deepEqual(next({ state: "idle" }), { ask: "nothing" });
});

test("no answer is the program between copies: ask /healthz", () => {
  assert.deepEqual(next(null), { ask: "health" });
});

test("only the version restarted into has come back", () => {
  assert.equal(cameBack("ok v1.2.4\n", "v1.2.4"), true);
  assert.equal(cameBack("ok v1.2.3\n", "v1.2.4"), false);
  assert.equal(cameBack("ok v1.2.40\n", "v1.2.4"), false);
  assert.equal(cameBack("unhealthy v1.2.4: no such table\n", "v1.2.4"), false);
  assert.equal(cameBack("ok v1.2.4\n", ""), true);
});
