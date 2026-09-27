# arcctl

arcctl is a terminal tool (TUI and CLI) that configures ProtoArc mice over USB HID, so you do not need the vendor's web app or Chrome.

arcctl is unofficial. It is not affiliated with, endorsed by, or supported by ProtoArc. ProtoArc and its product names are trademarks of their owner.

## Status

M3 write engine code complete; hardware stages H0-H3 pending.

Pre-alpha. The pure core (M1), the read-only transport, session and CLI (M2) and the write engine (M3: preflight, executor, journal, recovery, revert and dry run) are built and tested against an emulator, including a fault-injection matrix. No command changes a setting yet; editing arrives with the TUI (M4). The only command that writes is `arcctl journal recover`, which settles a write a crash left unfinished. None of this has run on a real receiver: the hardware stages H0 to H3 have not been signed off.

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

staticcheck is pinned in `go.mod` and runs as `go tool staticcheck`. `make fuzz FUZZTIME=1m` runs each fuzz target for longer (5 s by default).

`ARCCTL_MIRROR` is for maintainers only. It points at a private checkout; when set, `make check` also compares every public file's SHA-256 with the vendor and research files there, and runs `make drift` (regenerates the device facts from the vendor files and diffs them against `internal/catalog/facts`) and `make oracle` (regenerates `testdata/oracle` and diffs it). Without it those steps are skipped.

## License

MIT. See [LICENSE](LICENSE). The third-party code in release binaries is listed with its licenses in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES); `scripts/notices.sh --write` regenerates it.
