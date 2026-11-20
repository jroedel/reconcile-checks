# 09 — The Reconcile API

**The contract lives in Reconcile**: its `docs/checks.md`, and the routes in
`app/domain/apiapp/checks.go` and the rules in
`business/domain/receipt/receiptbus/sent.go` that it describes. This
document is the client's side: which call each part of this program uses,
and what it does with each answer. When the two disagree, Reconcile is right
and this document is corrected.

**Checked against what was built** (reconcile#167–#171, merged 2026-11-20,
read at `e01fa85`): every route, field, status and code below is as the code
answers it, not as #166 planned it. Where the plan and the build differed,
the build is written here and the difference is listed at the end.

## The calls

Every call carries the key from [08](08-signing-in.md):
`Authorization: Bearer rcn_…`. An account is always named by Reconcile's id,
never by an account number. Check numbers are **strings of digits** both
ways, at most 10, with no leading zeros (Reconcile strips them). Amounts are
**unsigned decimal strings**, `"1234.50"`, both ways.

| Call | Answer | Used for |
|---|---|---|
| `GET /api/v1/checks/accounts` | `{accounts: [{id, name, last4, currency, organization, highest, last_numbers}]}` | Settings' list of accounts; proposing the next number and showing the last few ([06](06-writing-a-check.md)) |
| `GET /api/v1/accounts/{account}/payees?q=` | `{payees: [{name}]}`, at most 20 | Payee completion as it is typed |
| `GET /api/v1/accounts/{account}/checks/{number}` | Always `200 {number, state, check?, cleared?}` | Asking about the number before printing (below) |
| `POST /api/v1/accounts/{account}/checks?number=&amount=&payee=&written_on=&memo=&name=`, the PNG as the body | See "What the program does with each answer to a send" | Sending a check ([07](07-the-image.md)) |
| `POST /api/v1/accounts/{account}/checks/{number}/void` | `200 {check}` | Void and reprint; voiding later |
| `GET /api/v1/accounts/{account}/checks?state=outstanding\|all` | `{checks: [check]}`, newest written first | The Checks screen's view of what Reconcile holds |

- **`accounts`**: the checking accounts in use (not a card, not kept for
  reference, not archived) where the person may attach receipts.
  `organization` is its name, `""` for an account on its own. `highest` is
  `""` when the account has used no number; `last_numbers` is the five
  highest, highest first, as strings. Both come from the account's check
  images, void ones too, **and from the bank's statements**, so a check
  written by hand from the same book counts.
- **`payees`**: the names whose name or any spelling contains `q`, archived
  ones left out. Empty, not refused, when the person's role is on the
  account and not on its organization.
- **`send`**: every part is needed but `memo`. `payee` and `memo` are at
  most **100 characters** each; `name` is the file's name, such as
  `check-1043.png`; `written_on` is `YYYY-MM-DD`. The image is a PNG (or
  JPEG, WebP, HEIC or PDF), judged by its content, **at most 20 MB**.
- **`list`**: only the checks **this person** sent (the key's user), so the
  rector's checks are not in the treasurer's list, and the other way round.
  Asking about one number sees everybody's.

A `check` is `{id, number, amount, payee, written_on, memo, state:
"outstanding" | "attached" | "void", attached_to: [{id, url}], url}`, where
`attached_to` is a list (a check is usually on one transaction) and `url` is
the check's page, shown on the Checks screen as "open in Reconcile".

A refusal is `{"error": {"field", "problem"}}`, where `problem` is a
sentence the page shows as Reconcile wrote it. A refusal the program must
tell apart also carries **`code`** beside `error`, and what it needs to act
on: `number_used`, `amount_differs`, `cleared`, `not_checking`, `no_check`.

**A `404` is never a free number.** On every route it answers both "no such
account" and "an account this key does not reach", so that a stranger learns
nothing. The program reads a 404 without `code` as **the account is gone
from this key**: Settings says so, and nothing is printed on that account
until it is chosen again.

## Asking about the number before printing

When online, before Print, the program asks `GET …/checks/{number}`
([06](06-writing-a-check.md), "Two computers, one stack"):

| `state` | Meaning | The program |
|---|---|---|
| `free` | No check here has it, and the bank hasn't paid it | Prints |
| `outstanding`, `attached` | A check has it (`check`), sent from either computer | Refuses, showing that check: payee, amount, day |
| `void` | A spoiled check has it (`check`) | Refuses: the paper with that number was voided, so it isn't the paper in the tray |
| `cleared` | The bank paid the number and no check here has it: `cleared: [{amount, date}]`, usually one | Refuses, showing what the bank paid and when: a check written by hand from the same book |

Reconcile says a voided number is "free to print again"; the program doesn't,
because the number is pre-printed and that sheet is gone. Offline, or on any
answer but `200`, it prints with the warning in [06](06-writing-a-check.md).

## What the program does with each answer to a send

| Answer | Meaning | The program |
|---|---|---|
| `201 {outcome: "outstanding", check}` | Waiting for the statement | Marks it sent; keeps `check.id` and `check.url` |
| `201 {outcome: "attached", check}` | The bank already paid it; the image is on the charge | Marks it sent; keeps `id` and `url` |
| `200 {outcome: "already_there", check}` | Same number, amount, payee (but for case and spaces) and day: a retry | Marks it sent. This is what makes retrying safe |
| `409`, `code: "number_used"`, `check` | Another check has this number | Shows both, asks for the number on the paper ([06](06-writing-a-check.md)) |
| `409`, `code: "amount_differs"`, `bank`, `sent` | The bank paid this number for another amount | Shows both amounts, asks for the number on the paper |
| `422`, `code: "not_checking"` | The account is no longer a checking account in use (archived, say) | Keeps it unsent, says so in Settings |
| `422` (no code), `400` | A field Reconcile won't take | Keeps it unsent with Reconcile's sentence. A bug here, since the form refuses the same things first |
| `403` | The person's role on the account doesn't let them add checks | Keeps it unsent with Reconcile's sentence: someone who keeps the books must give them a role that does |
| `413`, `415` | The image is too large, or not an image | Keeps it unsent with Reconcile's sentence. A bug here |
| `401` | The key ended or was revoked | Keeps it to send, asks to sign in ([08](08-signing-in.md)) |
| `404` (no code) | The key doesn't reach the account | Keeps it unsent; Settings says the account is gone from this key |
| No answer, or `5xx` | Offline, or Reconcile is down | Keeps it to send, retries later |

Nothing is kept by Reconcile on any refusal: the account and number are
checked before the image is stored.

### Sending again

A send whose answer was lost is sent again, and Reconcile answers
`already_there` with the check it made the first time: it recognises a check
by what it says (number, amount, payee and day), not by the file, since the
image drawn again is new bytes. The memo is not compared.

## Voiding

A voided check is **sent, then voided**: two calls, in that order, kept in
that order when offline. The moment between them in which the check is
outstanding is harmless: either way the number shows as used.

| Answer | The program |
|---|---|
| `200 {check}` | Marks the void sent. Voiding one already void answers it as it is, so a retry is safe |
| `409`, `code: "cleared"` | The bank paid it: it can't be voided. The page says so, and the register keeps it void with Reconcile's sentence beside it, for the person to take up with the bank |
| `404`, `code: "no_check"` | No check has the number: the send never arrived. Sends it, then voids again |
| `401`, no answer, `5xx` | Keeps it to void, in order |

**A void is only sent for a check Reconcile took from this computer.**
Reconcile voids by number: a void sent after a refused send ("another check
has this number") would void the other computer's check. So the queue sends
the check first, and sends its void only once the check's send was taken
(outstanding, attached, or already there).

**Once a check's void is sent, it is never sent again.** Reconcile skips
voided checks when it looks for a repeat, so the same check sent after its
void would be a new check outstanding. The queue only ever sends a check
whose void has not been sent.

## The stub note, and currency

- **The stub note is not sent** ([07](07-the-image.md)): there is no field
  for it. If the accountants come to want it (an invoice number), it can go
  in the memo, or Reconcile can gain a field.
- **Currency.** The amount in words is in dollars. An account whose
  `currency` isn't `USD` is not offered for printing.

## Where the build differs from the plan

What #166 planned and this document said before Reconcile was built, and
what was built instead (2026-11-20):

1. **Asking about one number** was this program's request on the issue
   ([comment](https://github.com/jroedel/reconcile/issues/166#issuecomment-7032655506)).
   Built as `GET …/checks/{number}`, with `cleared` for a number the bank
   paid with no image. The program asks it instead of listing every check.
2. **Refusals** were planned as `{field, message}` with `problem` as the
   code. Built as `{"error": {"field", "problem"}}`, where `problem` is the
   sentence, with `code` beside `error`.
3. **`attached_to`** is a list of `{id, url}`, not one value.
4. **Numbers and amounts** are strings, and `highest` is `""` for none.
5. **The list of checks** is only the person's own.
6. **New answers**: `403` for a role that can't add checks, `422
   not_checking`, `413` and `415` for the image, `409 cleared` and `404
   no_check` for a void.
7. **Limits**: payee and memo at most 100 characters, the number at most 10
   digits, the image at most 20 MB. The form refuses more before printing
   ([06](06-writing-a-check.md)).
