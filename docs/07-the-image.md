# 07 — The image

## What it is

A picture of the **check face as printed**, about 200 dpi PNG: not a photo of
the signed check, since the bank's image comes with the statement.

It is drawn from the same layout and the same placed text as the PDF that
went to the printer ([02](02-architecture.md), "Rendering"), by the PNG
renderer that also draws the live preview, with three differences:

- **The face only**, without the stubs. The stub note stays on the paper:
  the memo is what the accountants see, and Reconcile has no field for the
  note. If they come to want it, it can be added to the memo or to
  Reconcile ([09](09-reconcile-api.md)).
- **No calibration offset.** The offset corrects this printer; the image
  should show where the boxes are, not where this printer puts them.
- **The stock is drawn in**: the lines, the box outlines and their labels,
  the pre-printed check number, and the account as `XXXXXX1234`. What the
  program prints alone would be words floating on white; drawn into the
  stock's shape, it reads as a check. The bank's name and the account holder
  are left off, so the config holds only what printing needs; the MICR line
  is never drawn beyond the masked account.

The signature line is left empty. A voided check's image has "VOID" across
it.

So the preview, the paper and the image can't disagree about what was written where.

## Sending it

With the image go the check's account (by id), number, payee, amount, date
written and memo ([09](09-reconcile-api.md)). In Reconcile it waits among the
account's checks outstanding, and is attached when the statement listing
that number is imported, if the amounts agree. If the transaction is already
there (a check cashed before its image was sent), it is attached at once.

The image is sent right after "Did it print correctly? Y". If that fails
(offline, signed out, or Reconcile down), the
image is saved and the register marks it unsent:

```
  Saved; not sent to Reconcile (offline). It will be sent when Reconcile can be reached.
```

The program sends whatever is waiting, oldest first, each time it starts
and whenever Reconcile can be reached again; **Send now** on the Checks
screen does it at once, and each check there says whether it was sent and
what Reconcile answered ([09](09-reconcile-api.md)). Sending twice must not make two checks in Reconcile; see
[09](09-reconcile-api.md), "Sending again".

As built:

- **Yes** sends the check at once, waiting up to twenty seconds, and the
  form says what became of it: in Reconcile, outstanding; attached, the
  bank having paid it already; or kept here, and why.
- What is waiting is sent when the program starts and every minute after,
  and at once after a void or a corrected number. A try that fails for
  want of Reconcile waits a minute, then two, four, and so on to an hour.
  Signed out, nothing is tried and nothing counted until someone signs in.
- A refusal (another check has the number, the bank paid another amount,
  the account archived, a role that can't add checks) waits for the person:
  the check's page shows Reconcile's sentence and what it has instead,
  side by side. Correcting the number sends it afresh; **Send now** tries
  every waiting check, refused ones included.
- A check voided after it was sent keeps the image it was sent with, and
  Reconcile marks it void; one voided before it was sent (a jam) goes with
  "VOID" across it.

The PDF and the PNG are kept locally after sending
([02](02-architecture.md)).

## What the accountants see

In Reconcile, once the statement is imported:

- On the charge "CHECK 1043 −$1,234.50": the image, paid to Payee A, memo
  "Kitchen drain repair", written on the 20th though it cleared on the 3rd.
  The memo and the day written are what an accountant preparing 1099s needs
  and what the bank never says (Reconcile's `docs/phone.md`, 7).
- On the account's month page: checks written but not yet cashed, with their
  total (`receiptbus.OutstandingChecks`, already built).

None of this is built here: it is Reconcile showing a check image it already
knows how to show. What this program adds is that the image, memo and day
written arrive before the check clears, typed once.

