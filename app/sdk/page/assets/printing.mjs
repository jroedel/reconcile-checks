// The printing page, following the job at the printer without reloading
// itself (docs/05-printing.md, "Did it print?"). Without the script the page
// reloads itself instead; either way it stops after two minutes, and the
// question below it is there from the start.
const status = document.getElementById("job-status");

if (status?.dataset.url) {
  const until = Date.now() + 120_000;
  const printer = status.dataset.printer;

  const look = async () => {
    try {
      const r = await fetch(status.dataset.url, { headers: { Accept: "application/json" } });
      const j = await r.json();

      if (j.error) {
        status.textContent = j.error;
        return; // a printer that can't be asked won't be asked every second
      }

      status.textContent = `${printer}: ${j.text}`;
      status.classList.toggle("ok", j.state === "completed");

      if (j.done) return;
    } catch {
      // the program is gone; the page's own banner says so
      return;
    }

    if (Date.now() < until) setTimeout(look, 1500);
  };

  setTimeout(look, 1500);
}
