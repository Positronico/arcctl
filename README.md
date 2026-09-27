# arcctl

arcctl is a terminal tool (TUI and CLI) that configures ProtoArc mice over USB HID, so you do not need the vendor's web app or Chrome.

arcctl is unofficial. It is not affiliated with, endorsed by, or supported by ProtoArc. ProtoArc and its product names are trademarks of their owner.

## Status

M5 code complete (macros, backups and restore, factory reset); hardware stages H0-H7 and H9 pending.

Pre-alpha. The pure core (M1), the read-only transport, session and CLI (M2), the write engine (M3: preflight, executor, journal, recovery, revert and dry run), the TUI (M4: button mapping and DPI editing with a review screen) and M5 (a macro editor and library, a Backup tab, `arcctl restore` from a backup or a web `.bin`, and a factory reset that stays locked until a hardware test records it) are built and tested against an emulator, including a fault-injection matrix. Settings change only through the TUI's review screen or `arcctl restore`, and `arcctl journal recover` settles a write a crash left unfinished. None of this has run on a real receiver: the hardware stages H0 to H9 have not been signed off, so writes need `--allow-untested` and a typed confirmation, and the factory reset is not available at all.

Commands:

| Command | What it does |
|---|---|
| `arcctl doctor` | Input Monitoring status and the app that holds it, the receiver's interfaces and which one answers, other programs holding the receiver, Secure Input, the lock, and on Linux the udev commands |
| `arcctl info [--json]` | online state, versions, model, battery, profile and long-range support |
| `arcctl dump [--range a:b] [--bin] [--json]` | the flash as hex, raw bytes or a decoded summary |
| `arcctl backup [--full]`, `arcctl backups` | save or list backups |
| `arcctl show FILE`, `arcctl diff A [B]` | decode a backup or web `.bin`, or compare two (or one with the mouse) |
| `arcctl export-bin BACKUP -o FILE` | write a file the web app can import |
| `arcctl restore FILE [--include-unknown] [--other-device] [--other-profile] [--yes]` | write a backup or a web `.bin` back to the mouse, only the records that differ; `--dry-run` shows the packets |
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

The emulated mouse's journal, backups and macro library go to a new temporary folder, named on stderr and on the Info tab and removed when arcctl exits, never to the real data folder. `--dry-run` sends every write to an in-memory overlay and shows its packets, `--replay FILE` opens a recorded transcript read-only, `--os mac|win` picks the key names, `--ascii` limits the screen to ASCII, and `--no-color` (or `NO_COLOR`) turns colour off.

Tabs: **Buttons** (system functions, media keys, the shortcut presets of the current OS, a key composer and macros; there is no live key capture), **DPI** (stage values, stage count, current stage), **Macros** (the macros on the mouse, unbound ones still in its memory, and a local library), **Backup** (saved backups, their decoded summary, diff, restore and the factory reset), **Info** (identity, versions, tiers, hidden fields read-only) and **Log** (what the session did, and the revert review). Edits are staged: nothing reaches the mouse until you open the review with `a`, read the exact diff, its web-app warnings and tier gates, and confirm. Untested changes need the phrase `write untested`; each written record is read back and compared.

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
| `q`, `ctrl+c` | quit; asks first while writing or with staged edits. In a dialog, `q` closes it unless a write or reset is running |

Buttons: `s` shows all 16 slots and `d` drops the selected button's staged edit. The last button on Left Click is guarded and cannot be changed. In the picker, `tab` or `←→` switch groups and `/` filters; the Macro group lists the macros on the mouse, unbound ones and the library's, and "New macro…" opens the Macros editor for that button. In the composer, `1`-`8` toggle the modifiers and `0` clears them. DPI: `←→` steps through legal values, `enter` types a value, `space` sets the current stage, `+`/`-` changes the stage count, `del` undoes a stage's edit and `x` shows the raw bytes and colours. Log: `v` opens the revert review and `G` jumps to the newest entry.

Macros: `enter` opens the editor, `n` starts a new macro, `B` binds the selected one to a button, `s` saves it to the library, `x` deletes a library macro, `d` drops a pending edit, and `i`/`e` import or export a library file. In the editor, `i` inserts a key (a tap, a press or a release), `c` a mouse click, `space` turns an event between press and release, `←→` change its delay, `J`/`K` move it, `t` tidies unpaired presses and releases, and `b` binds; `?` lists every key. Backup: `enter` shows a backup in full, `d` diffs it with the mouse (there, `u` includes or leaves out the unknown records), `w` restores it through the review, `f` takes a full backup and `X` opens the factory reset.

A write the TUI starts can be stopped with `s` in the review; it stops after the current record. If a write is cut short, by a stop or a crash, the TUI shows the unfinished write, at once or at the next start (`R` reopens it), and offers to finish it, roll it back, or leave it as it is.

## Macros

A macro is a list of up to 70 press and release events, each with the delay after it (10 ms or more), a repeat mode (a number of times, while the button is held, until it is pressed again, or until any key) and a name of up to 30 bytes. Events come from the modifier keys, the composer's key list and the five mouse buttons; there is no recorder. Binding a macro to a button writes the macro first and the binding after it; when the button already runs a macro, arcctl disables the binding, writes and reads back the macro, then binds it again, so an interrupted write never leaves a button running half a macro. Tidy (`t`) finds presses without a release and offers to add the missing releases.

The library, `macros.json` in the data folder, keeps macros that are on no button, which the web app loses on reload. Export writes a new file and never replaces one; import adds the macros whose names are free and names the ones it leaves out.

## Backups and restore

`arcctl backup` saves what the session has read, and `arcctl backup --full` (or `f` on the Backup tab) reads everything the mouse holds. arcctl also saves one before the first write of each session, and a full one before the first write ever to a mouse and before a factory reset. `arcctl show` decodes a backup without the mouse.

A restore compares the backup with the mouse record by record and writes only the records that differ, through the same review, journal and read-back as any other write. It never writes a byte the backup did not capture, and by default it also leaves out unknown or invalid records and the settings arcctl does not write yet (the report rate and the DPI colours, among others); the preview lists what it leaves out and why. A backup of another mouse needs `--other-device` (same model and sensor only), and one read on another onboard profile needs `--other-profile`. Restoring a backup of the mouse as it is writes nothing.

`--include-unknown` (or `u` in the Backup tab's diff) also writes back, exactly as the backup captured them, the records of the settings page that arcctl knows no valid value for: unmapped bytes, other models' fields, and records whose captured bytes are not a valid value. It needs `--experimental` and a typed phrase that names every extent it writes, and it never writes the button bindings, shortcut and macro bodies, a record captured only in part, or a hidden setting that has no hardware test yet.

A web app `.bin` file can be a restore source too. It holds only what the web app had read, so arcctl takes from it the settings records and the shortcut and macro bodies its buttons were bound to, and treats every record of 0xFF fill as not captured. Values the mouse cannot take (too many DPI stages, a report rate the connection cannot carry) are clamped and shown. A `.bin` names no mouse, so it always needs `--other-device`.

## Factory reset

The factory reset (`X` on the Backup tab) sends the mouse's own reset command once, never twice, with nothing after it. Nobody knows yet what it clears on this mouse or whether the mouse stays paired, so it stays locked until hardware test H7 has recorded it for the mouse's model and firmware; until then arcctl explains why and sends nothing. Once H7 has recorded it, the reset takes a fresh full backup, asks you to type `reset`, sends the one packet, reads the mouse again and says what changed; the Log tab records the reset, its backup and what changed. See [docs/safety.md](docs/safety.md) for the steps, the way back and how to pair the mouse again without arcctl.

## Scope

- EM11 Pro first: button mapping (system functions, media keys, shortcuts, macros), DPI stages, battery and firmware info, backups, restore and the factory reset.
- Staged writes: you review every change before it is applied, and each written record is read back and compared.
- Automatic backups before the first write, a crash journal, and undo.
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
