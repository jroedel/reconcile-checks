# reconcile-checks documentation

What the program does, decided before it is built. The order is the order of
dependency: the program and its shape first, then the paper, then what it
tells Reconcile, then how it reaches a computer.

The starting point was the first README (2026-11-20), now split across these
documents, and [reconcile#166](https://github.com/jroedel/reconcile/issues/166),
which is Reconcile's half.

| Doc | What it decides | Status |
|---|---|---|
| **Foundation** | | |
| [01 Overview](01-overview.md) | What the program is for, who uses it, one check end to end, what is in and out, the order of work | drafting |
| [02 Architecture](02-architecture.md) | One binary, rendering in Go, IPP, the config, the register, where files live, the code's layout | drafting |
| [03 The local web app](03-the-local-app.md) | Opening it, one at a time, when it closes, security on localhost, the screens, the live preview | drafting |
| **The paper** | | |
| [04 Stock and layout](04-stock-and-layout.md) | The stock, what is printed where, the stubs, amount in words, calibration | drafting |
| [05 Printing](05-printing.md) | IPP to any IPP Everywhere printer, the options always set, finding the printer, the M612 | drafting |
| **The program** | | |
| [06 Writing a check](06-writing-a-check.md) | The form, numbering on two computers, payees, void and reprint, the register | drafting |
| [07 The image](07-the-image.md) | The picture of the check that goes to Reconcile, and what the accountants see | drafting |
| **Reconcile** | | |
| [08 Signing in](08-signing-in.md) | OAuth for a native program, one key per computer, where it is kept | drafting |
| [09 The Reconcile API](09-reconcile-api.md) | The calls, and what the program does with each answer | drafting |
| **Delivery** | | |
| [10 Install, update and releases](10-install-update-and-releases.md) | One file, self-update, push to deploy, CI, testing on Windows | drafting |
| **Later** | | |
| [11 Later](11-later.md) | What was considered and left out of the first version | outline |

Status is one of **outline** (headings and what is known), **drafting**
(being worked through), or **agreed** (signed off).

A document with questions still open ends with its **Open questions**. A
question the documents do not answer goes there, rather than being decided
in code.
