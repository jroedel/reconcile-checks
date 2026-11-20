# 11 — Later

Considered, and left out of the first version. Each says what it would cost.

## Maybe

- **A mailing address on the face, for window envelopes.** Payees would need
  addresses, which Reconcile does not keep; kept locally, they are one more
  thing to maintain. Costs a field per payee and a position in the layout.
- **A Positive Pay file for the bank**: the checks written, in the bank's
  format, uploaded so the bank refuses any check not in it. Cheap to make
  from the register once the bank's format is known; worth asking the bank.
- **A link from Reconcile** to the latest release, on the keys screen or the
  account's page, so that installing starts where the person already is.
  Costs Reconcile a link; the download stays on GitHub
  ([10](10-install-update-and-releases.md)).
- **macOS**: the same build and the same IPP, untested until somebody
  prints from a Mac.
- **Printing a batch**: several checks typed, then printed together. Void
  and reprint becomes harder to follow, so one at a time first.

## Not planned

- **Blank stock and MICR printing.** Needs MICR toner and the full account
  and routing numbers on this computer, which nothing else here needs
  ([04](04-stock-and-layout.md)).
- **A Windows code-signing certificate**: it would remove one SmartScreen
  warning per computer, for a yearly fee ([10](10-install-update-and-releases.md)).
- **Spanish pages.** English, like the check and the bank.
- **A printed signature.** A signature image on the computer that prints
  checks makes every check it prints a signed one. By hand, after printing.
