// Writing a check in a real Chrome against the real program: `make
// test-browser`.
//
// What only a browser can say: that the page's script runs under the header
// policy (a blocked script looks, to everything but a browser, like a page
// that works); that the preview, the amount in words and the Print button
// follow the typing; that choosing a payee already paid offers its last
// check; and that the whole of writing, printing and confirming works with
// the script in the page. The printer is a folder (scripts/testserver.mjs).
//
// Set SCREENSHOTS to a directory to keep pictures of the form.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { launch, skip, stage } from "../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../scripts/testserver.mjs";

let server, browser, page;

const today = new Date().toLocaleDateString("en-CA"); // YYYY-MM-DD, here

before(async () => {
  if (skip()) return;

  server = await startServer("write");

  stage("starting Chrome");
  browser = await launch();
  page = await browser.page();
  await page.setCookie(sessionCookie(server.base, server.cookieName, server.cookie));
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
});

const fill = async (id, text) => {
  await page.evaluate(`(() => { const e = document.getElementById(${JSON.stringify(id)}); e.focus(); e.select?.(); })()`);
  await page.type(text);
};

const shot = async (name) => {
  if (process.env.SCREENSHOTS) writeFileSync(join(process.env.SCREENSHOTS, name), await page.screenshot());
};

test("the preview, the words and the button follow the typing", { skip: skip() }, async () => {
  await page.goto(`${server.base}/`);
  await page.waitFor(`document.getElementById("write")`);

  await fill("number", "1043");
  await fill("payee", "Payee A Plumbing");
  await fill("amount", "1234.5");
  await fill("memo", "Kitchen drain repair");

  await page.waitFor(`document.getElementById("print").textContent === "Print check 1043 to Payee A Plumbing for $1,234.50"`);
  await page.waitFor(`document.getElementById("words").textContent === "One thousand two hundred thirty-four and 50/100"`);
  await page.waitFor(`document.getElementById("preview").src.startsWith("blob:") && !document.getElementById("preview").classList.contains("stale")`);

  await shot("write.png");

  // Too much to fit: the preview says so, beside the check.
  await fill("payee", "Payee with a very long name ".repeat(8));
  const said = await page.waitFor(`!document.getElementById("preview-problem").hidden && document.getElementById("preview-problem").textContent`);
  assert.match(said, /too long to print/);

  // An amount that isn't one: said under the amount.
  await fill("amount", "12.345");
  await page.waitFor(`document.getElementById("amount").getAttribute("aria-invalid") === "true"`);

  // Chrome logs each refused preview as an error of its own; those two are
  // the ones asked for above, and nothing else may be there.
  const others = page.errors.filter((e) => !/status of 422/.test(e));
  assert.deepEqual(others, [], "the page logged errors");
  page.errors.length = 0;
});

test("a check is written, printed and confirmed, with the script in the page", { skip: skip() }, async () => {
  await page.goto(`${server.base}/`);
  await page.waitFor(`document.getElementById("write")`);

  // A refused preview the test before asked for can still be answered
  // after that test cleared the log, and Chrome logs it then. This page is
  // a new one, which nothing of the last can reach, so the log starts here.
  page.errors.length = 0;

  await fill("number", "1043");
  await page.evaluate(`document.getElementById("date").value = ${JSON.stringify(today)}`);
  await fill("payee", "Payee A Plumbing");
  await fill("amount", "1234.50");
  await fill("memo", "Kitchen drain repair");
  await fill("stub_note", "Invoice 8812");
  await page.waitFor(`document.getElementById("print").textContent.startsWith("Print check 1043")`);

  stage("pressing Print");
  await page.click("#print");

  // A payee never paid is asked about once.
  await page.waitFor(`document.body.textContent.includes("Payee A Plumbing is new. Keep it?")`);
  await page.click(".notice button[type=submit]");

  await page.waitFor(`location.pathname === "/checks/checking-a/1043/printing"`);
  await page.waitFor(`document.getElementById("job-status")?.textContent.includes("has printed it")`);

  const jobs = readdirSync(server.printer).filter((f) => f.endsWith(".json"));
  assert.equal(jobs.length, 1, `the printer has ${jobs}`);
  const job = JSON.parse(readFileSync(join(server.printer, jobs[0]), "utf8"));
  assert.equal(job.Name, "Check 1043");
  assert.equal(job.Source, "tray-1");
  assert.equal(job.Scaling, "none");

  stage("saying yes");
  await page.click("form[action$='/printed'] button");
  await page.waitFor(`document.body.textContent.includes("Check 1043 printed.")`);
  assert.equal(await page.evaluate(`document.getElementById("number").value`), "1044");

  // The same payee again: its last check is offered.
  await fill("payee", "Payee A Plumbing");
  await page.evaluate(`document.getElementById("amount").focus()`); // leaving the payee is its change
  await page.waitFor(`document.getElementById("memo").value === "Kitchen drain repair"`);
  assert.equal(await page.evaluate(`document.getElementById("amount").value`), "1,234.50");
  assert.equal(await page.evaluate(`document.getElementById("stub_note").value`), "Invoice 8812");
  await page.waitFor(`document.getElementById("print").textContent === "Print check 1044 to Payee A Plumbing for $1,234.50"`);

  // The preview is drawn again with what was filled in: wait for the drawing
  // asked for last, then for its picture to be the one shown.
  await page.waitFor(`(() => {
    const img = document.getElementById("preview");
    return img.src.startsWith("blob:") && !img.classList.contains("stale") && img.complete && img.naturalWidth > 0;
  })()`);

  // The picture has ink where the payee goes: more dark pixels than the
  // blank form's stock alone.
  const ink = await page.evaluate(`(() => {
    const img = document.getElementById("preview");
    const c = document.createElement("canvas");
    c.width = img.naturalWidth; c.height = img.naturalHeight;
    const g = c.getContext("2d");
    g.drawImage(img, 0, 0);
    const d = g.getImageData(0, Math.round(c.height * 0.3), Math.round(c.width * 0.6), Math.round(c.height * 0.15)).data;
    let n = 0;
    for (let i = 0; i < d.length; i += 4) if (d[i] < 100) n++;
    return n;
  })()`);
  assert.ok(ink > 200, `the preview's payee line has ${ink} dark pixels: it was not drawn again with the payee`);

  await shot("write-again.png");

  assert.deepEqual(page.errors, [], "the page logged errors");
});
