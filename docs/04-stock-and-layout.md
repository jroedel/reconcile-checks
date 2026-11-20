# 04 — Stock and layout

## Pre-printed stock, one per account

Each checking account has its own stock, bought from the bank or a check
printer, with the bank's name, the account holder, the check number and the
MICR line already printed. So each account has its own layout, offsets, tray
and number sequence, in the local config ([02](02-architecture.md)).

**The stock** (decided 2026-11-20): ordered from Walmart Checks, **one
check per page, in the top position**, laser stock, with **two perforated stubs below the check**. The exact
product and the positions of its boxes are read from the paper.

**Nothing the stock already carries is printed.**

## Account numbers

Reconcile keeps only the last four of an account, and the stock comes with
the MICR line, the check number, the bank and the account holder printed. So
the program needs no account or routing number at all. The image it sends
shows the account as `XXXXXX1234`, last four only ([07](07-the-image.md)).
The API route takes no account number; the account is named by its id
([09](09-reconcile-api.md)).

Blank stock would need MICR toner and the full account and routing numbers on
this computer. Not supported ([11](11-later.md)).

## What is printed, and where

A **layout** of the check face and its stubs, placed to the stock's printed
boxes: for each field, its position, width, size and alignment in
millimetres. A layout is data, not code (a small file per stock product,
embedded in the binary), so a new stock is a new file, not a new program
([02](02-architecture.md), "Rendering"). The first is the Walmart voucher
check, top position. One layout per stock product, chosen by `stock` in the
account's config.

**On the face:**

| Field | Printed as |
|---|---|
| Date | In the date box, as `November 20, 2026`; `11/20/2026` if the stock's box is too narrow for that |
| Pay to the order of | The payee, on the payee line |
| Amount in figures | In the amount box, padded with `*`: `**1,234.50` |
| Amount in words | On the dollars line, the rest of the line filled with `*` |
| Memo | On the memo line |

**On the stubs** (agreed 2026-11-20). A top check with two stubs is a
*voucher* check: the first stub goes in the envelope with the check as the
payee's record of what it paid for (the remittance advice), and the second
stays with the payer as the file copy. Common practice prints the same on
both:

- check number, date, payee, amount;
- the memo;
- the **stub note**, optional: the detail the memo line is too short for,
  which is what the payee's stub is for. For a regular payee, the period
  paid ("Weekly cleaning, Nov 3–24"); for a supplier, the invoice numbers
  paid. Several lines are allowed here, unlike the memo.

Not on the stubs: a category, project or account for the books. Sorting is
Reconcile's job, done once there; printed on paper it is a second copy that
can disagree with the first. The payer's copy matters less than usual,
because Reconcile holds the image, and stays mainly as the paper to write
"VOID" on and file.

**Never:** the check number on the face (it is pre-printed), any account or
routing number, a signature.

The checks are **signed by hand**, one signature at any amount (decided
2026-11-20); nothing is printed on the signature line.

A payee or memo too long for its line at the layout's smallest allowed size
is refused in the form, with how many characters fit, rather than shrunk
until it can't be read or run into the next box.

## Amount in words

- Cents as a fraction: `and 50/100`, always two digits, `and 00/100` for a
  whole amount.
- The dollars in words, the first word capitalized, compound numbers
  hyphenated: `One thousand two hundred thirty-four and 50/100`.
- The rest of the line filled with `*`, so nothing can be written after it.
- Hundreds stay hundreds: 1,100 is `One thousand one hundred`, never
  `Eleven hundred`.
- Under a dollar: `Zero and 05/100`.
- **A ceiling per account**, $10,000 unless set otherwise: an amount above it
  is asked about a second time, with the amount in words, not refused. The
  layout also refuses an amount whose words don't fit on the line.

The amount is held as whole cents throughout (`business/types/money`). A
float never touches it.

Table tests, at least: 0.05, 1.00, 1.10, 11.00, 13.00, 19.99, 21.00, 99.99,
100.00, 101.00, 110.00, 1,000.00, 1,001.00, 1,100.00, 12,000.00, 99,999.99,
100,000.00, 1,000,000.00, and every teen.

## Alignment and calibration

**Print calibration page** in Settings prints a calibration page **on plain
paper**: the layout's boxes outlined, with a cross at each corner and a ruler along
two edges. Held to the light against a real check, it shows how far off the
printer is. The correction is an offset in the config, `x` and `y` in
millimetres, applied to the whole page, **never edited into the layout**:
the layout describes the stock, and the offset describes this printer. Each
computer has its own offsets, since each prints on its own printer, or the
same printer through a different tray.

Then one real check, by hand, from a dummy register, before the program is
trusted with the account.

Which way up the stock goes in the tray is settled once, with a plain sheet
marked "top, this side up", and written in the account's `feed` in the config
and in [05](05-printing.md).

## This repository is public

No image of the real stock is ever committed: a scan or photograph of a check
shows its full account and routing numbers on the MICR line. The layout
describes the stock in millimetres, measured from the paper; positions, not
pictures.

## Open questions

1. **The stock, in hand**: the product name, the positions of the boxes in
   millimetres, and that the check number and MICR line are pre-printed
   (assumed). One stock for all accounts? Nothing in the layout can be
   finished before this.
