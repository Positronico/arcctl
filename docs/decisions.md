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

TODO (M1+): HID backend and its local patches, the guarded transport as the single write path, interface probing, conflict signals, and the write watchdog.
