# Decisions

Choices that shape arcctl, with the date each was made and the main reason. User decisions come first; technical choices follow as they are made.

## User decisions

### D1: name and module path (2026-09-26)

Choice: the tool is called `arcctl`, and the module path is `github.com/positronico/arcctl`.
Reason: a short command name, and the module path has to match the public repo that D8a calls for.

### D2: apply model (2026-09-26)

Choice: edits are staged, shown on a review screen, applied together, and each written record is read back and compared.
Reason: the mouse holds the only copy of its settings, so every write should be deliberate, minimal and verified.

### D3: key entry (2026-09-26)

Choice: shortcuts are built with a composer (modifier toggles plus a searchable key list). There is no live terminal key capture and no native recorder in v1.
Reason: a terminal cannot see many key combinations reliably, and the composer covers the same keys from the keyboard alone.

### D4: factory reset (2026-09-26)

Choice: include the reset command only, with no follow-up writes, behind a fresh backup and a typed confirmation. It stays disabled until hardware test H7 documents what it clears.
Reason: nobody knows yet exactly what the reset erases or whether pairing survives it.

### D5: hidden EM11 settings (2026-09-26)

Choice: settings the web app does not show are read-only by default. Each can become writable under `--experimental` only after its own H8 test with a defined measurement. The left/right button behaviour field stays read-only in all cases.
Reason: there is no evidence the firmware honours these fields, and a bad button-behaviour value could leave the user without a working click.

### D6: pairing (2026-09-26)

Choice: pairing is out of v1; the offline copy of the web app is the fallback. Revisit if H7 shows that factory reset unpairs the mouse.
Reason: a pairing mistake can disconnect the only mouse, and an existing tool already covers it.

### D7: models we cannot test (2026-09-26)

Choice: other mice get the full decoded UI, with writes behind `--allow-untested`, a typed confirmation and an automatic backup. Keyboards get a read-only viewer plus backup.
Reason: there is no hardware here to test them on, and keyboard writes need checksum upkeep that cannot be verified without a device.

### D8a: distribution (2026-09-26)

Choice: public, unsigned GitHub releases from `Positronico/arcctl`, plus a Formula in `Positronico/homebrew-tap`.
Reason: it reuses a release pattern that already works for another project, and a Homebrew install avoids the quarantine prompt without code signing.

### D8b: two repos (2026-09-26)

Choice: this public repo holds only our own code, docs, generated device facts and test vectors. A separate private repo holds the vendor web app copy, the research and every tool that reads vendor files, and those tools write facts and vectors into this repo.
Reason: vendor material cannot be redistributed, and this repo must build and pass its checks without it.

### D9: hardware drills (2026-09-26)

Choice: run every planned hardware drill (bad-checksum probe, interrupted write, factory reset, robustness), each only after a verified full backup.
Reason: the drills are the only way to know recovery works on this unit, and the backup makes each one recoverable.

## Technical choices

Made during M1 (2026-09-26) unless dated otherwise. Each entry gives the choice and, where it is not obvious, the reason.

TODO (M2+): HID backend and its local patches, the guarded transport as the single write path, interface probing, conflict signals, and the write watchdog.

### Layering and tooling

**T1: layering rule.** The pure packages are `wire`, `flash`, `catalog`, `caps`, `keys`, `mouse`, `keyboard` and `plan`. None of them may import `os`, `net`, `time`, `syscall` or anything under `os/` or `net/` directly. Everything they depend on, directly or not, must be the standard library or another pure package, so no path leads to `hidio`, `session`, `safety`, `tui`, `platform`, `backup`, `emu`, `cli`, `vectors` or a third-party module. Standard-library packages may pull in `os` or `time` themselves (`fmt`, `encoding/json` and `embed` do). `internal/layering_test.go` checks this with `go list -json`, test files excluded. Reason: banning `os` and `time` from the whole dependency tree would also ban `fmt`, `encoding/json` and `embed`. What needs guarding is our own code reaching the device, the disk or the clock. This replaces the earlier plan to ban `time` from the whole tree. The pure packages built in M1 happen to pass the stricter rule as well.

**T2: staticcheck as a Go tool.** `go.mod` pins staticcheck v0.8.1 with a `tool` line, and `make staticcheck` runs `go tool staticcheck`. Its modules appear in `go.mod` and `go.sum` as indirect requirements, but no package imports them. `go get` rewrote the `go` line to `1.26.0`, which is the same language version as `1.26`.

**T3: `make check`.** It runs the vendor-file guard, gofmt, vet, staticcheck, `go test -race`, a fuzz run of every `Fuzz` target it finds (`FUZZTIME`, 5 s each by default), a `CGO_ENABLED=0` build for darwin, linux and windows, and the mirror-only checks. `make drift` and `make oracle` (`scripts/drift.sh`, `scripts/oracle.sh`) skip when `ARCCTL_MIRROR` is unset or missing. With the mirror, `drift` re-extracts the bundle tables, reruns the private front end into a temp dir, diffs the result against `internal/catalog/facts` and compares the bundle's SHA-256 with `sources.json`. `oracle` runs the harness's `--verify`, then regenerates every vector file with its recorded seed and case count into a temp dir and diffs it against `testdata/oracle`. `make generate` reruns the catalog generator.

### wire

**T4: Policy is a small enum.** `ReadOnly` (the zero value), `Edit`, `Reset` and `Experimental`. Each adds only its own commands to the read set: Edit adds 7, Reset adds 9, Experimental adds 7 and 22. Keyboards get the read set under every policy (D7). Unknown values allow nothing. Reason: a constant cannot be changed by another package, and the zero value is the safe one.

**T5: Check accepts exactly what Build produces.** Byte 1 is 0, the reserved bits of byte 4 are clear, nothing is past the data, only cmds 7 and 8 carry an address, and each fixed-shape command has its own length (0 for the queries and cmd 9, 8 for cmd 1, cmd 22 as `[0 or 1, 0 × 9]`). The checks run in this order: session target, policy, target flag, framing, request shape, checksum.

**T6: Build returns an error.** `MustBuild` is for tests. `BuildRead` builds reads, so callers never pass a buffer of zeros.

**T7: reply matching.** Cmds 7 and 8 must match on command, address and length. Every other command matches on the command alone, because the cmd-3 reply's length differs from the request's.

### flash

**T8: known bytes.** An `Image` keeps a known bit per byte. Unknown bytes read as 0xFF, but every read also reports whether they are known, so the fill is never decoded by accident. `Set` is the only way to mark bytes known. The zero value is an empty image.

**T9: Diff reports unread bytes.** `Diff` returns whole extents in the caller's order, as two lists: extents that changed, and extents the new image knows but the old one does not. An extent the new image does not fully know is never returned for writing.

**T10: field states.** A pair or record of all 0xFF is Erased, whatever its checksum says. A pair holding 0xFF with a valid complement (`ff 56`) is Unset. A slot class starts as Unknown, so a slot that was never read is never shown as Empty.

**T11: extents.** `Overlaps` and `Contains` cannot overflow. `Extent` has the JSON names `addr` and `len`, matching the backup and journal formats.

### catalog and facts

**T12: facts format.** The facts are ten JSON files described by one JSON Schema file and `SCHEMA.md`, all in this repo. `sources.json` carries the format version. Only the private front end writes the facts. It records input paths relative to the mirror, the extractor's SHA-256 and a constant tool version, and leaves out the Go version so that the output is identical across toolchains. It refuses extracts whose recorded bundle hash differs from the bundle it reads.

**T13: facts validation.** The generator decodes the facts strictly, rejecting unknown fields. A second pass over the raw JSON rejects missing required fields, nulls where they are not allowed, wrong-length arrays and duplicate keys. It also checks what the schema cannot express: label keys exist, sensor and consumer usage references resolve (the keyboard maps included), each (cid, mid) appears once, keyboard layers come in systems × layouts order, DPI ranges and value tables agree, no PID is in two classes, brightness levels are unique, offset names are unique, and usages are sorted. A test-only validator checks the facts against `schema.json` and stops on any keyword it does not support.

**T14: Go types follow the schema.** A pointer field that is required may be null. A field with `omitempty` is optional but never null. A field is `uint8` or `uint16` exactly when its schema range is 0–255 or 0–65535. A test compares the Go types with `schema.json`.

**T15: labels.** Short English labels come from an allowlist kept in the private front end. It maps each language-file entry to an id of ours, such as `button.left`. Only the id and the text reach the facts. An id is 2 to 4 lowercase dotted segments (`^[a-z][a-z0-9_]*(\.[a-z0-9_]+){1,3}$`) of at most 40 characters. A text is 1 to 40 printable characters with no leading or trailing space. Both the front end and the generator enforce this.

**T16: consumer usages.** Media names and labels come from our own table, built from the HID Usage Tables, never from vendor text, because the vendor's keyboard labels for 0x0224 and 0x0225 are swapped. `facts/media.json` holds the whole table: every usage the vendor refers to, plus common ones arcctl labels when it decodes a record another tool wrote. The generator writes it into `catalog` and into `keys/zz_consumer.go`, so the two cannot drift apart.

**T17: corrected vendor values.** The keyboard table's settings end is 9430, not the vendor's 9376, because the settings are read over 9408..9430. The office keyboard's custom light map and tape offsets are `null`, with the vendor's number kept beside them. The front end fails if a corrected vendor value changes. The cfg's media pseudo-type becomes type 5, param 0 plus a usage code, and a lift-off distance of `false` becomes `null`.

**T18: model identity and flags.** `Model.Key` is the cid and the entry's first mid (`7B04`, or `0301` for mids 1 and 3). The pairing cid is an explicit field. The UI flags follow the web app's rules, applied to mice only; keyboards get only the OS-switch lock. Both keyboards get display names of ours.

**T19: generated Go.** Mouse offsets are `Addr<Name>` constants. Keyboard offsets are an `Offsets` struct with `NoOffset` for missing names, because bare constants such as `Macro` would collide in the `keyboard` package. Labels for buttons, presets and repeat modes are resolved at generation time, so `keys` does not import `catalog`. The generator writes only files that changed, through temp files renamed at the end, and a go test fails when the committed outputs differ from what `facts/` generates.

**T20: verified stages.** `verified.json` lists `{model, feature, firmware, stage, date}`. The generator validates it (a known model, a lowercase dotted feature name, firmware as `v%d.%02x`, stage `H<n>` with an optional letter, an ISO date, one entry per model, feature and firmware) and compiles it into `zz_verified.go`. A test checks that every feature is one of `mouse.Features()`. A stage covers only the exact firmware it names.

**T21: udev rules.** One rule per VID and PID pair on the hidraw subsystem, with `TAG+="uaccess"`, generated from the facts.

### keys

**T22: names.** The kinds are `KindModifier` 0, `KindKey` 1, `KindConsumer` 2, `KindMouse` 4 (mouse buttons in macros) and `KindMenu` 7. The Cmd/Win modifier is `LMeta`/`RMeta` in code and "Cmd" or "Win" on screen, and right-hand modifiers get an "R" prefix. Modifier names are ours; other keys use the key table's names with runs of spaces collapsed. An unknown OS gets the Windows names.

**T23: combos keep their order.** A combo is shown and written in its stored order, never sorted, so the display matches the bytes. `MatchPreset` accepts the same keys in any order.

### plan

**T24: Identity lives in plan.** `plan.Identity` is here so that backups can reuse it. `Key()` is lowercase hex and safe as a path segment; the trusted form (cid, mid and address) and the fallback (VID, PID, cid and mid) cannot collide.

**T25: plan gets a Layout.** `plan` cannot import `mouse`, so `mouse.Layout` passes in the binding, shortcut and macro tables, the physical buttons, the frozen extents (key operation @8) and every writable record. A record write must be exactly one listed record, and a body write must start at its slot and stay inside it.

**T26: New builds the plan.** The caller passes changes; `New` fills in the old bytes from the image and the phase from the layout, drops changes that change nothing, adds the two-phase ops, orders them Neutralise, Body, Bind, Record, numbers them and validates the result. Each op's old bytes are the state just before it runs.

**T27: two-phase rebinding.** Every binding that points at a body being rewritten is first set to Disable and bound again after the body is written. A type-6 binding at slot k counts as pointing at macro k and at the macro its own bytes name, since it is not known which one the firmware uses.

**T28: guards in Validate.** Every op's bytes must be known, record and bind writes must sum to 0x55, and the Off and ReadOnly tiers are refused, as are plans for keyboards and unknown models. The Left Click guard counts only physical buttons; if one was on Left Click before the plan, one must be after every op.

**T29: revert.** Reverting runs `New` again on the image after the plan, with each extent set back to its first op's old bytes, instead of replaying the ops backwards.

### mouse

**T30: codecs work on bytes.** Codecs do not know addresses. `DPIExtent`, `ColorExtent`, `KeyFnExtent`, `ShortcutExtent` and `MacroExtent` supply them and refuse an index that does not exist.

**T31: errors.** Decoders return `ErrEmpty`, `ErrUnset`, `ErrTruncated` (read more) or `ErrInvalid`; encoders return `ErrValue`. A failed call returns the zero value. A decoder accepts exactly what its encoder writes, except that DPI also accepts legal but non-canonical flag bytes.

**T32: codec limits are flash limits.** The codecs check the stage count (1–8), the current stage (0–7) and the sensor's DPI ranges. The model's limits (its stage count and maximum DPI) and the 10 ms macro delay floor are checked by the planner, because the web app's recorder can store shorter delays.

**T33: DPI writes.** A changed stage is written with X equal to Y. A DPI lock takes only DPIs from the sensor's first range, because the web app drops the range flag there.

**T34: PlanEdits takes Options.** `Options` carries the device identity, profile, firmware and verified stages recorded at load time. A zero identity is refused.

**T35: tiers.** Feature names are the `verified.json` names. Features the web app offers start Untested and become Verified when `verified.json` covers that firmware. Right-hand modifiers and the Menu key are separate Untested features. Custom combos and media codes the web app cannot build, macros the web app did not write, hidden slots, Fire Key, Profile Switch, DPI Lock and the hidden settings are Experimental and are refused until a hardware record exists. Key operation @8 is read-only and the optional fields are Off. An op takes the lowest tier of the features it touches, and `WebCompat` uses the same test of what the web app can build.

**T36: stage count and current stage.** When both change, the planner picks the write order whose in-between state is safest: current below count is best, an invalid old value counts as unknown, and a state known to be bad is worst.

**T37: hidden setting values.** Each hidden setting accepts only the values the web app's own setter writes (see `docs/protocol.md`).

**T38: Config.** `Shortcuts` is `[16]keys.Combo`, `Binding` pairs each key function with its field state, and `Slots`, `ShortcutClass` and `MacroClass` class the bound body, every shortcut slot and every macro slot. `Hidden` lists every hidden or optional field and every unmapped gap in 0..255. Decode uses only bytes that were read.

**T39: read strategy.** A bound shortcut slot is read whole (32 bytes). A macro is read as its full 32-byte header and then its events, because the decoder checks the name padding. When an edit makes a body longer than what was read, the planner returns `plan.ErrUnread` naming the extent; the session reads it and plans again.

### Test vectors

**T40: oracle harness.** The private harness cuts the web app's own encoder and decoder functions out of the bundle at run time and runs them with minimal stubs. It feeds them seeded inputs from the valid domain and writes only inputs and bytes. Each area has its own random stream and starts with a fixed coverage set. The two macro areas hold 64 cases each to keep the repo small. Inputs stay inside what the web app handles correctly, so its bugs do not become expectations.
