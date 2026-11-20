# 06 — Writing a check

The **Write a check** screen ([03](03-the-local-app.md)), field by field, and
what happens after Print.

## The form

| Field | Default | Rules |
|---|---|---|
| **Account** | The only account, or the one used last | One of the accounts this computer prints for (Settings, [08](08-signing-in.md)) |
| **Check no.** | Proposed; see "Numbering" | The person looks at the paper and confirms or corrects |
| **Date** | Today | A date more than a week either side of today, or any date in the future (a post-dated check), is asked about again, not refused |
| **Pay to** | — | Completes from payees already paid; see "Payees" |
| **Amount** | — | `1234.5`, `1,234.50` and `$1234.50` are all `$1,234.50`. More than two decimals, zero, negative, or over the account's ceiling ([04](04-stock-and-layout.md)) is refused. Shown in words underneath as it is typed, so a slipped digit is read before it is printed |
| **Memo** | The payee's last memo | Optional, but asked; the accountants need the *what for* ([07](07-the-image.md)) |
| **Stub note** | The payee's last stub note | Optional. Printed on both stubs: the detail the memo is too short for ([04](04-stock-and-layout.md)) |

A payee or memo too long for its line is refused with how many characters fit
([04](04-stock-and-layout.md)); the live preview has already shown it running
over.

Then one button, which says what it will do:

```
[ Print check 1043 to Payee A for $1,234.50 ]
```

**Nothing prints without that press.** Leaving the page, or closing it,
leaves nothing behind: nothing is written to the register until the check is
printed.

## Numbering

The number on the paper is the truth; the program's number is a proposal.

- **Proposed**: one more than the highest number known, taking the highest
  of what Reconcile reports for the account (its highest number and its last
  few, [09](09-reconcile-api.md)) and what the local register holds. Offline,
  the register alone. The last few numbers are shown beside the field, so a
  gap or a second checkbook is visible rather than hidden in one number.
- **Confirmed by the person**, who looks at the next check in the tray. A
  new box of stock, or checks written by hand from the same book, both break
  the sequence, and that is fine: any number may be typed.
- **Refused** if the register already has it on that account, printed or
  voided, or if Reconcile already has it ([09](09-reconcile-api.md)).

### Two computers, one stack

The rector prints from another computer, on the same accounts, from the same
stack of checks ([01](01-overview.md)). Each computer has its own register,
and neither knows what the other printed until Reconcile tells it. Three
things keep one number from being used twice:

1. **The paper.** There is one stack, so the next check in the tray is the
   next number whichever computer prints it. The confirmation at the tray is
   the guard that always works, online or not.
2. **Reconcile, before printing.** When online, the program asks Reconcile
   about the number before printing: already sent or voided by either
   computer, or already cleared at the bank with another amount, and refuses
   it if so ([09](09-reconcile-api.md)).
3. **Sending at once.** A check is sent the moment it is confirmed printed,
   voids included, so the window in which the other computer can't see it
   is seconds, not days.

**Offline, it prints with a warning** (decided 2026-11-20): *"Reconcile
can't be reached, so this number couldn't be checked against the other
computer's checks. Make sure it is the number on the paper."* Refusing to
print would make the program useless exactly when it should just work.

A shared register (a file on a network share) was considered and rejected:
SQLite over a network share corrupts, and Reconcile is already the place
both computers reach.

### When Reconcile refuses the number after printing

An offline check sent later can still collide. Reconcile answers
([09](09-reconcile-api.md)):

- **Already there**, with the same details: a retry of a send whose answer
  was lost. Marked sent; nothing to do.
- **Number used** by another check: the two computers used one number, or a
  number was mistyped. The check is marked on the Checks screen with both
  checks side by side, and the person **corrects the number** to the one on
  the paper (the stub shows what was printed). The corrected check is sent
  again.
- **Amount differs**: the bank already paid a check with this number, for
  another amount. Almost always a mistyped number. The same correction.

## Payees

Names complete from Reconcile's payees as they are typed, so one plumber
doesn't become three spellings; offline, or before signing in, from the
payees in the local register. The matches are listed under the field.

A payee already in the register brings its last check with it: the amount,
memo and stub note are offered as defaults. A payee paid the same amount for
the same thing every week is a check of a few keystrokes; the date and number
still come fresh.

A payee neither list knows is asked about once: *"Payee B Plumbing is new.
Keep it?"*, which catches a typo before it is printed on paper.

## After it prints

The page follows the job ([05](05-printing.md)) and then asks:

```
Printed on tray 1.  Did it print correctly?
[ Yes ]   [ No — void it and reprint on 1044 ]
```

- **Yes**: the check is final. Its image is sent to Reconcile at once, or
  saved to send later ([07](07-the-image.md)). The form clears for the next
  check, keeping the account and proposing the next number.
- **No**: void and reprint, below.

## Void and reprint

A jam or a misaligned print is voided, and the same check goes out on the
next number.

1. Check 1043 is marked void in the register, with a reason ("jam",
   "misaligned", or anything typed).
2. It is sent to Reconcile and then voided there, so the number is accounted
   for on both computers and never waits as outstanding
   ([09](09-reconcile-api.md)).
3. The form is filled again with the same payee, amount, memo and stub note
   on 1044, and the person confirms 1044 is the next check in the tray.
4. **The paper gets "VOID" written across it and is kept.**

## Voiding a check later

From the Checks screen: a check lost in the mail, written in error, or never
cashed. It is marked void in the register and in Reconcile, where it leaves
the checks outstanding. Reconcile refuses to void a check that has already
cleared, and the page says so. Stopping payment is between the person and
the bank; the program only records it.

A check is never deleted from the register, and its number is never reused.

## The register

One row per check printed, in SQLite ([02](02-architecture.md)):

| Column | |
|---|---|
| account | Reconcile's id for the account |
| number | Unique per account |
| written on | The date on the check |
| payee, amount (cents), memo, stub note | As printed |
| printed at | Unix milliseconds |
| voided at, void reason | Empty unless voided |
| reprint of | The number this check replaced, after a void |
| sent at, void sent at | Empty until Reconcile has it |
| Reconcile's answer | The last refusal, if any, shown on the Checks screen |

The register is what refuses a number twice on this computer, what is sent
from, and what the Checks screen shows. It is not the books: Reconcile is.

