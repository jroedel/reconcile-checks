// The updating page, following Update now through the check, the install and
// the restart (docs/10-install-update-and-releases.md, "Updating itself").
// While the program restarts it is away for a moment; the page asks /healthz
// until the new version answers, then goes back to Settings, where the same
// session cookie still holds. Without the script the page says to look again.
//
// The words shown are the server's (/settings/update/status), as on every
// page here: the script decides only when to ask, and what to ask next.

// next is what to ask after a status: the status again while checking or
// waiting for printing to finish; /healthz while restarting, or when the
// program didn't answer at all (it is between copies); nothing once idle.
export function next(status) {
  if (status === null) return { ask: "health" };

  switch (status.state) {
    case "checking":
    case "waiting":
      return { ask: "status" };
    case "restarting":
      return { ask: "health" };
    default:
      return { ask: "nothing" };
  }
}

// cameBack reports whether /healthz's answer is the version restarted into,
// or any healthy answer when which version wasn't heard.
export function cameBack(text, version) {
  const t = text.trim();
  return version ? t === `ok ${version}` : t.startsWith("ok ");
}

const box = typeof document === "undefined" ? null : document.getElementById("update-words");

if (box && box.dataset.state && box.dataset.state !== "idle") {
  let version = box.dataset.installed;
  const until = Date.now() + 5 * 60_000;

  const health = async () => {
    try {
      const r = await fetch("/healthz", { cache: "no-store" });
      if (r.ok && cameBack(await r.text(), version)) {
        location.href = "/settings" + (version ? "?updated=" + encodeURIComponent(version) : "");
        return;
      }
    } catch {
      // still between copies
    }

    if (Date.now() < until) setTimeout(health, 1000);
  };

  const status = async () => {
    let s = null;

    try {
      const r = await fetch("/settings/update/status", { headers: { Accept: "application/json" }, cache: "no-store" });
      if (r.ok) s = await r.json();
    } catch {
      // the program is away: restarting
    }

    if (s?.words) box.textContent = s.words;
    if (s?.installed) version = s.installed;

    const n = next(s);
    if (n.ask === "nothing" || Date.now() > until) return;

    setTimeout(n.ask === "health" ? health : status, 1000);
  };

  setTimeout(status, 1000);
}
