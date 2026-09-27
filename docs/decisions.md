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

### D10: hardware-test writes (2026-09-26)

Choice: the hardware test stages build their own write plans and send them through a raw path in `internal/hwtest`, which exists only in builds made with `-tags hwtest`. The release planner keeps refusing identity writes, inactive DPI stages and untested Experimental features.
Reason: test-only switches inside the core packages would put bypasses into code the release binary also runs.

## Technical choices

T1 to T40 were made during M1 and T41 on during M2 (both 2026-09-26) unless dated otherwise. Each entry gives the choice and, where it is not obvious, the reason. Values marked "until H0" or "until H1" are guesses the hardware tests will settle.

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

### hidio and the vendored usbhid (M2)

**T41: usbhid is vendored as a package.** `rafaelmartins.com/p/usbhid` at `v0.0.0-20260903160318-2edd824d3b06` lives in `internal/third_party/usbhid` as a plain package of this module, with its BSD 3-Clause `LICENSE` and a `NOTICE`. There is no `go.mod` replace. Reason: the upstream mirror is archived and has no tags, `go install` refuses a module with replace directives, and as a package the patched code is covered by our vet, staticcheck and tests. `patches/verify.sh` rebuilds the copy from the sum-checked upstream module, the patches in order, an import-path rewrite and the removal of `go.mod`/`go.sum`, then diffs it; `make usbhid-patches` runs it.

**T42: patch 1, setReport timeout.** `IOHIDDeviceSetReportWithCallback` gets a timeout of 1000, which is one second: the argument is typed `CFTimeInterval`, but IOKit reads it as milliseconds. A report the device never accepts then completes with `kIOReturnTimeout`. The wait for the completion also ends when the device starts closing, after a 2 s grace for a report already in flight; an abandoned report's buffer is leaked if IOKit may still own it, so a normal `Close` still never tears down during I/O.

**T43: patch 2, buffered input.** The darwin input callback queues up to 64 reports in front of `GetInputReport` instead of dropping every report that arrives while the reader is busy, and counts drops (`Device.DroppedInputReports`). When the queue is full it still drops the newest report.

**T44: patches 3 and 4.** Patch 3 keeps the report descriptor read at enumeration and returns it from `Device.ReportDescriptor` (darwin and Linux). Patch 4 only makes the upstream code pass vet and staticcheck.

**T45: only `Guarded` makes a Transport.** `Transport` has an unexported marker method, so only `hidio.Guarded` produces one. Device `Raw`s are unexported hidio types, `hidio.Open` re-checks the VID and PID against the catalog, and the emulator's `Bus.Open` also returns only guarded transports. `internal/guard_wiring_test.go` (go/types) fails on a `Guard.Set` call outside `session`, an exported function outside hidio that returns a `Raw`, or a type that embeds a Transport and defines its own `Write`.

**T46: the M2 build is read-only.** `Guard.Set` returns an error and enables only the policies listed in `enabled` in `guard.go`, which in M2 is ReadOnly alone; every other policy is refused and logged. `Guard.SetTarget` switches between the mouse and the keyboard target (the keyboard probe retried without the 0x80 flag) and is logged too. Refusals wrap `hidio.ErrForbidden` with the `wire` error that explains them. M3 adds policies to `enabled`.

**T47: vendor channel check.** Before any packet, an interface's report descriptor must declare output report 8 of 16 bytes in an application collection on usage page 0xFF02 (`ErrNotVendor` otherwise). Reason: VID 0x062A is shared with other vendors, and the udev rules still grant access to every catalog VID and PID. The usbhid backend checks at enumeration and open, the hidapi backend at open. Windows exposes no descriptor, so the usage-page 0xFF02 filter at enumeration stands in for it there.

**T48: input split.** Each Raw splits input in its reader: report-8 frames go to one queue and all other reports to `Others()`, so a burst of mouse movement cannot evict a reply. `Guarded` passes only 16-byte report-8 frames to `Reports` and turns everything else into one coalesced `Wake` signal. Each queue holds 256 reports and drops the oldest, with a counter (`Transport.Dropped`). `Transport` gained `Err` and `Dropped` over the PLAN sketch.

**T49: one write path for devices.** Every device Raw writes through the same helper: up to 3 tries on `kIOReturnError` (0xE00002BC) with 20 and 40 ms backoff, a 2 s watchdog on each try, and a sticky Stalled state once the watchdog fires (`ErrStalled`). A stalled usbhid handle is closed in a detached goroutine; a stalled hidapi handle is left open. The emulator's `Pipe` uses the same helper.

**T50: hidapi backend.** `github.com/sstallion/go-hid` v0.15.0 behind the `hidapi` build tag (cgo). `Init` runs once, then `SetOpenExclusive(false)` on darwin. Every call runs on a locked OS thread with SIGURG blocked through `pthread_sigmask`, with one writer thread per device; reads use a 250 ms timeout. Close order: stop, join the reader, then `hid_close`. On Linux go-hid uses hidraw, so the hidraw udev rules cover it.

**T51: enumeration.** Every catalog VID and PID pair, compared as numbers; a usage-page filter only on Windows (0xFF02). Candidates are deduplicated by path and sorted, carry the interface number read from the path (`IOUSBHostInterface@N`, `mi_XX` or the sysfs parent; -1 when unknown) and no serial number. hidio never picks a candidate; the session probes them.

**T52: error classes come from the IOReturn text.** `hidio.IOReturn` parses the last `(0xE00002xx)` in an error's text, because that is the only place usbhid (`ioReturnError`, wrapped by `SetOutputReport` and the input callback) and hidapi expose it. `hidio.Classify` is the single table (Retry, Timeout, Locked, Permission, Seized, Gone, Stalled, Refused, Other); `platform.Diagnose` adds what the OS knows on top of it.

**T53: transcripts.** JSONL: a header line, then one entry per packet with `dir` set to out, in, out-error, in-error or note, bytes as hex with `xx` for masked bytes. `Redact` masks the cmd-3 address, the data of cmd-7 packets and cmd-8 replies inside 256..6911 (the shortcut and macro slots) except the event count at +31 of each macro header, all data of keyboard-flagged packets and of input reports other than report-8 frames, and the checksum of every redacted packet; times become UTC. Replay ignores masked bytes, handshake bytes 5 to 8 and byte 15, fills masked bytes with 0xFF (the address with the 11 22 33 placeholder) and recomputes the checksum. Only copies made by `arcctl redact` go under `testdata/transcripts`, and `.gitignore` keeps every other `*.jsonl` out.

### Emulator (M2)

**T54: the emulator hands out only guarded transports.** `emu.Bus` mirrors `hidio.Enumerate` and `hidio.Open` and satisfies `session.Devices`; `hidio.Open` refuses its backend name. A go/types test checks that nothing exported by `emu` is, returns or holds a `hidio.Raw`.

**T55: determinism.** One lock per bus, a seeded PCG for latency jitter and reply stealing, and a manual clock. With zero latency a reply is delivered before `Write` returns.

**T56: receiver and mouse.** The receiver answers cmds 3 and 29 and makes NAKs itself, even while the mouse sleeps; cmds 1, 4, 7, 8, 9, 14, 18, 22 and 23 need an awake mouse. Interface 0 is silent. A "Chrome" competitor polls cmds 3 and 4 in Broadcast mode (every client sees every reply) or Steal mode (each reply goes to one client).

**T57: reply layouts are guesses until H0 and H1.** Handshake len 8 echoing the nonce then cid, mid and connection type; cmd 4 len 6; cmd 3 len 4 (online flag, then the address reversed); cmds 14 and 23 len 1; cmds 18 and 29 len 2; a NAK is the request's header with status 1 (optionally without the length, `Behavior.ShortNAK`); StatusChanged len 2. Whether unknown commands and bad checksums get a NAK or silence, and whether pushes happen on sleep and wake, are switches.

**T58: flash in the emulator.** Unwritten flash reads 0xFF, reads past 16 KiB get a NAK, and cmd 9 restores the factory image. `emu.Defaults` builds a model's factory image from the catalog defaults with the M1 encoders; `WithBodies` fills the shortcut and macro slots a binding points at but a dump lacks with placeholder bodies, so a settings-page dump can be served whole.

### Session (M2)

**T59: one owner goroutine.** `session.Run` owns the Transport, the image and the guard. Each loop turn handles pending API requests, reports and wake signals, then timers that are due, then one unit of work, and only then blocks. A transaction runs inline and dispatches unrelated reports while it waits. Readers publish immutable snapshots through a coalescing `Changed()` channel.

**T60: the guard in the session.** One guard per session, set to ReadOnly when `Run` starts; `Apply` returns `ErrReadOnly` in M2. The target switches per interface and each switch is logged.

**T61: probing.** Every candidate is opened shared and sent cmd 3 up to 3 times with a 150 ms wait. Interfaces that fail the vendor-channel check, stay silent or NAK are closed. One answer attaches; several go to Choosing, each handshaken once online, unless `--device` names one. When nothing answers, the failure picks the state in this order: Locked, NeedsPermission, Seized, Stalled, then NoReceiver with `ErrNoAnswer`.

**T62: transactions.** Drain before send, then 5 tries of 200 ms; reports that do not answer the request do not use up a try. A reply answers when `wire.Match` accepts it with status 0, or when it has status 1 and the same command (and address, for cmds 7 and 8) whatever length it echoes, because the NAK layout is unknown until H1. Inbound checksums are counted (`Stats.BadChecksums`), not enforced, until H0.

**T63: classifying other report-8 frames.** In order: cmd 10 is a push; a frame matching the current request with an unknown status is logged; a match for a request completed in the last 2 s is a duplicate and one that timed out in that window is a late reply (both logged only); an unrequested cmd 3 is logged and, when it says online, taken as a wake hint; a stray status-1 frame is logged; anything else is foreign and puts the session in Conflict. Foreign replies never reach the image.

**T64: jobs.** Loads, push re-reads, backups and explicit reads are queues of single transactions (reads of at most 10 bytes, or queries) with a cursor, one transaction per loop turn, in the priority re-read, load, then backup or read. Every successful read goes into the working image and into every job's image; the published image changes only when a job finishes.

**T65: the working load.** 0..256 and 6912..6987; then each bound shortcut slot whole, and for each bound macro the 32-byte header of both the button's own slot and the slot its binding names, then the events after the count byte (5 × count + 2 bytes when the count is 1 to 70); then cmds 14, 18 and 4, plus 23 when the handshake says wireless. A full backup reads [0, 6987) and [9504, 9760) minus bytes already known, 725 reads from scratch.

**T66: offline during a job (I7).** When a mouse command runs out of tries, cmd 3 decides: offline pauses the job on the failed op, online gives the op one more transaction and then leaves its bytes Unknown (listed as unread or missing). A job with no successful read for 30 s while the mouse seems online gives up its remaining reads.

**T67: wake and identity.** Every Offline-to-online transition re-handshakes. The same cid and mid (and address, once trusted) resume a paused load; a different device fails running jobs with `ErrDeviceChanged` and starts a fresh load; a wake from Ready always reloads. `TrustAddress` stays off until H0 shows the cmd-3 address is stable.

**T68: pushes.** StatusChanged flags re-read the ranges of PLAN §5; 0x04 in the first flag byte (profile switch) fails a running backup or read with `ErrProfileChanged` and reloads, and 0x40 polls the battery.

**T69: SuspectedConflict.** Entered after 2 unanswered cmd-3 tries in a row, or when a mouse command needs 3 or more tries or goes unanswered while cmd 3 says online. It clears after 3 transactions in a row answered on the first try plus a clean client scan, checked every 5 s; without a scanner the scan counts as clean. The thresholds are tuned at H0.

**T70: Conflict.** A foreign reply enters it. It ends only through `ClearConflict` after 10 s with no foreign reply, or when a different device (path, VID and PID, or cid and mid) is attached; a reattach of the same device keeps it.

**T71: write errors are not missing replies.** A write the OS refused or that timed out counts in `Stats.WriteErrors`, not as an unanswered try, and never feeds the conflict signals. The device is dropped only when it no longer enumerates, when its reader ends or the error is ClassGone, or after 3 transactions in a row that failed to write (`ErrWrites`); the session then rescans.

**T72: blocked and stalled states.** Locked, Seized and NeedsPermission remember the state before them and retry (cmd 3, or a fresh probe when nothing is attached) with a backoff from 2 s to 10 s; jobs keep their place. A stall closes the handle in a detached goroutine and rescans; `Snapshot.Stalls` counts stalls for the "replug the receiver" banner.

**T73: unsupported devices.** An unknown (cid, mid) or a charging base goes to Unknown with no flash reads. A keyboard reaches Ready with no load until M8.

### Platform (M2)

**T74: macOS calls through purego.** No cgo. Symbols bind lazily once; CoreGraphics and `responsibility_get_pid_responsible_for_pid` are optional, the rest required.

**T75: the responsible app.** The responsibility API first, then the nearest ancestor inside an `.app` bundle (the outermost `.app` wins, so helpers are charged to their app), then `TERM_PROGRAM` unless it is `tmux`. When `TMUX` is set the hint adds that the grant belongs to the app that started the tmux server.

**T76: console state.** Screen lock and the Secure Input holder come from `CGSessionCopyCurrentDictionary`, falling back to the registry root's `IOConsoleUsers` (over SSH). A locked screen is reported as such rather than naming loginwindow as the Secure Input holder.

**T77: IORegistry client scan.** IOHIDLibUserClient children of the IOHIDDevice services whose VID and PID the catalog knows, with `bInterfaceNumber` from the parent entry, seized from `ClientSeized` or the seize bit of `ClientOptions`, and full process names from `proc_pidpath` (the registry cuts them at 16 characters). A seized client or a browser is never benign; `karabiner_observer` is benign by default.

**T78: single-instance lock.** `flock(LOCK_EX|LOCK_NB)` on darwin and Linux, an exclusive share mode on Windows. The holder writes its pid into the file, which is never removed, so the refusal can name the holder.

**T79: Linux permission.** `access(R_OK|W_OK)` on the `/dev/hidrawN` nodes found through the sysfs uevent `HID_ID` and `HID_PHYS` lines; the node is never opened. The udev hint renders the rules from the catalog into a `sudo tee` command plus the `udevadm` reload, and a test keeps it equal to `packaging/linux/70-arcctl.rules`.

### Backups and the CLI (M2)

**T80: backup file.** `arcctl-backup/1` JSON: identity from `plan.Identity` (the key plus its fields, checked on read), the known bytes as runs of "ok" ranges with their hex and failed reads as "missing" ranges, a `sha256` over every ok range's address, length and bytes, UTC times, a null `profile` when cmd 14 was never asked, and a `source` (device, emulator or replay) so emulated backups cannot pass as real ones.

**T81: backup store.** `<Backups>/<model slug>-<key>/<UTC time>[-label].json`, written to a temp file, synced, then hard-linked into place, so a save never overwrites (a clash gets `-2`, `-3`). Files are 0600 and folders 0700.

**T82: web `.bin` export.** `Image.Bytes()` (unread bytes as 0xFF) plus the 64-byte trailer. It refuses an image that lacks any byte the web app's import writes back (0..255, 6912..6987 and each bound key's own shortcut or macro record) and any backup made by the emulator or a replay; `--allow-partial` exports anyway and lists the gaps. Reason: the web import would write the 0xFF fill over the mouse.

**T83: where devices come from.** The real HID backend, the emulator (`--emulate` with a backup, `.bin` or dump; EM11 Pro unless `--model`) or a replay (`--replay`). Only the real device takes the single-instance lock and runs the Input Monitoring preflight.

**T84: waiting and exit codes.** "No receiver" is decided after the first scan. A sleeping mouse or a paused job ends the command after `--wait` (10 s). Conflict ends it at once; SuspectedConflict once the load finishes or after `--wait`. Exit codes follow PLAN §8, plus 1 for general errors (bad file, replay divergence, unsupported device).

**T85: recording and trace.** `--record FILE` writes an unredacted transcript and never overwrites; a bare name goes to the logs folder. `trace` opens every candidate through `hidio.Open` with a read-only guard, never writes, and prints only the ID and length of non-report-8 reports unless `--raw` is given. `arcctl redact` makes the copies that may be committed.

**T86: release checks.** `THIRD_PARTY_NOTICES` (Go runtime, usbhid, purego) is generated by `scripts/notices.sh` and checked by `make check`. `scripts/release-check.sh` (macOS only) checks that the darwin release binaries link only libSystem and libresolv, that `go tool nm` finds no hwtest symbol, and that the hidapi and hwtest builds compile.
