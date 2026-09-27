# arcctl

arcctl is a terminal tool (TUI and CLI) that configures ProtoArc mice over USB HID, so you do not need the vendor's web app or Chrome.

arcctl is unofficial. It is not affiliated with, endorsed by, or supported by ProtoArc. ProtoArc and its product names are trademarks of their owner.

## Status

M4 TUI code complete; hardware stages H0-H4 pending.

Pre-alpha. The pure core (M1), the read-only transport, session and CLI (M2), the write engine (M3: preflight, executor, journal, recovery, revert and dry run) and the TUI (M4: button mapping and DPI editing with a review screen) are built and tested against an emulator, including a fault-injection matrix. Settings change only through the TUI's review screen, and `arcctl journal recover` settles a write a crash left unfinished. None of this has run on a real receiver: the hardware stages H0 to H4 have not been signed off, so writes need `--allow-untested` and a typed confirmation.

Commands:

| Command | What it does |
|---|---|
| `arcctl doctor` | Input Monitoring status and the app that holds it, the receiver's interfaces and which one answers, other programs holding the receiver, Secure Input, the lock, and on Linux the udev commands |
| `arcctl info [--json]` | online state, versions, model, battery, profile and long-range support |
| `arcctl dump [--range a:b] [--bin] [--json]` | the flash as hex, raw bytes or a decoded summary |
| `arcctl backup [--full]`, `arcctl backups` | save or list backups |
| `arcctl show FILE`, `arcctl diff A [B]` | decode a backup or web `.bin`, or compare two (or one with the mouse) |
| `arcctl export-bin BACKUP -o FILE` | write a file the web app can import |
| `arcctl trace [--for 60s]` | print every report the receiver sends; sends nothing |
| `arcctl journal status` | list writes a crash left unfinished, and the last write that changed each mouse |
| `arcctl journal recover [--forward\|--back\|--leave]` | settle an unfinished write: finish it, roll it back, or record it as settled |
| `arcctl redact IN -o OUT` | strip addresses and shortcut and macro content from a `--record` transcript |

On macOS, arcctl first checks that the terminal app has Input Monitoring (System Settings, Privacy & Security); `arcctl doctor` names the app to grant it to. `--emulate FILE` runs a command against an emulated mouse, with no hardware; the `journal` commands refuse it, because only a real mouse has a journal.

## The TUI

`arcctl` with no command starts the TUI. It is laid out for terminals of 80x24 and larger; when stdin or stdout is not a terminal, arcctl prints the command list instead. Every action works from the keyboard.

Try it without hardware on the demo mouse, `flash-dump.bin` plus its shortcut bodies:

```sh
arcctl --emulate testdata/demo-em11.json --allow-untested
```

The emulated mouse's journal and backups go to a new temporary folder, named on stderr and on the Info tab, never to the real data folder. `--dry-run` sends every write to an in-memory overlay and shows its packets, `--replay FILE` opens a recorded transcript read-only, `--os mac|win` picks the key names, `--ascii` limits the screen to ASCII, and `--no-color` (or `NO_COLOR`) turns colour off.

Tabs: **Buttons** (system functions, media keys, the shortcut presets of the current OS, and a key composer; there is no live key capture), **DPI** (stage values, stage count, current stage), **Info** (identity, versions, tiers, hidden fields read-only) and **Log** (what the session did, and the revert review). Edits are staged: nothing reaches the mouse until you open the review with `a`, read the exact diff, its web-app warnings and tier gates, and confirm. Untested changes need the phrase `write untested`; each written record is read back and compared.

| Key | Action |
|---|---|
| `1`-`9`, `tab`, `shift+tab` | switch tabs |
| `↑↓` or `jk` | move |
| `enter` | change the selected button or stage |
| `a` | review and apply the staged edits |
| `u` | discard the staged edits |
| `U` | review a revert of the last write |
| `r` | reload from the mouse |
| `b` | back up now |
| `o` | switch labels and presets between mac and win |
| `?` | help for the current tab |
| `esc` | back, or close a dialog |
| `q`, `ctrl+c` | quit; asks first while writing or with staged edits |

Buttons: `s` shows all 16 slots and `d` drops the selected button's staged edit. The last button on Left Click is guarded and cannot be changed. In the picker, `tab` or `←→` switch groups and `/` filters; in the composer, `1`-`8` toggle the modifiers and `0` clears them. DPI: `←→` steps through legal values, `enter` types a value, `space` sets the current stage, `+`/`-` changes the stage count, `del` undoes a stage's edit and `x` shows the raw bytes and colours. Log: `v` opens the revert review and `G` jumps to the newest entry.

A write the TUI starts can be stopped with `s` in the review; it stops after the current record. If a write is cut short, by a stop or a crash, the TUI shows the unfinished write, at once or at the next start (`R` reopens it), and offers to finish it, roll it back, or leave it as it is.

## Planned scope

- EM11 Pro first: button mapping (system functions, media keys, shortcuts, macros), DPI stages, battery and firmware info.
- Staged writes: you review every change before it is applied, and each written record is read back and compared.
- Automatic backups before the first write, a crash journal, and undo.
- Restore from a backup, and import and export of the web app's `.bin` files.
- Other ProtoArc mice get the decoded view, with writes gated behind an explicit flag until they are tested on hardware. Keyboards are read-only.

## Safety

- Close the ProtoArc web app (and any Chrome tab using it) before running arcctl. Two programs talking to the receiver at once can garble replies.
- Your mouse's settings live only in the mouse's own memory. There is no cloud copy. arcctl will back them up before it writes anything, but keep that in mind.

## Install

Not released yet. The plan is:

```sh
brew install Positronico/tap/arcctl
```

## Build and test

```sh
make check      # vendor-file guard, gofmt, go mod tidy, go vet, staticcheck, go test -race, short fuzz runs,
                # cross-builds, the vendored usbhid check, third-party notices, release-binary checks (macOS),
                # mirror-only checks
make generate   # regenerate the Go tables and udev rules from internal/catalog/facts
make build
```

staticcheck is pinned in `go.mod` and runs as `go tool staticcheck`. `make fuzz FUZZTIME=1m` runs each fuzz target for longer (50,000 runs each by default).

`ARCCTL_MIRROR` is for maintainers only. It points at a private checkout; when set, `make check` also compares every public file's SHA-256 with the vendor and research files there, and runs `make drift` (regenerates the device facts from the vendor files and diffs them against `internal/catalog/facts`) and `make oracle` (regenerates `testdata/oracle` and diffs it). Without it those steps are skipped.

## License

MIT. See [LICENSE](LICENSE). The third-party code in release binaries is listed with its licenses in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES); `scripts/notices.sh --write` regenerates it.
