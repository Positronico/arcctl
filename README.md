# arcctl

arcctl is a terminal tool (TUI and CLI) that configures ProtoArc mice over USB HID, so you do not need the vendor's web app or Chrome.

arcctl is unofficial. It is not affiliated with, endorsed by, or supported by ProtoArc. ProtoArc and its product names are trademarks of their owner.

## Status

Pre-alpha. Milestone M1, the pure core, is done: packet framing, the flash image, the device catalog, key tables, mouse codecs and the write planner, all tested offline. Nothing talks to a device or changes any setting yet.

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
make check      # vendor-file guard, gofmt, go vet, staticcheck, go test -race, short fuzz runs, cross-builds, mirror-only checks
make generate   # regenerate the Go tables and udev rules from internal/catalog/facts
make build
```

staticcheck is pinned in `go.mod` and runs as `go tool staticcheck`. `make fuzz FUZZTIME=1m` runs each fuzz target for longer (5 s by default).

`ARCCTL_MIRROR` is for maintainers only. It points at a private checkout; when set, `make check` also compares every public file's SHA-256 with the vendor and research files there, and runs `make drift` (regenerates the device facts from the vendor files and diffs them against `internal/catalog/facts`) and `make oracle` (regenerates `testdata/oracle` and diffs it). Without it those steps are skipped.

## License

MIT. See [LICENSE](LICENSE).
