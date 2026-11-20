// make test-js: the form's pure parts, under Node's own test runner.
import { test } from "node:test";
import assert from "node:assert/strict";

import { debounce, fillEmpty, previewQuery, summary } from "./form.mjs";

test("the preview's address has the fields typed, and only those", () => {
  const q = previewQuery({ account: "checking-a", number: "1043", payee: "Payee A & Sons", amount: "", stub_note: "line 1\nline 2" });
  const p = new URLSearchParams(q);

  assert.equal(p.get("account"), "checking-a");
  assert.equal(p.get("payee"), "Payee A & Sons");
  assert.equal(p.get("note"), "line 1\nline 2");
  assert.equal(p.has("amount"), false);
  assert.equal(p.has("stub_note"), false);
});

test("the button says what it will print, once it knows", () => {
  assert.equal(summary({ number: "1043", payee: " Payee A " }, "$1,234.50"), "Print check 1043 to Payee A for $1,234.50");
  assert.equal(summary({ number: "1043", payee: "Payee A" }, ""), "Print check");
  assert.equal(summary({ number: "", payee: "Payee A" }, "$1.00"), "Print check");
  assert.equal(summary({ number: "10a", payee: "Payee A" }, "$1.00"), "Print check");
  assert.equal(summary({ number: "1043", payee: "  " }, "$1.00"), "Print check");
});

test("a payee's last check fills what is empty, and nothing typed", () => {
  const last = { found: true, amount: "125.00", memo: "Cleaning", stub_note: "Weekly" };

  assert.deepEqual(fillEmpty({ amount: "", memo: "", stub_note: "" }, last), { amount: "125.00", memo: "Cleaning", stub_note: "Weekly" });
  assert.deepEqual(fillEmpty({ amount: "90", memo: " ", stub_note: "Mine" }, last), { memo: "Cleaning" });
  assert.deepEqual(fillEmpty({ amount: "" }, { found: false, amount: "1.00" }), {});
});

test("debounce waits for a pause", async () => {
  let calls = 0;
  const f = debounce(() => calls++, 20);

  f();
  f();
  f();
  await new Promise((done) => setTimeout(done, 60));

  assert.equal(calls, 1);
});
