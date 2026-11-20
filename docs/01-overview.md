# 01 — Overview

## What it is for

Write a check in one short sitting, with no Claude needed: type the payee,
amount and memo, and the check prints on pre-printed check stock. The image of
the check as printed goes to [Reconcile](https://github.com/jroedel/reconcile)
under its number before the bank has even seen it. When the statement that
pays it is imported, Reconcile attaches the image to the charge, so the
bookkeeper and the accountants see who, what and why beside the money leaving.

Today the same result takes a photograph of each check after it is signed, or
Claude reading the bank's check images after they clear (`read_check`,
Reconcile's `docs/phone.md`). Both happen after the fact, and the memo and the
day written are typed or read a second time. Here they are typed once, when
the check is written, and the paper and the books are made from the same
keystrokes.

## Who uses it

- **The person who writes the checks** for an organization in Reconcile: a
  treasurer or bookkeeper, at the computer the check printer is attached to.
  They must be able to attach receipts on the account the check is drawn on;
  the program can do nothing their role in Reconcile can't.
- **The accountants and the bookkeeper**, who never run it, but see its work
  in Reconcile ([07](07-the-image.md)).

Today one person prints the checks, from Linux. The rector of the house will
print too, **from Windows**, from the first version, on the same accounts and from **the same stack
of checks** (decided 2026-11-20). So:

- it is a **local web app**: it opens in the browser, which anyone can use
  ([03](03-the-local-app.md));
- it prints over **IPP**, the same on both systems ([05](05-printing.md));
- two computers number one checkbook ([06](06-writing-a-check.md), "Two
  computers, one stack");
- it is **one file** from GitHub Releases that **updates itself**
  ([10](10-install-update-and-releases.md)).

## One check, end to end

Double-click Reconcile Checks. The browser opens at **Write a check**:

```
┌───────────────────────────────────────┬──────────────────────────────────────────┐
│ Account    Checking A ····1234      ▾ │  ┌────────────────────────────────────┐  │
│ Check no.  1043   (last: 1040 1041    │  │                   Nov 20, 2026     │  │
│                    1042)              │  │ Payee A               **1,234.50   │  │
│ Date       2026-11-20                 │  │ One thousand two hundred thirty-   │  │
│ Pay to     Payee A                    │  │ four and 50/100 ****************** │  │
│ Amount     1234.50                    │  │ Kitchen drain repair               │  │
│            One thousand two hundred   │  └────────────────────────────────────┘  │
│            thirty-four and 50/100     │        the live preview, on its stock    │
│ Memo       Kitchen drain repair       │                                          │
│ Stub note  Invoice 8812               │                                          │
│                                       │                                          │
│ [ Print check 1043 to Payee A for $1,234.50 ]   Tray 1 · face down, top first    │
└───────────────────────────────────────┴──────────────────────────────────────────┘
```

Press Print. The page follows the printer, then asks *"Did it print
correctly?"*; **Yes**, and the check goes to Reconcile: *"Sent: check 1043
waiting on Checking A, outstanding."*

Then sign it, tear it off and mail it. Each step is in
[06](06-writing-a-check.md).

## The test the first version must pass

A check written on this program on the 20th, for an amount and to a payee
typed once, prints squarely in its boxes on the real stock; and when the
statement bringing "CHECK 1043 −$1,234.50" is imported on the 3rd, the charge
in Reconcile shows the image, the payee, the memo and "written on the 20th"
without anybody touching it.

## In and out of the first version

**In:** the local web app with its live preview; writing one check at a
time; the stock's layout and calibration per account; printing over IPP to
any IPP Everywhere printer; the local register; void and reprint; payee
completion; the image sent to Reconcile, or saved and sent later; signing in
through Reconcile; installing from one file, and updating itself.

**Out** (see [11](11-later.md)): blank stock and MICR printing; printed
signatures; a mailing address for window envelopes; a Positive Pay file for
the bank; printing a batch of checks at once; a link to the download from
Reconcile's own pages.

## Order of work

1. The open questions in these documents, starting with the stock
   ([04](04-stock-and-layout.md)).
2. The release pipeline and self-update, with its tests
   ([10](10-install-update-and-releases.md)), on a program that does
   nothing yet but serve a page. Updating must work before there is
   anything worth updating, because the first release without it is the
   last one a computer receives by itself.
3. The layout and calibration on plain paper, then one real check, printed
   from a development build by a person. *Needs the stock in hand.*
4. The program: the form and preview, amount in words, the register, void
   and reprint, printing, saving the image.
5. reconcile#166 in Reconcile: the `checks` scope and the built-in client
   ([09](09-reconcile-api.md)).
6. Signing in and sending; send what was saved meanwhile.

Steps 2–4 need nothing from Reconcile, and the program is useful without it:
the checks print, and the images wait.

## Decided

- **Windows is in the first version** (2026-11-20), beside Linux. CI tests it
  from the start ([10](10-install-update-and-releases.md)).
