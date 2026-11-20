# 12 — Look and voice

**Reconcile Checks looks and speaks like Reconcile** (decided 2026-11-20).
The two are tools of the same house, used by the same people, and a person
moving from one to the other should feel they are in the same place. So
this document borrows rather than invents: Reconcile's
[`docs/design.md`](https://github.com/jroedel/reconcile/blob/main/docs/design.md)
for how it speaks, and its stylesheet,
`app/sdk/page/assets/app/app.css`, for how it looks.

## Colours, type and spacing

The stylesheet starts as a copy of Reconcile's, kept in this repository and
served from this program, never linked from Reconcile's site: the page makes
no request beyond `127.0.0.1` ([03](03-the-local-app.md)). Its first comment
names the Reconcile commit it was copied from; a change made there later is
carried over here by hand, in a pull request that says so.

What comes across:

- **The colours, as Reconcile's `:root` tokens**: a warm off-white
  background (`--bg: #fbfaf8`), white surfaces, near-black ink
  (`--ink: #1d1d1b`), a muted grey for secondary text, soft lines, and the
  **accent blue `#1f5f8b`** for links, buttons and the icon. Warning and
  success colours as Reconcile's.
- **Dark mode**, from the computer's own setting, with Reconcile's dark set.
- **System fonts.** Reconcile fetches no font, and neither do the pages
  here. (The check itself is drawn in one embedded font,
  [02](02-architecture.md); that is the paper's, not the page's.)
- **The radius, spacing and line length** as Reconcile's (`--radius: 10px`).

## The page around every screen

As Reconcile's `base.html`:

- **The masthead**: the wordmark **Reconcile Checks**, bold, on the left; on
  the right, the menu behind the same three-line button: *Write a check*,
  *Checks*, *Settings*, *Quit*.
- **The footer**: *"Reconcile Checks is free software."* with a link to the
  source code, as Reconcile's says of itself.
- **Forms** with Reconcile's labels, fields and buttons, so typing a payee
  here feels like typing one there.

## The icon

Drawn in Reconcile's manner: **a white glyph on the accent blue, in a
rounded square**. Reconcile's glyph is a tick; this one is **a check (the
paper kind) in outline, with a signature stroke across it**, so the two sit
side by side on a desktop as a pair and are still told apart.

One SVG is the source. `make icons` makes from it the PNGs (16 to 512
pixels), the Windows `.ico`, and the exe's own icon resource. It is used as:

- the favicon of every page;
- the Windows program's icon, which the Start menu shortcut shows;
- the Linux launcher's icon, installed with the `.desktop` entry.

## Voice

Reconcile's principles, as they are:

- **Plain words.** "Money out", not debit. "Check", "payee", "memo" are the
  words on the paper, so they are the words on the screen.
- **Every number says where it came from.** The proposed check number says
  what it was proposed from: the last few numbers are shown beside it.
- **Verified, or plainly not.** "Couldn't check this number with Reconcile"
  is said in words, never only in a colour.
- **Nothing is lost by accident.** A check is never deleted; a check not yet
  sent says so until it is.

One difference: Reconcile is **phone first**; this is **desk first**, since
it lives beside a printer. Narrow windows still work, but the form and its
live preview are laid out side by side for a desk.

**English only** ([03](03-the-local-app.md)), so the language menu of
Reconcile's masthead is left out.
