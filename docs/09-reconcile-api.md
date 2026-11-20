# 09 — The Reconcile API

**The contract lives in Reconcile**, in
[reconcile#166](https://github.com/jroedel/reconcile/issues/166) and, once
built, in Reconcile's `docs/books-api.md`. This document is the client's
side: which call each part of this program uses, and what it does with each
answer. When the two disagree, Reconcile is right and this document is
corrected.

**Planned, not built** (2026-11-20). This follows the contract in #166's
review comment of that day. When Reconcile builds it, read what was built and
correct this document from it: routes, fields and error sentences as they
are.

## The calls

Every call carries the key from [08](08-signing-in.md):
`Authorization: Bearer …`. An account is always named by Reconcile's id,
never by an account number. A refusal is `{field, message}`, and the page
shows `message` as Reconcile wrote it.

| Call | Used for |
|---|---|
| `GET /api/v1/checks/accounts` → `{accounts: [{id, name, last4, currency, organization, highest, last_numbers}]}` | Settings' list of accounts; proposing the next number and showing the last few ([06](06-writing-a-check.md)) |
| `GET /api/v1/accounts/{account}/payees?q=` → `{payees: [{name}]}`, at most 20 | Payee completion as it is typed |
| `POST /api/v1/accounts/{account}/checks?number=&amount=&payee=&written_on=&memo=&name=`, body the PNG | Sending a check ([07](07-the-image.md)) |
| `GET /api/v1/accounts/{account}/checks?state=outstanding\|all` → `{checks: [check]}` | The Checks screen's view of what Reconcile holds; finding a number before printing (below) |
| `POST /api/v1/accounts/{account}/checks/{number}/void` | Void and reprint; voiding later |

`check` is `{id, number, amount, payee, written_on, memo, state:
"outstanding" | "attached" | "void", attached_to, url}`. The `url` is shown on
the Checks screen as "open in Reconcile".

## What the program does with each answer to a send

| Answer | Meaning | The program |
|---|---|---|
| `201 {outcome: "outstanding"}` | Waiting for the statement | Marks it sent |
| `201 {outcome: "attached"}` | The bank already paid it; the image is on the charge | Marks it sent |
| `200 {outcome: "already_there"}` | Same number, amount, payee and date: a retry | Marks it sent. This is what makes retrying safe |
| `409 {problem: "number_used", check}` | Another check has this number | Shows both, asks for the number on the paper ([06](06-writing-a-check.md)) |
| `409 {problem: "amount_differs", bank, sent}` | The bank paid this number for another amount | Shows both amounts, asks for the number on the paper |
| `401` | The key ended or was revoked | Keeps it to send, asks to sign in ([08](08-signing-in.md)) |
| No answer, or `5xx` | Offline, or Reconcile is down | Keeps it to send, retries later |

A voided check is **sent, then voided**: two calls, in that order, kept in
that order when offline. The moment between them in which the check is
outstanding is harmless: either way the number shows as used.

## Things to check when #166 is built

The first was asked for on the issue on 2026-11-20
([comment](https://github.com/jroedel/reconcile/issues/166#issuecomment-7032655506)),
and the fourth confirmed there. The second and third are this program's own
notes.

1. **Asking about one number before printing.** Two computers print from
   one stack ([06](06-writing-a-check.md)). `GET …/checks?state=all` lists
   the check images, but a number the bank has already paid with no image (a
   check written by hand) is not among them, and is exactly the
   `amount_differs` case. Asked on the issue: one call for one number, giving
   the check if there is one, or the bank's cleared transaction and its
   amount.
2. **The stub note**: not sent ([07](07-the-image.md)). If the accountants
   come to want it (an invoice number), it can go in the memo, or Reconcile
   can gain a field.
3. **Payee search per keystroke** crosses to Reconcile's server each time,
   so the page waits for a pause in typing and shows local payees first.
4. **Which currency.** The amount in words is in dollars. An account whose
   `currency` isn't USD is not offered for printing.
