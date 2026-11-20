# reconcile-checks

Write a check in one short sitting, with no Claude needed: type the payee,
amount and memo, and the check prints on pre-printed check stock. The image
of the check as printed goes to [Reconcile](https://github.com/jroedel/reconcile)
under its number before the bank has even seen it. When the statement that
pays it is imported, Reconcile attaches the image to the charge, so the
bookkeeper and the accountants see who, what and why beside the money leaving.

Double-click it, and the browser opens at a form: account, number, date,
payee, amount, memo, with a live preview of the check on its stock. Press
Print, say whether it printed correctly, and the check is in Reconcile as
outstanding. Then sign it, tear it off and mail it.

## What it is

- **One file**, downloaded from GitHub Releases, for Linux or Windows. It
  installs itself on first run and **updates itself** after that.
- **A local web app**: it serves its own pages to this computer's browser on
  `127.0.0.1`, and nothing to the network.
- **Prints over IPP** straight to any IPP Everywhere printer, with every
  option that decides where ink lands set on each job.
- **A client of Reconcile's API**, signed in through Reconcile with a key
  that can only send and void checks. The API contract lives in Reconcile:
  [reconcile#166](https://github.com/jroedel/reconcile/issues/166).
- **No account or routing numbers.** The stock comes with them printed, and
  Reconcile keeps only the last four.

## Status

Planning. Nothing is built yet. What the program does, and the questions
still open, are in [`docs/`](docs/README.md).

## For contributors

How this repository is worked on, by people and by agents, is in
[`CLAUDE.md`](CLAUDE.md).

## License

Copyright 2026 Jeff Roedel.

Licensed under the Apache License, Version 2.0; see [`LICENSE`](LICENSE).
