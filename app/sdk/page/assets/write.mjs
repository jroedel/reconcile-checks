// The Write a check page, live (docs/03-the-local-app.md, "The screens"): the
// preview drawn again a moment after each change, the amount in words under
// the amount, the Print button saying what it will print, a payee's last
// check offered when a payee already paid is chosen, and the number proposed
// again when the account changes.
//
// The form works without any of this; the script only keeps the page up to
// date while it is being filled in. Everything it shows, it asks the server
// for, so nothing here can disagree with the paper.
import { debounce, fillEmpty, previewQuery, summary } from "./form.mjs";

const form = document.getElementById("write");

if (form) {
  const $ = (id) => document.getElementById(id);
  const img = $("preview");
  const whole = $("preview-whole");
  const problem = $("preview-problem");
  const words = $("words");
  const button = form.querySelector("button[type=submit]");
  const amount = form.elements.amount;
  const number = form.elements.number;
  const asking = button && !button.id; // the questions' "Yes" button: left as the server wrote it

  let figures = "";
  let shown = null; // the preview's object URL, to let go of when replaced
  let drawing = 0;

  const fields = () => Object.fromEntries([...new FormData(form)].filter(([k]) => k !== "form_token" && k !== "answered"));

  const ask = async (path) => {
    const r = await fetch(path, { headers: { Accept: "application/json" } });
    if (!r.ok) throw new Error(`${path}: ${r.status}`);
    return r.json();
  };

  async function redraw() {
    const q = previewQuery(fields());
    const mine = ++drawing;

    img?.classList.add("stale");
    if (whole) whole.href = `/preview.png?${q}&whole=1`;

    const r = await fetch(`/preview.png?${q}`);
    if (mine !== drawing) return; // a later change is already being drawn

    if (r.ok) {
      const next = URL.createObjectURL(await r.blob());
      if (img) img.src = next;
      if (shown) URL.revokeObjectURL(shown);
      shown = next;
      img?.classList.remove("stale");
      if (problem) problem.hidden = true;
    } else if (problem) {
      problem.textContent = await r.text();
      problem.hidden = false;
    }
  }

  async function sayAmount() {
    const j = await ask(`/write/amount?amount=${encodeURIComponent(amount.value)}`);

    figures = j.figures;
    if (words) words.textContent = j.error || j.words;
    if (j.error) amount.setAttribute("aria-invalid", "true");
    else amount.removeAttribute("aria-invalid");

    label();
  }

  function label() {
    if (button && !asking) button.textContent = summary(fields(), figures);
  }

  async function offerLast() {
    const j = await ask(`/write/defaults?payee=${encodeURIComponent(form.elements.payee.value)}`);
    const changes = fillEmpty(fields(), j);

    for (const [name, value] of Object.entries(changes)) {
      form.elements[name].value = value;
    }

    if (Object.keys(changes).length) update();
  }

  async function propose() {
    const j = await ask(`/write/proposal?account=${encodeURIComponent(form.elements.account.value)}`);

    if (!number.dataset.typed) number.value = j.next ? String(j.next) : "";

    const last = $("last");
    if (last) last.textContent = j.last.length ? `The last on this account: ${j.last.join(", ")}. ` : "";

    update();
  }

  const update = debounce(() => {
    label();
    sayAmount().catch(() => {});
    redraw().catch(() => {});
  }, 250);

  form.addEventListener("input", (e) => {
    if (e.target === number) number.dataset.typed = "1";
    update();
  });

  // A payee chosen from the list, or typed and left: offer its last check.
  form.elements.payee?.addEventListener("change", () => offerLast().catch(() => {}));
  form.elements.account?.addEventListener?.("change", () => propose().catch(() => {}));

  // Once at the start, so the button and the words are the script's from
  // the first key.
  sayAmount().catch(() => {});
}
