// The Write a check form's pure parts, apart from the page so that Node can
// test them alone (form_test.mjs). Nothing here knows the money rules: the
// amount's figures and words come from the server, from the code that prints
// (app/domain/writeapp/live.go).

// The fields the preview draws, by the name its address gives them.
const drawn = [
  ["account", "account"],
  ["number", "number"],
  ["date", "date"],
  ["payee", "payee"],
  ["amount", "amount"],
  ["memo", "memo"],
  ["stub_note", "note"],
];

// previewQuery is the preview's query string for the form's fields: only
// those with something in them, as the server's own page builds it.
export function previewQuery(fields) {
  const q = new URLSearchParams();
  for (const [field, param] of drawn) {
    const v = fields[field] ?? "";
    if (v !== "") q.set(param, v);
  }
  return q.toString();
}

// summary is what the Print button says: the number, the payee and the
// amount as it will print, or plainly "Print check" until all three are
// there.
export function summary(fields, figures) {
  const number = (fields.number ?? "").trim();
  const payee = (fields.payee ?? "").trim();

  if (!/^\d+$/.test(number) || payee === "" || !figures) {
    return "Print check";
  }

  return `Print check ${Number(number)} to ${payee} for ${figures}`;
}

// fillEmpty is the payee's last check, offered for this one: the amount, memo
// and stub note it had, for each field still empty. What the person has
// already typed is never replaced.
export function fillEmpty(fields, last) {
  const changes = {};
  if (!last?.found) return changes;

  for (const [field, value] of [["amount", last.amount], ["memo", last.memo], ["stub_note", last.stub_note]]) {
    if ((fields[field] ?? "").trim() === "" && value) changes[field] = value;
  }

  return changes;
}

// debounce calls fn once things have been quiet for ms: one preview per
// pause in typing, not one per key.
export function debounce(fn, ms) {
  let timer;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), ms);
  };
}
