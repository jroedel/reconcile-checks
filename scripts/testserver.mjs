// The program, for a browser test: built, started on a free port with a
// config of its own in a temporary folder -- a printer that is a folder of
// files, and an invented account on it -- and signed into through /open with
// the one-time address it prints, as the browser it would open is.
//
// Never the real config, register or printer (CLAUDE.md, section 6): the
// config names its own data folder, and its one printer is file:.
import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { stage } from "./browser.mjs";

const repo = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const freePort = () =>
  new Promise((done) => {
    const s = createServer();
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => done(port));
    });
  });

// startServer gives back the program's base address, the session cookie's
// name and value, the printer's folder, log() for everything it has written,
// and stop().
export async function startServer(name) {
  const dir = mkdtempSync(join(tmpdir(), `reconcile-checks-${name}-`));
  const printer = join(dir, "printer");
  mkdirSync(printer);

  let said = "";
  let app;

  const stop = () => {
    app?.kill();
    rmSync(dir, { recursive: true, force: true });
  };

  try {
    stage("building the program");
    const bin = join(dir, process.platform === "win32" ? "reconcile-checks.exe" : "reconcile-checks");
    execFileSync("go", ["build", "-o", bin, "./cmd/reconcile-checks"], { cwd: repo, stdio: "inherit", timeout: 180_000 });

    const port = await freePort();
    const base = `http://127.0.0.1:${port}`;

    // An invented account (CLAUDE.md, "This repository is public").
    writeFileSync(
      join(dir, "config.toml"),
      `port = ${port}
close_after = "10m"

[[printer]]
name = "Test printer"
url = ${JSON.stringify("file:" + printer.replaceAll("\\", "/"))}

[[account]]
key = "checking-a"
name = "Checking A"
last4 = "1234"
stock = "walmart-voucher-top"
printer = "Test printer"
tray = "tray-1"
media_type = "stationery-preprinted"
feed = "face down, top edge in first"
`,
    );

    stage("starting it");
    app = spawn(bin, ["--config", join(dir, "config.toml"), "--no-browser"], { stdio: ["ignore", "pipe", "pipe"] });
    app.stdout.on("data", (d) => (said += d));
    app.stderr.on("data", (d) => (said += d));

    let open;
    for (let i = 0; !open; i++) {
      open = /http:\/\/127\.0\.0\.1:\d+\/open\?k=\S+/.exec(said)?.[0];
      if (i > 200) throw new Error(`the program printed no address to open in 20 s:\n${said}`);
      if (!open) await new Promise((done) => setTimeout(done, 100));
    }

    const r = await fetch(open, { redirect: "manual", signal: AbortSignal.timeout(10_000) });
    const set = r.headers.get("set-cookie") ?? "";
    const m = /^([^=;]+)=([^;]+)/.exec(set);
    if (r.status !== 303 || !m) throw new Error(`/open answered ${r.status} with no cookie:\n${said}`);

    stage("signed in");
    return { base, cookieName: m[1], cookie: m[2], printer, log: () => said, stop };
  } catch (err) {
    stop();
    throw new Error(`${err.message}\n${said}`);
  }
}

// sessionCookie is the cookie as Network.setCookie takes it.
export function sessionCookie(base, name, value) {
  return { name, value, url: base, httpOnly: true, sameSite: "Strict" };
}
