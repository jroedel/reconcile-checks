// Signing in to Reconcile in a real Chrome against the real program: `make
// test-browser`.
//
// What only a browser can say: that the page's session cookie, which is
// SameSite=Strict, is left behind when Reconcile sends the browser back to
// /callback -- and is sent again when the callback's own page moves on to
// keep the key. The Go tests follow the same steps, but they decide which
// request carries the cookie; here Chrome does.
//
// The Reconcile is a few lines of Node below: it agrees to every sign-in at
// once, as reconciletest does for the Go tests. Never the real one
// (CLAUDE.md, section 6).
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";

import { launch, skip, stage } from "../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../scripts/testserver.mjs";

let books, server, browser, page;
const requests = [];

// A Reconcile: its metadata, a sign-in page that agrees at once, the token
// endpoint, and one invented account.
function reconcile() {
  let base;
  const codes = new Map();

  const srv = createServer((req, res) => {
    const u = new URL(req.url, base);
    requests.push(`${req.method} ${u.pathname}`);

    const json = (status, v) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(v));
    };

    if (u.pathname === "/.well-known/oauth-authorization-server") {
      return json(200, {
        issuer: base,
        authorization_endpoint: `${base}/oauth/authorize`,
        token_endpoint: `${base}/oauth/token`,
        code_challenge_methods_supported: ["S256"],
      });
    }

    if (u.pathname === "/oauth/authorize") {
      const code = `code-${codes.size + 1}`;
      codes.set(code, u.searchParams.get("redirect_uri"));
      const back = new URL(u.searchParams.get("redirect_uri"));
      back.search = new URLSearchParams({ code, state: u.searchParams.get("state"), iss: base }).toString();
      res.writeHead(303, { Location: back.toString() });
      return res.end();
    }

    if (u.pathname === "/oauth/token" && req.method === "POST") {
      let body = "";
      req.on("data", (d) => (body += d));
      req.on("end", () => {
        const f = new URLSearchParams(body);
        if (!codes.has(f.get("code")) || codes.get(f.get("code")) !== f.get("redirect_uri") || !f.get("code_verifier")) {
          return json(400, { error: "invalid_grant" });
        }
        codes.delete(f.get("code"));
        json(200, { access_token: "rcn_browser_test", token_type: "Bearer", expires_in: 31536000, scope: "checks" });
      });
      return;
    }

    if (u.pathname === "/api/v1/checks/accounts") {
      if (req.headers.authorization !== "Bearer rcn_browser_test") return json(401, { error: { field: "", problem: "no" } });
      return json(200, { accounts: [{ id: "acc1", name: "Checking A", last4: "1234", currency: "USD", organization: "", highest: "", last_numbers: [] }] });
    }

    json(404, { error: { field: "", problem: "not here" } });
  });

  return new Promise((done) =>
    srv.listen(0, "127.0.0.1", () => {
      base = `http://127.0.0.1:${srv.address().port}`;
      done({ base, close: () => srv.close() });
    }),
  );
}

before(async () => {
  if (skip()) return;

  books = await reconcile();
  server = await startServer("signin", { settings: `reconcile = ${JSON.stringify(books.base)}` });

  stage("starting Chrome");
  browser = await launch();
  page = await browser.page();
  await page.setCookie(sessionCookie(server.base, server.cookieName, server.cookie));
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
  books?.close();
});

test("signing in comes back to Settings, signed in", { skip: skip() }, async () => {
  await page.goto(`${server.base}/settings`);
  await page.waitFor(`document.querySelector('a[href="/settings/reconcile/sign-in"]') !== null`);

  await page.click('a[href="/settings/reconcile/sign-in"]');

  // To Reconcile, back to /callback, on to keep the key, and to Settings.
  await page.waitFor(`location.pathname === "/settings" && location.search === "?saved=signed-in"`, 20_000);

  const text = await page.evaluate(`document.querySelector("main").textContent`);
  assert.match(text, /Signed in to Reconcile\./);
  assert.match(text, /Print checks on it here|in Reconcile as Checking A/);

  assert.ok(requests.includes("POST /oauth/token"), `the program never traded the code: ${requests}`);
  assert.deepEqual(page.errors, [], "the page logged errors");
});
