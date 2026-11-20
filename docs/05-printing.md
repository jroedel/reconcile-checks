# 05 — Printing

## Over IPP, straight from the program, to any IPP Everywhere printer

Decided 2026-11-20. The program speaks **IPP** itself: it sends the PDF in a
`Print-Job` request, with every option in the request, to the printer's
address. No `lp`, no CUPS, no Windows print queue, no driver. The same code
on Linux and Windows.

**Nothing in the program is about one printer.** The printer today is an HP
LaserJet Enterprise M612, and it may not be in ten years. So the program
assumes only **IPP Everywhere**, the standard that driverless printing on
every system rests on, and learns everything else from the printer itself:
it asks for the printer's attributes (`Get-Printer-Attributes`) and offers
the trays, media types and sizes the printer says it has. A new printer is
a new entry in Settings, not a new release.

`foundation/ipp` is the IPP client: four operations
(`Get-Printer-Attributes`, `Validate-Job`, `Print-Job`,
`Get-Job-Attributes`), knowing nothing about checks. The wire format is
[`github.com/OpenPrinting/goipp`](https://github.com/OpenPrinting/goipp)
(BSD-2, pure Go, no dependencies of its own, and it encodes the `media-col`
collection); the transport, the certificate pinning and the operations are
ours.

## The M612, as it describes itself

Asked over IPP on 2026-11-20 (`ipptool … get-printer-attributes.test`,
read-only):

| Attribute | It says | So |
|---|---|---|
| `document-format-supported` | includes `application/pdf` (PDF 1.2–1.7) | The program sends the PDF as it is |
| `ipp-features-supported` | `ipp-everywhere`, `airprint-2.1` | Driverless printing is supported |
| `sides-default` | `two-sided-long-edge` | **Duplex by default**: the program must send `one-sided` |
| `print-scaling-default` | `auto` | **Scales by default**: the program must send `none` |
| `media-source-supported` | `auto`, `tray-1`, `tray-2` | Tray 1 is the multipurpose tray, **where the checks go** (decided 2026-11-20); tray 2 is the main drawer, holding plain letter |
| `media-type-supported` | includes `stationery-preprinted` | The media type for check stock |
| `operations-supported` | includes `Get-Job-Attributes`, `Cancel-Job` | The job can be followed and cancelled |

Two of its defaults would misprint a check, which is the reason for the next
section.

## Options set by the program, every time

A printer's defaults are someone else's choices, made for ordinary paper. So
every option that decides where ink lands on a check is in the request,
every time:

| Option (IPP attribute) | Value | Why |
|---|---|---|
| `sides` | `one-sided` | Duplex is the default above. A duplex pass through check stock prints the second page on the back of a check, or jams |
| `media-col` → `media-source` | the account's tray | The checks live in their own tray; plain paper must never be drawn instead, nor checks drawn for a letter |
| `media-col` → `media-type` | the account's media type (`stationery-preprinted` on the M612) | Tells the printer this is pre-printed stock |
| `media-col` → `media-size` | Letter | So the printer neither scales nor refuses the page |
| `print-scaling` | `none` | Any fit-to-page moves every box by a few millimetres |
| `copies` | 1 | |
| `document-format` | `application/pdf` | |

**Nothing is substituted.** A printer that can't honour an option may
quietly print with one of its own instead, and say so only in a status code
(`successful-ok-ignored-or-substituted-attributes`) after the check is already
coming out of the tray. So every job is sent with
`ipp-attribute-fidelity` set, which tells the printer to refuse rather than
substitute, and before every print the program makes sure twice:

1. **Against the printer's lists.** It asks the printer for its attributes,
   afresh each time, and refuses a value the printer doesn't offer, saying
   what it does offer.
2. **With `Validate-Job`.** It sends the whole request, without the PDF, and
   prints only if the printer accepts it. A refusal names the options it
   won't honour. Every IPP printer must support `Validate-Job`.

Neither check is enough on its own. A printer may honour an option it leaves
out of a list, which only `Validate-Job` can show. And a printer may accept
a `Validate-Job` for a value it doesn't have: CUPS's own `ippeveprinter`
answers `successful-ok` for a tray that doesn't exist, even with fidelity set
(found 2026-11-20, while testing the client against it).

On the printer's own panel, tray 1 should be set to the paper type
"Preprinted" (or "Any type"). An HP asked for preprinted paper from a tray
it believes holds plain paper may stop and ask for the right paper to be
loaded. Settled once, when the tray is set up.

**Nothing prints without the person pressing Print** ([06](06-writing-a-check.md)).

## Finding the printer

**Settings → Printers** lists what is found on the network by DNS-SD (the
`_ipp._tcp` and `_ipps._tcp` services that printers announce), and accepts
an address typed in. For each printer it shows what the printer reports
about itself, and an account then chooses its printer, tray and media type
from those lists.

**Kept by name, not number.** The M612 is at `192.168.2.92` today, an
address the router may change. Its announced name (`NPI46CBDF.local`) follows
it. Windows 10 and later resolve `.local` names themselves. **On Linux the
program resolves them itself** (`foundation/mdns`): a pure-Go build doesn't
go through the system's mDNS resolver, so `NPI46CBDF.local` would not be
found otherwise. A typed address is kept as typed.

Finding printers sends one-shot queries from an ordinary port and asks for
the answers to come back to it directly, rather than listening on the mDNS
port, so Windows Firewall never asks about it.

Each query goes out of **every** network the computer is on, not only the
one the routing table picks for multicast. On the development computer that
route led into a VPN tunnel, and the M612 was never asked (found
2026-11-20); a computer running Docker can do the same. Tunnels, loopback
and networks that are down are skipped. Asked this way, the office network
answers with the M612 and three other printers, `_ipp._tcp` and `_ipps._tcp`
alike.

**This computer's queues are not used.** This Linux machine has four for the
M612: one over USB through `ipp-usb`, one IPP Everywhere queue to
`ipps://NPI46CBDF.local:631/ipp/print`, one raw socket to port 9100, and one
through HP's own driver. Raw port 9100 can't carry IPP options at all, and a
queue's defaults are what this program refuses to depend on. The program
talks to the printer itself, at the IPP Everywhere address.

**Encrypted where the printer allows it**: `ipps://` when the printer offers
it. Printers use a certificate they made themselves, so the program
remembers the printer's certificate when it is added in Settings, and asks
again if it changes (after a firmware update, typically). A check's payee and
amount are not secrets on an office network, but there's no reason to send
them in the clear when the printer can encrypt.

## Which way up

Settled once per printer and tray with a plain sheet marked "top, this side
up", and written down twice: in the account's `feed`, which the page shows
beside the Print button ("Tray 1: face up, stubs in first"), and here.

| Printer | Tray | Feed |
|---|---|---|
| HP LaserJet M612 | `tray-1`, multipurpose | **Face up, the stubs' edge in first.** The side facing up is the side printed, and the edge fed in first becomes the bottom of the page, so the check's end goes in last. |

The M612's answer was found on 2026-11-20 with tray 1's paper type set to
Preprinted on the panel and the job asking for `stationery-preprinted`, as
every check does. A printer's handling of letterhead and preprinted paper is
a setting on its panel on some printers; if anything about tray 1 is
changed there, mark a plain sheet and test again before the next check.

It is also the program's default: a new account's form starts with it, and
an account saved without a feed is given it (`settingsbus.DefaultFeed`).

## Did it print?

The printer accepting a job is not the check being printed. The program
follows the job over IPP (`Get-Job-Attributes`) until it is completed,
stopped or aborted, for up to two minutes, shows each state on the page as
it comes, and then asks
the person: **"Did it print correctly?"** A jam, a skew or a blank page is
answered there, with void and reprint ([06](06-writing-a-check.md)). The
person's eye is the check; the program doesn't try to be.

## Printing to a file, for development

A printer in the config may be `file:<directory>`: the program writes the
PDF there and sends nothing. This is what development and any agent use.
The IPP client is tested against a fake IPP server that records what it
received ([10](10-install-update-and-releases.md)), never against a printer.

Real check stock is not cheap and a misprint can't be undone, so **only a
person prints to a real printer**. See [`CLAUDE.md`](../CLAUDE.md).

## The M612 and `print-scaling=none`

It lists `none` in `print-scaling-supported`, but leaves `print-scaling` out
of `job-creation-attributes-supported`, and its default, `auto`, would shrink
a full Letter page into its 4.23 mm margins and move every box. So whether it
would honour `none` was an open question until a person printed the first
calibration page on plain paper, on 2026-11-20: **it accepted the job with
`print-scaling=none` and fidelity, and printed the page at full size.** The
ruler's millimetres matched the check's own spacing across the page, and,
held to the light against a blank check, the boxes lay on the check's rules,
so the M612's offsets are 0.

The program still sends `none` with fidelity on every job, so a firmware
update that changes this shows as a refusal naming `print-scaling`, never as
a scaled check.

## Open questions

None yet.
