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

T1 to T40 were made during M1, T41 to T86 during M2 and T87 to T136 during M3 (all 2026-09-26), T137 to T172 during M4 and T173 on during M5 (both 2026-09-27), unless dated otherwise. Each entry gives the choice and, where it is not obvious, the reason. Values marked "until H0" or "until H1" are guesses the hardware tests will settle.

### Layering and tooling

**T1: layering rule.** The pure packages are `wire`, `flash`, `catalog`, `caps`, `keys`, `mouse`, `keyboard` and `plan`. None of them may import `os`, `net`, `time`, `syscall` or anything under `os/` or `net/` directly. Everything they depend on, directly or not, must be the standard library or another pure package, so no path leads to `hidio`, `session`, `safety`, `tui`, `platform`, `backup`, `emu`, `cli`, `vectors` or a third-party module. Standard-library packages may pull in `os` or `time` themselves (`fmt`, `encoding/json` and `embed` do). `internal/layering_test.go` checks this with `go list -json`, test files excluded. Reason: banning `os` and `time` from the whole dependency tree would also ban `fmt`, `encoding/json` and `embed`. What needs guarding is our own code reaching the device, the disk or the clock. This replaces the earlier plan to ban `time` from the whole tree. The pure packages built in M1 happen to pass the stricter rule as well.

**T2: staticcheck as a Go tool.** `go.mod` pins staticcheck v0.8.1 with a `tool` line, and `make staticcheck` runs `go tool staticcheck`. Its modules appear in `go.mod` and `go.sum` as indirect requirements, but no package imports them. `go get` rewrote the `go` line to `1.26.0`, which is the same language version as `1.26`.

**T3: `make check`.** It runs the vendor-file guard, gofmt, vet, staticcheck, `go test -race`, a fuzz run of every `Fuzz` target it finds (`FUZZTIME`, 50,000 runs each by default; a run count rather than a duration, because Go's fuzzer sometimes reports "context deadline exceeded" when a timed run ends), a `CGO_ENABLED=0` build for darwin, linux and windows, and the mirror-only checks. `make drift` and `make oracle` (`scripts/drift.sh`, `scripts/oracle.sh`) skip when `ARCCTL_MIRROR` is unset or missing. With the mirror, `drift` re-extracts the bundle tables, reruns the private front end into a temp dir, diffs the result against `internal/catalog/facts` and compares the bundle's SHA-256 with `sources.json`. `oracle` runs the harness's `--verify`, then regenerates every vector file with its recorded seed and case count into a temp dir and diffs it against `testdata/oracle`. `make generate` reruns the catalog generator.

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

**T25: plan gets a Layout.** `plan` cannot import `mouse`, so `mouse.Layout` passes in the binding, shortcut and macro tables, the physical buttons, the frozen extents (key operation @8) and every writable record. A record write must be exactly one listed record, and a body write must start at its slot and stay inside it. Amended by T226.

**T26: New builds the plan.** The caller passes changes; `New` fills in the old bytes from the image and the phase from the layout, drops changes that change nothing, adds the two-phase ops, orders them Neutralise, Body, Bind, Record, numbers them and validates the result. Each op's old bytes are the state just before it runs. Amended by T226.

**T27: two-phase rebinding.** Every binding that points at a body being rewritten is first set to Disable and bound again after the body is written. A type-6 binding at slot k counts as pointing at macro k and at the macro its own bytes name, since it is not known which one the firmware uses.

**T28: guards in Validate.** Every op's bytes must be known, record and bind writes must sum to 0x55, and the Off and ReadOnly tiers are refused, as are plans for keyboards and unknown models. The Left Click guard counts only physical buttons; if one was on Left Click before the plan, one must be after every op. Amended by T226.

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

**T46: the M2 build is read-only.** `Guard.Set` returns an error and enables only the policies listed in `enabled` in `guard.go`, which in M2 is ReadOnly alone; every other policy is refused and logged. `Guard.SetTarget` switches between the mouse and the keyboard target (the keyboard probe retried without the 0x80 flag) and is logged too. Refusals wrap `hidio.ErrForbidden` with the `wire` error that explains them. M3 adds policies to `enabled`. Amended by T195.

**T47: vendor channel check.** Before any packet, an interface's report descriptor must declare output report 8 of 16 bytes in an application collection on usage page 0xFF02 (`ErrNotVendor` otherwise). Reason: VID 0x062A is shared with other vendors, and the udev rules still grant access to every catalog VID and PID. The usbhid backend checks at enumeration and open, the hidapi backend at open. Windows exposes no descriptor, so the usage-page 0xFF02 filter at enumeration stands in for it there.

**T48: input split.** Each Raw splits input in its reader: report-8 frames go to one queue and all other reports to `Others()`, so a burst of mouse movement cannot evict a reply. `Guarded` passes only 16-byte report-8 frames to `Reports` and turns everything else into one coalesced `Wake` signal. Each queue holds 256 reports and drops the oldest, with a counter (`Transport.Dropped`). `Transport` gained `Err` and `Dropped` over the PLAN sketch.

**T49: one write path for devices.** Every device Raw writes through the same helper: up to 3 tries on `kIOReturnError` (0xE00002BC) with 20 and 40 ms backoff, a 2 s watchdog on each try, and a sticky Stalled state once the watchdog fires (`ErrStalled`). A stalled usbhid handle is closed in a detached goroutine; a stalled hidapi handle is left open. The emulator's `Pipe` uses the same helper. Amended by T198.

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

**T66: offline during a job (I7).** When a mouse command runs out of tries, cmd 3 decides: offline pauses the job on the failed op, online gives the op one more transaction and then leaves its bytes Unknown (listed as unread or missing). A job with no successful read for 30 s while the mouse seems online gives up its remaining reads. T133 amends this for reads another client takes the replies of.

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

### Write engine: safety (M3)

**T87: the executor's link.** `safety.Link` is `Transact`, `OnlineCheck`, `Recheck` and `Pending`. The session wraps its no-reply error in `ErrTimeout` and a sleeping mouse in `ErrOffline`; write errors come back as the transport returned them, for `hidio.Classify`. `Recheck` asks the OS again, after every pause, what the preflight asked it (T102).

**T88: journal file.** `<Journal>/<identity key>/<UTC start>-<pid>.jsonl`, format version 1, with run, op, end and resolve entries. A run's planned ops, old and new bytes included, are appended and fsynced in one batch before its first packet, and each later state change is fsynced; the parent of each folder the journal creates is synced too. After a failed append the journal refuses to write again, so a torn line can only be the last one of a file, and it is ignored. A partly written batch is a run that never started, and a run whose ops are all still planned sent nothing: neither is open.

**T89: one op.** Signals, a fresh cmd 3, "sending", chunks of at most 10 bytes, "sent", the read-back, "verified". A write error of class Retry or Timeout is resent up to 2 times. No reply leads to a cmd 3: a sleeping mouse pauses the run and the record is written again from chunk 0, at most 3 times; an online mouse stops it. A locked screen pauses the same way. A NAK, a mismatch, a device gone or stalled, or anything else stops the run; the extent is then read once, when that is safe, and classed old, new or torn.

**T90: signals and pauses.** A profile push stops the run before its next packet. An abort, or a push that re-reads the current or a later op's range, stops it between ops, and also before the current op's first packet, the wait for a sleeping mouse included. A push on the last op's range while it is written cannot stop anything after it and sets `Result.Overlap`. After a pause the link's `Recheck` runs, then every extent the run has yet to write is read again and must hold its op's old bytes (`ErrChanged`); the current op's extent is included until a packet of it went out. The signals taken while a recovery or a revert reads its extents carry over into its run.

**T91: recovery.** `Inspect` reads every extent of the run again, and the records every binding points at with the run's bytes from before, from after and from now (a reload reads only the bodies bound at that moment, and a shorter body hides the tail of the record it replaced), then classes each extent old, new, mid or torn. Forward and Back build their plan through `plan.New`, two-phase and validated, from the first old and the last new bytes of each extent. Leave is refused while an extent is torn and records the ops whose bytes the extents hold. Recovery runs are journaled and folded into the run they settle.

**T92: revert.** It undoes the last apply or revert run that changed the device: one that completed or was finished forward, or one that was left, counting the ops the Leave recorded (the verified ones before any Leave). It needs a clean journal and every extent still holding what that run left (`ErrDiverged` otherwise), and reads the same bodies as a recovery. `PlanRevert` returns the plan without writing, for dry runs.

**T93: guard policies.** The guard enables ReadOnly and Edit. Reset stays off until H7 (D4) and Experimental (cmd 22) until long range has an H8 measurement (D5); Experimental-tier records go out as cmd 7 under Edit. Besides the session, only the tests of `internal/safety` may call `Guard.Set`, to give their test link a writable emulator transport. Amended by T195.

**T94: killing the executor in tests.** The test link panics after chunk k, and the executor writes nothing to the journal from deferred calls, so hwtest can end the process for real at the same point. Skipping fsync is possible only through `export_test.go`.

### Preflight and writes in the session (M3)

**T95: preflight.** `safety.Preflight` is a pure function over facts the session gathers fresh: a handshake, cmd 3, cmd 14, a read of every extent the plan writes, the client scan of every interface with the device's VID and PID, the lock, the console and the journal. It returns every failure at once, each wrapping a typed error. A session that is Locked, Seized or Stalled, or a cmd 3 that fails that way, is `ErrBlocked`. `Session.Preflight` runs the same checks and writes nothing, for writes that do not go through `Apply`.

**T96: typed confirmation.** `ConfirmPhrase` is "write untested" or "write experimental", compared after trimming. Untested needs `--allow-untested`, Experimental needs `--experimental`, ReadOnly and Off are always refused. The per-field D5 check stays in `PlanEdits` (verified.json). Amended by T228.

**T97: gates per kind of write.** Apply runs every check. Revert is gated by the tiers of the run it undoes and has no stale check, which the executor's divergence check covers. Recover skips the tier gates and does not need a clean journal. A dry run skips the tiers, the confirmation, foreign clients, the clean journal and I1.

**T98: I1 in the session.** The backups a session saved are keyed by identity and onboard profile. The first write to an identity and profile, whose journal holds no run on that profile, runs a full backup first, saved as "auto first write"; a partial one is saved and blocks with `*PartialError` unless `AcceptPartial` is set. Otherwise the session's first write to that device and profile saves the loaded image as "auto before write". The record of these backups is dropped on a profile switch, when the mouse is forgotten, and after a reload the session's own write did not cause that found bytes the image lacked or held otherwise.

**T99: dry run.** A `safety.Overlay` wraps the link: cmd 7 is checked by `wire.Edit.Check`, recorded, logged and echoed, and reads come from the device with the overlay's bytes on top. The guard stays ReadOnly and the run goes to a throwaway journal. There is one overlay per session and identity (`Snapshot.DryRun`); a real write, a profile switch or another mouse drops it.

**T100: write passes.** Writes run on the owner goroutine in passes: the checks, then the I1 backups, then the executor. A pass that waits for the full backup starts over from fresh checks. One write at a time (`ErrBusy`). A cancelled caller context or `Abort()` stops the run after the current op, or before the op when none of its packets went out, and the call still returns the outcome; only the context of `Run` stops it before the next packet.

**T101: the guard around a write.** The guard is Edit only around the executor call of a real write; a deferred call puts it back to ReadOnly, even on panic, and each change is logged. A panic becomes `ErrPanic` and leaves the run open, like a crash.

**T102: the write link.** During a write, pushes become signals instead of jobs and get their usual handling once the write ends. The link refuses cmd 7 during Conflict or SuspectedConflict (reads still go through), maps no reply to `ErrTimeout`, handshakes and asks cmd 14 again after the mouse slept, and its `Recheck` repeats the conflict, client-scan, lock and console checks with the write's gates.

**T103: the journal after a load.** The journal is checked after every load (Progress "journal"). A run whose extents all hold its new bytes, or all its old ones, is settled Forward or Back without a packet; anything else puts the session in Recovering, shown over Ready. With `TrustAddress`, unfinished runs of this mouse (the same address) kept under the key without the address are listed too and block writes until they are settled with the address not trusted.

**T104: after a write.** A real write is followed by a reload. `ErrIdentity` takes the handshake path instead, and device errors take the usual state handling. Bytes the preflight read again are published when they differ, so planning again after `ErrStale` works.

**T105: backup store.** `backup.Store` saves labelled backups, never overwriting one, lists them per identity, and `File.Automatic()` marks the two I1 labels.

### Hardware tests (M3)

**T106: the raw path.** A tap sits between each device Raw and the session's guard, in the session Devices hwtest supplies; `hidio.OpenRaw` and `emu.(*Bus).OpenRaw`, which open without a guard, exist only in hwtest builds.

**T107: tap claims.** The tap claims raw replies by matcher. Replies to cmd 7 and cmd 8 are kept from the session, late repeats included, for 3 s; cmd-3 replies are shared with it. When the session sends a packet of the same shape itself, the tap stops keeping those replies from it. A frame counts as a late reply only when no exchange that is still listening took it.

**T108: raw writes.** The raw path only writes the bytes already there. Before each identity write and the probe, the session's preflight runs for that write as one op whose old and new bytes are equal, at the step's tier; then a fresh cmd 3 and a raw read must show the previewed bytes, and a read-back follows. The probe is the same packet with byte 15 one lower, checked to fail only on its checksum.

**T109: stage plans.** Stage plans are built with `plan.New` on the fresh backup's image. Each op takes the lowest tier of its features, plus the hidden-slot feature where it applies. "And back" steps use `session.Revert`, after a dry run of it sent exactly the packets the preview showed.

**T110: write-stage flow.** A complete full backup read back from its file; a dry run of every write, whose packets must equal the plan's chunks; the flags, the confirmation and the typed phrase; the steps; a reload that must find every touched extent as it was. An unexpected answer fails the stage but the steps go on, so every change is undone. An error or an interrupt runs the reverts still owed, the failed step's included, on a context of their own bounded by `--wait`; what cannot be undone is named.

**T111: recording.** Only confirmed stages are recorded: a redacted transcript in `testdata/transcripts/<date>-<stage>.jsonl` (H0 also commits `-h0-info`), a log entry and the stage's status in `docs/hardware-tests.md`, log lines scrubbed of local and device paths, and on a real-device pass `verified.json` merged and sorted, then `go generate ./internal/catalog`, rolled back if the generator fails.

**T112: promotions.** H1 promotes `dpi.current`; H2 `dpi.value` and `dpi.stages`; H3 `button.system`; H0 and H3b nothing. H3b covers slots 6 to 11, and the slots from 12 on that the model shows (12 and 13 on mid 6). Amended by T159: H3b now promotes `button.unmapped-slot`.

**T113: H0.** H0's sessions have no write setup, its raw path sends only cmd 3 and cmd 8, and its interface probes go through a fresh read-only guard.

**T114: `--debug-abort-after-chunk n`.** It exits with code 8 right after chunk n of an op is acknowledged; the tests inject an exit that panics instead.

**T115: stage texts.** They never name a shortcut's or a macro's keys, because the log is public. Amended by T225.

**T116: rehearsals.** `--emulate` runs a stage as a rehearsal into a temporary folder that never promotes, compared against the checkout's `flash-dump.bin`.

**T117: release check.** `scripts/release-check.sh` searches the release binary's symbols for `hwtest`, `cli.hwHost`, `hwSummary`, `hidio.OpenRaw` and `emu.(*Bus).OpenRaw`. Amended by T216.

**T118: hwtest firmware.** The hwtest tests use an emulated firmware v0.42, so the committed `verified.json` never changes the tiers they see.

### Journal commands (M3)

**T119: `journal status`.** It reads only `<data>/journal/*/`, takes no lock (a short probe of the lock names another running arcctl), lists the open runs with their op states and the last run that changed each device, and exits 7 when a run is open and 1 when a journal cannot be read.

**T120: real devices only.** The journal commands refuse `--emulate` and `--replay`: an emulator journal would mark the real device as already written to and skip its first-write full backup (I1).

**T121: `journal recover`.** It runs the session with writes enabled (journal and backup roots, `Lock` returning nil because the CLI holds the lock, the console from the host, `Env.Executor`) and reports the runs the session settled on load by comparing the journal before and after connecting.

**T122: choices.** `--forward|--back|--leave`, in the journal's own words. Without one, recover shows each record's class (labelled with the run's last op on it) and the number of writes per choice from `safety.RecoveryPlan`, then exits 7.

**T123: write flags.** The global write flags map to `safety.Gates`. Recover uses `--dry-run` and `--allow-foreign-client`; `--allow-untested` and `--experimental` are passed through but do nothing, since recover skips the tier gates.

**T124: interrupts.** The session's run context does not inherit the command's cancellation, so the first Ctrl-C cancels only the write; `Main` then restores the default signal action, and a second Ctrl-C ends the process like a crash.

**T125: exit codes of a write.** A blocked preflight exits 6 when any check says the device is blocked (locked, seized, stalled, a conflict, another client or arcctl, the screen lock or Secure Input), else 4 when the mouse sleeps, else 1. A run that stopped exits 6 for the same blocking causes, 4 when it gave up waiting for a sleeping mouse, 8 when it was aborted and 7 otherwise; `--leave` on a torn record exits 7.

**T126: progress output.** Progress rows go to stdout and the pause, resume and restart notices to stderr, in fixed columns; a run ID never shares a line with another, because its length varies with the pid.

### Fault matrix (M3)

**T127: two levels.** The fault matrix runs exhaustively through the executor over the test link, and at sampled chunks (the first two, the middle and the last two of each op; `-every-chunk` for all) through the session's own link, with the session's own recovery before journal recovery. Besides the faults of PLAN §10 item 6, it injects writes the device acks but ignores or stores garbled (`emu.Ignore`, `emu.Corrupt`), a record changed while the mouse sleeps, a profile switch while it sleeps with no push, and a reply nobody asked for.

**T128: matrix invariants.** After recovery, the whole 16 KiB emulator image equals the image from before the plan or the one the plan meant, as the journal resolved; every record the plan touches is valid; no binding runs an invalid body (a macro binding is checked on both slots); every packet passes the Edit policy and every cmd 7 lies inside the plan. At the stop, nothing outside the plan changed, and every extent but the stopped one holds old, new or in-between bytes. A cmd 3 precedes each op's first chunk, and no chunk goes out after the point where the fault must stop the run.

**T129: kills in the session matrix.** A kill cancels the session's Run context and panics in the progress callback; a new session on the same folders then recovers at startup.

**T130: reply stealing during writes.** It uses the emulator's seeded Steal routing with a competitor attached at chunk k; the outcome depends on the seed (complete or stop), and every write ticker is paused, so a seed replays exactly.

**T131: reply stealing during reads.** A competitor opens at read k and takes every reply to that chunk until 20 tries or until the session moves on, then closes before the chunk can be read again.

**T132: stale acks.** A stale ack arrives while chunk k waits for its reply (delayed 15 ms): the previous chunk's ack, or an old-bytes ack for chunk k's own address. The session must count the extra frame as a duplicate, never as foreign.

**T133: reads under reply stealing (amends T66).** A read that goes unanswered while cmd 3 says the mouse is online is tried again, not given up, as long as the client scan shows another program on the device's interface: its replies were most likely taken. The load watchdog (30 s with no successful read) still bounds it. Without another client, T66 applies.

### M3 follow-ups

**T134: `hwtest --dry-run`.** A write stage run with `--dry-run` shows only its preview and exits 0. It connects and checks the model and the journal as a run does, lays the steps out on the image the session loaded (a full backup is only needed before writing), and dry-runs each write into the session's overlay, so the preview is the one a run prints before it asks to go on. It takes no backup, asks nothing, writes nothing, records nothing and, like any dry run, skips the tier gates; it names the flags the stage itself would need. H0 writes nothing and has no dry run (exit 2). With `--emulate` its temporary folder is deleted afterwards.

**T135: hwtest tests in `make check`.** `make test-hwtest` runs `go test -race -tags hwtest ./...` as part of `make check`, so CI runs it on Ubuntu and macOS. The command list differs between the two builds, so the help and no-args output have a golden file per build (`help.txt` and `help.hwtest.txt`, `no-args.txt` and `no-args.hwtest.txt`), chosen by a constant set in build-tagged test files; `help hwtest` has its own golden file in the tagged build.

**T136: the verified.json test.** It no longer requires an empty list: every entry must cover its own model, feature and firmware and no other firmware. The generator already validates each entry, so promotions do not break the test.

### TUI shell (M4)

**T137: stack.** `charm.land/bubbletea/v2` v2.0.10, `bubbles/v2` v2.2.1 (only `key`), `lipgloss/v2` v2.0.6, `x/ansi` for width, cutting and stripping, and `colorprofile` for `--no-color`. `huh` is not used, because its Form is not a `tea.Model` in v2, and neither are `textinput` and `list`, which would add clipboard and fuzzy-search modules: text entry reads key events. `THIRD_PARTY_NOTICES` lists the new modules.

**T138: starting the TUI.** `arcctl` with no command starts the TUI when stdin and stdout are terminals, and otherwise prints the command list. `cli` never imports `tui`; `cmd/arcctl` connects `cli.Env.TUI` to `tui.Run`. `cli.TUI` carries the running session, the mode, the gates, the source, the label OS, `--ascii` and `--no-color`, the backup store, the data folder, the notes printed before the TUI started and the OS hooks the banners need.

**T139: the emulator in the TUI.** With `--emulate`, the journal and backups go to a new temporary folder, named on stderr and on the Info tab, never to the real data folder (T120). The emulator holds exactly what the file holds, with no placeholder bodies; when the file lacks bodies its shortcut or macro bindings run, arcctl names those slots on stderr and in the first notice, and the emulated mouse reads them as erased flash. `testdata/demo-em11.json` is an `arcctl-backup/1` file with source "emulator": `flash-dump.bin` plus the test vectors' shortcut bodies for slots 2 to 5, encoded with `mouse.EncodeShortcut` and padded with 0xFF to 32 bytes, identity 260d-1282-7b04, with no address, firmware or profile. `TestDemoBackup` rebuilds and checks it. Amended by T219.

**T140: colour and glyphs.** `--no-color` uses the ASCII colour profile, and `NO_COLOR` is honoured by Bubble Tea's own detection. `--ascii` swaps the glyph set and filters every line to ASCII; the footer measures its hints after that filter. State is always a text badge.

**T141: snapshots.** One command waits on `Changed()`, waits 33 ms to fold bursts, then reads `Snapshot()`; the shell re-arms it after each, and drops a snapshot with the same `Seq`. The shell sends its first snapshot to the tabs in `Init`.

**T142: write progress.** The executor callback appends to a locked queue and never blocks the session goroutine. One listener drains the events, then the end, so `WriteDoneMsg` always follows the last `OpEventMsg`.

**T143: tabs.** A tab is shown when one of the features it needs has a visible tier on the mouse; Experimental features count only with `--experimental`, and a tab with no needs is always shown. Tab numbers follow the visible tabs in order. The active tab is the user's choice: while it is hidden the first visible tab shows, and it comes back once it is visible again. The default tabs are Buttons (given `Session.Read`), DPI, Info and Log, and the default review is `NewReview(Session)`; `tui.New` builds them when the options leave them nil.

**T144: keys.** The shell takes q, ctrl+c, ?, tab, shift+tab, 1 to 9, a, u, U, r and b before the tab, plus R while a write is unfinished, c in Conflict, o in NeedsPermission and the picker keys while Choosing. A tab that needs those keys (typing a DPI value, a filter) says so with `Capturing()`, and the footer then shows only its hints. Otherwise the footer always keeps its last two hints, puts the review and discard keys first when edits are pending, and shows the others where they fit. No key uses Cmd or Ctrl+Tab.

**T145: dialogs.** The shell keeps a stack: `OpenDialog` pushes, `CloseDialog` closes the top and shows the one beneath. Open dialogs get every broadcast; only the top one gets keys and pastes. Typed confirmations read key events and echo them on a plain line, compared after trimming; a paste into a phrase is ignored with a notice. There is no cursor blink, so the golden files stay stable.

**T146: header.** The mode badge reads EDIT, DRY-RUN (`--dry-run`: every write is a dry run) or READ-ONLY (`--replay`). The tier badge is the lowest tier of the web app's features, or "UNTESTED ON THIS FIRMWARE" when `verified.json` records a feature for another firmware. While a write runs the state badge reads BACKING UP or WRITING. When the header does not fit, the receiver and mouse versions, the connection and the battery are dropped first, then `[EMULATED]` or `[REPLAY]`, then the tier badge; the state and mode badges always stay.

**T147: staged edits.** One store, shared by the tabs and keyed by field; staging a key again replaces its edit in place. It is cleared after a successful apply, on discard, and when another mouse identity answers.

**T148: writes need an approving dialog.** Tabs cannot write. A write starts only from the open dialog that approved it, and only once: the review while it runs with the same plan and gates, or a confirmed recovery prompt. Any other request is refused with a notice. Every write goes through the session's `Apply`, `Revert` or `Recover`, so preflight, the I1 backups, the journal and the read-back always run.

**T149: quitting.** q and ctrl+c ask while a write runs or edits are pending; a second ctrl+c while that question is open quits at once. y during a write stops it and quits when it ends; the question is replaced when the write ends, and its y applies only to the write that was running.

**T150: unfinished writes.** The recovery prompt opens once per open run and R reopens it; f, b or l, then y, calls `Recover`. Leave is refused while a record is torn, and the write counts come from `safety.RecoveryPlan`. At 80x24 the choices stay at the bottom and the records scroll above them. A write that stops under an open review marks its run as prompted, so the review's own recovery prompt is the only one. Recovery progress counts the recovery plan's records. An extent's description, in recovery, revert and inspection, is its last op's.

**T151: notices.** A notice takes up to two lines, and paths are cut in the middle so the file name shows. The notes arcctl printed before the TUI started are the first notice and are listed on the Info tab.

### Buttons (M4)

**T152: rows.** The catalog's visible buttons in catalog order. `s` shows all 16 slots in slot order; the others are named "Slot N" and tagged "not shown by the vendor app".

**T153: labels.** System functions use catalog label ids and media keys the HID usage label. Shortcuts use `Combo.Format` in stored order, plus a preset name when `MatchPreset` matches in any order for the label OS; on win the maintainer's Cmd+V reads Win+V, as the bytes say. Macro names are quoted. Only Profile Switch, DPI Lock and the Fire Key parameters are labelled in our own words. The label OS lives in the shell and starts at `--os` or this computer's; `o` switches the labels and the preset table on every screen, and the review uses the Buttons tab's labels.

**T154: tier and web-app columns.** A slot's tier is its function's feature plus the slot's own (`mouse.SlotFeatures`); a pending edit shows the lowest tier of its ops in a trial plan. The web-app column is `mouse.WebCompat` over a trial plan: for the device state, one that rewrites the slot's binding and body with the bytes it holds now.

**T155: previews and staging.** A choice is planned with `PlanEdits` together with the other pending edits, as the review will plan them, keeping only that slot's ops and warnings; when another edit is what fails, it is planned alone. Staging refuses what the planner refuses, and choosing what the button already does drops its pending edit. A body that was never read previews as 0xFF on a cloned image, for display only; staging reads it through `Session.Read` when the session is Ready.

**T156: Left Click guard (I9).** The tab counts the visible buttons on Left Click, pending over device. Staging or dropping an edit that would leave none is refused, and the review's `d` uses the same count. The last one is marked "guarded" and does not open the picker.

**T157: picker.** Groups System, Special (the current OS's presets), Media (the 17 codes) and Combo Key (the composer); tab or ←/→ switch groups, `/` filters each group by substring, ignoring case, and the first enter ends filtering while the second chooses. It opens on the current function, or on the composer when the composer can build the current combo. On macOS, presets diy1, diy2 and diy4 and media codes 0x0183, 0x018A, 0x0192, 0x0194 and 0x0223 to 0x0227 are dimmed as not offered by the web app; the list is a table in the TUI until `facts/` carries it. Amended by T233.

**T158: composer (D3).** 1 to 8 toggle the modifiers, pressed in toggle order, at most 4 (`MaxShortcutKeys-1`); 0 clears them. The keys are the kind-1 entries of the key table plus ContextMenu, searched by mac name, win name or key code, punctuation included. Right-side modifiers and ContextMenu carry their own feature's tier while it is below Verified. There is no live key capture.

**T159: unmapped slots (amends T35 and T112).** `button.unmapped-slot`, an Untested feature, covers the slots from 6 on that the model shows (12 and 13 on mid 6); slots the model hides keep `button.hidden-slot` (Experimental). `mouse.SlotFeatures` adds the slot's feature in the planner, the Buttons tab and hwtest. H3b promotes `button.unmapped-slot`, so slots 12 and 13 stay Untested until H3b even after H3 promotes `button.system`.

### DPI, Info and Log (M4)

**T160: DPI values.** The legal values are `mouse.DPIs(sensor)` up to the lower of the model's and the sensor's maximum. ←/→ moves to the next legal value. A typed value rounds up to the next legal one, and anything above the maximum becomes the maximum; a notice says which. Typing takes the keyboard, so digits do not switch tabs.

**T161: DPI edits.** Setting a stage back to what the mouse holds on both axes removes its pending edit, and `del` removes the selected stage's edit. An asymmetric stage shows X and Y and is not written unless changed; a change writes X equal to Y (T33). Lowering the count below the current stage also stages the current stage as the last active one, because the planner needs current below count, and gives the old value back once the count leaves room again. The planner's objection to the staged DPI edits shows under the table. Edits are refused in read-only mode and while a write runs, because a successful apply clears the store.

**T162: DPI colours and extra records.** Stage colours are a read-only column behind `x` on office mice (D5) and shown by default on other models. Records past the model's stage count are listed read-only.

**T163: Info.** It never prints the mouse address, only whether it is trusted, and prints the identity key only when the key holds no address. At 100 columns or more, facts and tiers sit left and hidden fields right; below that they stack, under one scroll. The hidden fields' "Writable" column comes from `mouse.Layout`: frozen means never (I9), records mean after H8 (D5), and the rest are unmapped or optional.

**T164: Log.** Built in the TUI from snapshot changes, op events (not chunks) and write ends, stamped with the shell's clock, keeping the last 2000 entries. A view scrolled back stays put when entries arrive, and `G` returns to the newest. A job ends "stopped at x of y" only when the session moved to a state that cuts it short, and "finished" otherwise, because snapshots skip steps; a write's end comes from its result.

### Review and apply (M4)

**T165: the review.** `NewReview(api)` builds it. The pending edits are planned with `PlanEdits`; each op shows its sequence, record, phase, tier and description, the record before and after in words, its old and new bytes and its packet count. The old side is decoded over the loaded image and the new side over the image with the whole plan applied, so a binding shows the body it will run. Web-app warnings, a line that says whether the plan can go ahead, and the checks (session state, journal, other programs at the last scan, preflight, the I1 backups, with a note when a full backup comes first) follow.

**T166: refusals and re-planning.** When the plan fails, each staged edit is planned alone to name the one the planner refuses; with no mouse loaded the review says so, blames no edit and keeps the edits. The review plans again when the pending edits, the image or the model change, compared op by op; a changed plan sends a confirmation in progress back to the review.

**T167: preflight and confirmation.** Enter runs `Session.Preflight` before the confirmation, leaving out the tier and phrase failures the review asks for itself. The phrase is `safety.ConfirmPhrase` ("write untested" or "write experimental", T96), not the word "apply" of the plan; a Verified-only plan and a dry run need `y`, and a dry run skips the tier gates, as the session does.

**T168: running and results.** Progress shows the full-backup bar, then each op's step. `s` stops after the current record ("nothing was written" when no packet went out), esc hides the dialog and `a` shows it again, and `q` hands over to the shell's quit prompt, whose n returns to the review. While a dialog is open the Applying banner shows only its summary line. The result screens: verified (run ID and backup paths), dry run (the exact packets under each op), stopped (the op, the packets acknowledged, the reason, what the record holds now, then the recovery prompt), refused (each failure; enter goes back to the review) and the Overlap note. A partial first backup (`*safety.PartialError` alone in a `PreflightError`) is accepted with `y`, which retries the same plan with `AcceptPartial`. Amended by T217.

**T169: undo in the review.** `d` drops the selected edit, keeping the Left Click guard; `u` discards them all after asking; a plan that already matches the mouse drops its edits on enter.

**T170: revert.** `U` and the Log's `v` open the same review for a revert of the journal's last change. It previews with `safety.RevertPlan` on the loaded image and shows each record before and after, the bytes and the web-app warnings. The session's new `PreflightRevert` runs before the phrase; the gates and the phrase come from the tiers of the run it undoes (T97), Untested and Experimental checked separately. When the loaded image lacks a record, the review shows the journal's records and still lets the revert go on, since the session reads the records itself. A different last run blocks it. Undoing a revert is titled "undo last revert" and names the run it puts back.

### TUI tests (M4)

**T171: snapshot tests.** The harness runs commands inline and delivers the ones that block on `flush`. Golden files are ANSI-stripped screens at 80x24 and 120x40, and every line must be exactly as wide as the screen, with as many lines as it is tall; `-update` rewrites them.

**T172: acceptance.** `TestAcceptance` is the M4 exit script: 80x24, keys only, the default tabs and review, a real session on an emulator seeded from the demo backup. Slot 3 becomes Play/Pause, DPI stage 2 becomes 1600, and `U` reverts the last apply; each run must end complete, every op verified in the journal, and the emulated flash must hold what the last run left.

### Macros (M5)

**T173: the Macros tab.** It sits between DPI and Backup in the default tabs (Buttons, DPI, Macros, Backup, Info, Log). Three lists: on the mouse (each macro slot that a type-6 binding may run by either rule of T27, with a pending `SetMacro` in place of what it replaces), valid bodies in the loaded image that no binding runs, and the library. Unbound bodies show only when the image holds them (a full backup, or an emulator seeded from one); there is no scan of the 16 macro headers yet.

**T174: binding.** Binding stages one `mouse.SetMacro` under `SlotKey(slot)`, the key the Buttons tab uses, so a slot has one pending edit. Body before binding and the two-phase rebind come from `plan.New`, and the write happens only through the review (T148). Before staging, the whole 384-byte macro slot is read through `Session.Read`, so the new body and the old record it replaces are both known; the read runs again when the mouse becomes Ready or a load drops those bytes, and previews treat unread bytes as 0xFF. The binder starts on the first unguarded button other than Left and Right. Amended by T234.

**T175: macros that share a name.** Binding a macro whose name another button's macro already has offers (y/n) to give the same events to every button that runs a macro of that name from its own slot, each keeping its repeat mode. Any other macro with the same name must be renamed first.

**T176: names.** The sanitiser is our own version of the web app's rule: it drops ASCII punctuation and symbols, the CJK punctuation marks, all whitespace and U+FEFF, then cuts whole characters until the name fits 30 bytes; arcctl also drops control and format characters and invalid UTF-8. It runs when a name is typed, pasted or committed. Names loaded from the mouse or a file stay as they are until edited; binding or saving one the sanitiser would change is refused ("rename it in the editor"), and an import renames it, leaving the macro out when nothing is left. Display escapes control and format characters, invalid bytes and U+2028/2029 as `\xNN` or `\uNNNN`, and a backslash as `\\`.

**T177: the editor.** Repeat modes follow `keys.RepeatOptions` (254, 255, 253, then ×N) with the labels of `cycleText` and, for 253 to 255, whose labels H5 has yet to confirm, the raw code as well; the ×N count is kept while another mode is chosen. Counts 1 to 250 and delays 10 to 65535 ms are clamped with a notice. An event is inserted after the selected one, or at the end when none is selected, and gets the 10 ms minimum delay; a tap or a click inserts a press and its release and is refused past 70 events, which avoids the web app's 71-event overflow. Delays under 10 ms, which only a macro from elsewhere can hold, are flagged. Event numbers in refusals, macro and shortcut alike, start at 1.

**T178: tidy.** `t` pairs each press with the next release of the same key, flags presses repeated before their release, releases without a press and presses never released, and offers to append the missing releases in reverse order; it also raises delays under 10 ms to 10 ms. Its message names what it found.

**T179: event keys.** No recorder (D3). The key list is the modifier keys, the composer's keys (ContextMenu included) and the five mouse buttons.

**T180: library file.** `{"format":"arcctl-macros/1","macros":[{"name","cycle","events":[{"action":"press|release","kind":"modifier|key|mouse|menu","value","delay_ms"}]}]}` in `<data>/macros.json`. Decoding is strict: unknown fields, duplicate names and data after the library are refused, and so is a file over 4 MiB (`library.MaxFile`); a missing file is an empty library. Saving refuses a library that would not load again (`library.ErrTooLarge`), and the tab keeps its current library.

**T181: saving, import and export.** The library is saved atomically with mode 0600 through `backup.WriteFile`, in the background and in order (a save older than the last one written is skipped), and never after a failed load, so a file arcctl could not read is not overwritten. Import merges by name: identical entries are skipped, and an entry whose name is taken with other events is left out and named. Export writes a new file only (`backup.WriteNew`); the prompt starts at `~/arcctl-macros-<date>.json`, numbered when that name is taken. Amended by T236 and T237.

**T182: macro keys.** The list keys avoid the shell's: `B` binds (`b` is the shell's backup), `s` saves to the library, `x` deletes a library macro, `d` drops a pending edit, `i` and `e` import and export. In the editor, `b` binds, `J`/`K` or shift+↑/↓ move an event, and `?` lists every key, as it does in the key picker and the binder.

### Backups, restore and `.bin` import (M5)

**T183: `.bin` trailer.** `ParseBin` needs the vendor text, then a device type and a sensor that are printable ASCII padded with zero bytes. The type must be "mouse" and the sensor must be in the catalog; anything else is `ErrNotBin` or `ErrUnsupported`.

**T184: what a `.bin` captured.** Only records that are not all 0xFF: each record of the settings page and of the extended block (`backup.SettingsRecords`, the decoder's fields and the runs between them), and the body in the own slot of each button bound as type 5 or 6, as far as the web app read it. A shortcut keeps the record its header declares, or the first 10 bytes when the count is invalid. A macro keeps its name block (10 bytes, or the length byte and name when longer) and its events (from the count on, 10 bytes or the declared events and checksum); the unread rest of the name field is filled with 0xFF only when that makes a valid macro. A binding's second byte never captures another slot.

**T185: `.bin` clamps.** Only for a `.bin` source: the stage count to the model's stages, the current stage to the last of them and, when the connection type is known, the report rate to what the connection carries, with the main rate encoding (2000, 4000 and 8000 Hz as 16, 32 and 64). Each clamped pair gets its complement recomputed; a pair that does not decode is left alone. The web app's z2 logic is not copied.

**T186: restore as a per-record diff.** `backup.PlanRestore` compares every settings field, the 16 bindings and all 32 body slots of the source with the mouse and gives each differing record a fate: write, unknown, not captured, not written or not read. Only records to write reach `plan.New`, with the model's normal layout; the rest are listed with their reason ("arcctl does not write it yet" where it applies; the CLI adds "(--include-unknown lists it)" for unknown records). `backup.RestoreReads` lists what to read from the mouse first, in two rounds: the body headers, then the records they declare. Amended by T227.

**T187: restore tiers.** A new feature, `backup.restore` (Untested until H6), covers restores: each restored record takes the lower of its own feature's tier and the restore's, so a restore needs `write untested` until H6 records it. Hidden settings go through the planner's own setting edit (`mouse.SetSetting`) and its D5 check. The report rate and the DPI colours have no feature and are not written, nor are DPI stages past the model's count. `backup.RestoreLeavesOut` names what a restore never writes back on a model.

**T188: restored bodies.** A body is written over the longer of the source's record and the mouse's, clipped to what the source captured, so a full read afterwards equals the source; a slot the source holds empty empties the mouse's record. Macro delays under 10 ms in the source are written as they are.

**T189: refusals before planning.** A pass refuses the records the planner would reject, each with its reason: a binding to a body that cannot be written or is invalid, emptying a body that stays bound, a rewrite whose disable-and-rebind would fail, removing the last Left Click (I9), and a current stage past the count. Amended by T229.

**T190: which mouse and profile (I11).** The source must have the mouse's identity key and, when both hold one, the same address, even while the key leaves the address out (T24); the refusal never prints an address. `--other-device` allows another mouse of the same model and sensor only. The profile must match, or be unknown on both sides, unless `--other-profile` is given. A `.bin` records neither, so it always needs `--other-device`, and `--other-profile` on a mouse with profiles. A backup made by the emulator or a replay never goes to a real mouse.

**T191: `--include-unknown`.** It asks only for unmapped settings-page records that split into checksummed records; bytes never captured and invalid records are never included. The executor and journal recovery accept only records in `mouse.Layout`, so this build lists what the flag would add and refuses to write it. Replaced by T226 to T228.

**T192: `arcctl restore`.** `restore <backup|.bin> [--include-unknown] [--other-device] [--other-profile] [--yes]` plus the global `--dry-run`: it reads what the plan needs, prints the preview (every record with its fate, the clamps, the notes), checks the flags and asks. `--yes` skips only the y/n question; a plan with untested records still needs its phrase typed (`Env.Stdin`). The write goes through the session's `Apply`; the command then waits for the reload and plans again, which must come out at 0 writes, or it exits 7. `--replay` is refused; with `--emulate` the journal and backups go to a temporary folder (T219).

**T193: the Backup tab.** It lists the backups of the loaded mouse through `backup.Store`, newest first, with date, kind (full, loaded, partial), firmware, profile and label, re-reading the folder on a device change, after a write, after a backup, and when the list is 2 s old. The detail pane adds the decoded summary in the Buttons tab's words ("Button: action; …"), the DPI stages, the bodies and the invalid fields. `enter` shows the whole decoded backup, `d` diffs it with the mouse, `w` restores it, `f` takes a full backup and `X` opens the factory reset. There is no `.bin` import or export and no `--other-device` or `--other-profile` in the TUI yet.

**T194: restore in the TUI.** `w` opens the existing review in a restore mode: the plan first, then the records the restore leaves alone with their reasons, then the usual checks and phrase. The restore plans again whenever the image changes, over the loaded image plus the bytes the tab read, and the preflight's re-read catches any that went stale. A restore does not clear the staged edits.

### Factory reset (M5)

**T195: the gate in the guard (amends T46 and T93).** `enabled` stays ReadOnly and Edit. A new `gated` table maps Reset to the feature `device.reset`, stage H7 and cmd 9. `Guard.Set` grants a gated policy only with exactly one `hidio.Grant` (model key, firmware, records) whose records hold that stage for that model and firmware, and the policy then lets one cmd 9 through; a second one is refused even while the policy is still Reset.

**T196: the records that open it.** A release build reads only the records compiled from `verified.json`. Tests give the session other records through `session.SetVerified`, which lives in `export_test.go`, so no release code can reach it. `safety.ResetGate` is the first check of `Session.Reset` and `Session.PreflightReset`, before any I/O. The TUI shows the same gate from its own list of records, and the session enforces it. `device.reset` has base tier Off, reports Verified once H7 covers the firmware, and no plan ever writes it.

**T197: order of a reset.** The gate; the preflight with fresh answers (handshake, cmd 3, cmd 14, and a fresh cmd 18 that must match the loaded firmware; other clients, lock, console, a clean journal and the phrase `reset`); a full backup read afresh (all of 0..6986 and 9504..9759), saved as "auto before reset", which must be complete; the preflight again; the journal entry; the guard to Reset; one transaction of one try with a 3 s wait (`Timing.ResetReply`); the guard back to ReadOnly; a full read, the diff against the backup, then a normal load. Nothing else is sent (D4).

**T198: never resent (amends T49).** The reset packet goes out through `hidio.Raw.WriteRawOnce`, which never retries, so a report the OS refused after the device took it is not sent again; usbhid, hidapi, Pipe, Replay, the recorder and hwtest's tap implement it. The emulator's `emu.Taken` fault (the device takes the packet, then the OS reports an error) covers it. Cancel or Abort before the packet sends nothing; after it they only stop the wait for the mouse. A cmd-9 reply up to 30 s after the reset counts as late, not foreign.

**T199: the journal of a reset.** A run of kind "reset" with no ops: the run entry names the backup and the packet, the "sending" entry is fsynced before the packet, then the reply, then an end entry with the verdict and the changed and unread byte ranges. A reset run is never open in the recovery sense, and once "sending" is on disk it ends `Status.Last`, so no revert crosses it. A reset with no end entry is unchecked (`safety.Status.Unsettled`): writes and resets are refused while one exists, the next session that loads the mouse reads it again, compares it with the backup and journals the verdict, `journal status` lists it and exits 7, and `journal recover` reports it once checked.

**T200: the verdict.** It comes from the byte diff against the backup, not from the reply: changed, unchanged or unchecked. A NAK or no reply is a reply, not an error. A failed or cancelled re-read gives unchecked with `ErrResetUnchecked`. After the packet the session drops the dry-run overlay and its I1 backup records.

**T201: a dry run of a reset.** It checks the gate and the preflight (without the phrase, other clients or the journal) and returns the packet, with no backup and no journal.

**T202: the reset in the TUI.** `X` on the Backup tab (R is the shell's recovery key and f the full backup). While the gate is closed it opens a dialog that says why and what to do instead; otherwise a reset review in `backup_reset.go` that mirrors the review's steps, lists what a restore cannot put back (`backup.RestoreLeavesOut`), asks for `reset` and starts the reset once. The shell asks before quitting while a reset runs (a second ctrl+c still quits), and shows a notice with the backup's path when a reset is unchecked. The reset does not run through the shell's write path yet, so the Log tab shows no reset run. Amended by T235.

### Hardware stages (M5)

**T203: custom steps.** H5 to H9 run steps of their own code. Their plans are dry-run in the preview, in order, and count towards the stage's flags and phrase; writes planned while a step runs add their tiers. Every plan made during a stage also passes the slots 0 and 1 guard.

**T204: the scratch area.** H5 and H9 write the slot-15 macro area and refuse to start while any binding runs macro 15.

**T205: checkpoints.** A stage that may stop past a point it must never repeat writes `<Logs>/hwtest-<stage>-checkpoint.json`, synced, with a kind: drill (H5) or reset (H7). The next run lays out the same steps from a fresh backup, and their fingerprint must match.

**T206: H5's torn-write drill.** It requires `--debug-abort-after-chunk n` (1 to 10), which applies only to the drill's 11-chunk, 103-byte write. Before the process ends the drill writes its checkpoint. The next `hwtest --stage H5` finds the torn record in the journal, recovers it to the old bytes after a dry run that must match the preview, reads it back, then finishes the stage, recording one log entry with both transcripts. A failure before the recovery keeps the checkpoint and records nothing, and `--dry-run` refuses while a checkpoint exists. In a rehearsal the abort acts as a crash inside the process and the stage resumes there through the same code, because the emulated flash would die with the process.

**T207: H5's repeat modes.** A macro that types "h5" is bound to Forward through the release planner with repeat modes 1, 253, 254 and 255, each applied, tried and reverted. For each, the user focuses a document, tries it, stops it and presses Enter, and only then answers the y/n questions; every fallback says to switch the mouse off and on or replug the receiver. Mode 1 must type once; the other three are recorded as observations next to their labels.

**T208: H6.** Five changes of different kinds in one apply: the current stage, DPI stage 1, slot 3 to a scroll function, slot 4's shortcut to Ctrl+F13 (two-phase) and a macro bound to slot 5. The restore plan must list exactly those records and its dry run must match the preview; after the restore, planning again must write nothing, and a full read from a new session must equal the backup byte for byte. The buttons' old actions are shown on the terminal only, never in the log.

**T209: H7's reset.** Four preparation questions, then the phrases `write experimental` and `reset`, both named in the preview. A preflight with no ops, an online check and a check against the `wire.Reset` policy, then cmd 9 once through the raw path with `WriteRawOnce`: one exchange of up to 3 s plus the listening window, never resent. Late cmd-9 replies are kept from every session, new ones included, for 30 s. The stage judges the reset from a full read in a new session, compared with the backup record by record, and records scope, cmd 23 before and after, pairing and identity, with no bytes in the log. The reset is not journaled, like the identity writes; the stage opens a new session afterwards.

**T210: H7 after a stop.** A reset checkpoint, naming the backup B1 and the cmd-23 reply, is synced before the packet. While it exists H7 never resets: the next run asks whether to go on and whether the mouse was paired again, reads the mouse, compares it with B1 and writes B1 back. The checkpoint goes once the write-back verifies, or when the reset changed nothing. H7 takes the identity and profile from B1's file; a different identity, address or profile afterwards is a finding and a failed step, writing back then needs a yes to `h7.write-back-other`, and the stage cannot pass.

**T211: H7's write-back.** The release restore plan, plus the records it refuses that a plan may write (the report rate, the DPI colours, hidden slots, hidden settings) at the Experimental tier, asked about before they are written (D5, D10); declined records are findings, not failures. It never touches slots 0 and 1, frozen or unmapped records; records it cannot write are listed for `--include-unknown`. After a stop it retries up to 3 times from a new session, never resetting again. Accepted by the maintainer (T221); amended by T231.

**T212: H7's pass.** H7 passes, and promotes `device.reset` for the exact firmware, only when the reset changed something, the mouse stayed paired, every writable record was verified and the user answered the physical question yes.

**T213: promotions.** A stage promotes only features that `mouse.Features()` lists, and a stage whose feature is not listed refuses to start (`hwtest.ErrUnknownFeature`). H5 promotes `button.macro`, H6 `backup.restore` and H7 `device.reset`. Amended by T224.

**T214: H9.** Each drill's write pauses after each chunk until it sees its event, so the user has time to act: 1 s for the screen lock, 10 s for sleep, 1 s for the unplug and 5 s per DPI record. A drill counts as observed when a lock or sleep paused the write after at least one chunk and it restarted and verified, when the unplug stopped it after at least one chunk and it recovered after the replug, and when the DPI press stopped the run before its current-stage write, which sets the current stage one below its value so a single press can never match the plan. The DPI drill uses a visible button that already runs a DPI function, or binds the catalog's DPI Cycle button to DPI cycle for the drill and reverts it after; with neither it is skipped with a finding. Each drill gets at most 3 tries; then open runs are recovered and the records put back from the backup. Putting records back grows a body write to the longer of the backup's and the mouse's declared record.

**T215: the debug abort elsewhere.** In a stage without a drill, `--debug-abort-after-chunk` ends the run the way the real exit would (`hwtest.ErrEnded`, nothing recorded).

**T216: build checks (amends T117).** `scripts/release-check.sh` also looks for `.hwReset` and `.hwCheckpoint`. The wiring and layering tests list packages with the test binary's build tags, and under `hwtest` allow only `hidio.OpenRaw` and `emu.(*Bus).OpenRaw` as unguarded opens.

### Shell and CLI (M5)

**T217: q in dialogs (amends T168).** q closes every dialog that is not running a write or a reset, the review included; only a running write or reset hands q to the shell's quit prompt. Accepted by the maintainer (T221).

**T218: notices with paths.** A notice about a file puts the cause first and keeps the file name when the path is cut; file errors keep their cause.

**T219: the emulator's folder (amends T139).** The temporary folder that holds an emulated session's journal, backups and macro library is removed when the TUI or `arcctl restore` ends, and the note that names it says so. A hardware-test rehearsal keeps its folder (T116), since it holds the rehearsal's records.

**T220: CLI golden files.** Golden files accept line wrapping that depends on the length of the test's temporary path, so the CLI tests pass from any checkout location.

### M5 gate follow-ups

**T221: sign-offs.** The maintainer accepted T211 (H7 asks before writing back the records the release restore leaves out) and T217 (q closes dialogs).

**T222: H4's plan.** H4 plans every change with the release planner (`mouse.PlanEdits` with `SetShortcut` or `SetMedia`), as the Buttons tab does; only slot 2's identity write goes through the hwtest raw path (D10). That write covers the record slot 2's body declares (Cmd+V: 14 bytes in 2 chunks), at the tier `PlanEdits` would give that body: shortcut or media, plus one feature per web-compat reason. H4 refuses to start unless slots 2 and 4 are bound to valid shortcut bodies.

**T223: H4's two-phase case.** Slot 4's shortcut gains Left Shift before its key (Cmd+Tab becomes Cmd+Shift+Tab), or loses it when the shortcut already holds it. The builder refuses unless both the change and its revert run neutralise, body, bind.

**T224: extra cases (amends T213).** An extra case is a question that any answer passes (`stageDef.extras`, `step.extra`): a yes adds its feature to what the stage promotes when it passes, a no is logged as "not promoted … stays Untested", and a failed stage promotes nothing, extras included. H4 promotes `button.shortcut` and `button.media`, plus `button.shortcut.right-modifier` when RCmd+Tab switches apps as Cmd+Tab does, and `button.shortcut.menu` when Karabiner-EventViewer lists the Menu key or a context menu opens.

**T225: record privacy (amends T115).** Questions name only the stage's own keys; what slots 2, 3 and 4 ran, and slot 4's Shift version, are asides on the terminal that stay out of the records. No record shows bytes of the shortcut or macro area: in every stage, step details, transcript notes and error texts show packets as the redacted transcript does (`xx`) and body bytes only as a count. Settings bytes still show.

**T226: the captured phase (amends T25, T26 and T28).** Bytes written back as a backup captured them are a plan phase of their own, `plan.Captured` (journaled as "captured"), after every record write; `plan.Change.Captured` asks for it. `plan.Layout.Capturable` says where such an op may go: before the first body table (0 to 255 on the EM11), clear of the frozen records and the binding and body tables, and either a whole record of the layout or clear of every record. There is no checksum rule, and `Validate` refuses a captured op anywhere else or at any tier but Experimental, so the executor, journal recovery and revert take these writes with the unchanged `mouse.Layout`. A journal holding a "captured" op cannot be read by an older build.

**T227: what `--include-unknown` writes (amends T186).** The records a restore marks unknown and `Capturable` accepts: unmapped fields in 0 to 255, other models' fields there (AngleTune at 189 and the like) and layout records whose source bytes are not valid (the report rate, the colours, DPI stages). Each is written whole as one op, only when it differs from the mouse. Never: KeyOperation @8, bindings, bodies, the extended block, anything captured only in part, and the hidden settings of T230. Without the flag the plan has no captured op.

**T228: flags and phrase (amends T96).** A captured write needs `--include-unknown`, `--experimental` (the Experimental tier's rule) and a typed phrase that names every captured extent, such as `write experimental and captured 6+2 84+12 187+2` (`safety.ConfirmPhrase`). The preflight checks the phrase for apply and revert; recovery skips the tier checks, as before. In the TUI, `u` in the Backup tab's restore diff includes or leaves out these records before `w` opens the review, which names each record and extent.

**T229: the stage pair (amends T189).** A restore refuses a count or current-stage write that is not captured when the mouse would hold current ≥ count before the captured bytes, written last, go back, as well as after them. Such a restore needs a second run, and its refusal says so.

**T230: D5 and `--include-unknown`.** It never writes a hidden setting of the layout whose feature has no `verified.json` record, even when the source's bytes are invalid; the reason ends "D5 keeps setting.X read-only until its own H8 test is recorded". Unmapped bytes, other models' fields, the report rate and the colours stay eligible. Pending the maintainer's sign-off.

**T231: H7's write-back (amends T211).** H7 plans with `IncludeUnknown` only to list the captured records for a new yes/no question, `h7.write-back-unknown`, asked after `h7.write-back-extra` and naming each record and extent; it writes a plan made without the flag unless the answer is yes. The invalid hidden settings T230 holds back join that question as captured bytes (D10). It is a yes/no rather than a typed phrase, since H7 fills the phrase itself. Declined records are left at the user's choice, not failures. The extra path never adds the stage count or current stage, which the release restore refuses only to keep current below count.

**T232: review words for captured blocks.** The review shows only the hex of a record whose bytes are known but decoded by no field, such as the unmapped block at 84+12; "not read" is kept for bytes that are unread.

**T233: the Buttons picker's Macro group (amends T157).** A fifth group after Combo Key: "New macro…", then the macros on the mouse (pending ones included), the valid unbound bodies and the library, each once by name, with events and repeat mode. A choice stages `SetMacro` for the button's own slot through the Macros tab's bind flow (T175's question, the sanitiser, the Left Click guard, the slot read). "New macro…" swaps the picker for the Macros tab's editor, whose binder starts on that button. The picker opens on the macro the button runs from its own slot.

**T234: pending macros on the Buttons tab (amends T174).** The preview takes an unread macro slot as erased flash instead of refusing. The Tier column shows "reading…" while the slot is read and "not read" otherwise, and the detail says when it is read. With a Macros tab present, only that tab reads macro slots.

**T235: the factory reset in the Log (amends T202).** The Log takes the reset dialog's result like a write's end (verdict, changed ranges, reply, and the backup under "This session"), names the reset's jobs, lists unchecked resets from the journal, and says that no revert goes past a reset of this session. The reset still runs in its own dialog, not the shell's write path.

**T236: library saves on quit (amends T181).** Saves always write the newest library, one write at a time, and a failed save is retried by the next one. `tui.Run` then waits up to 5 s (`saveWait`) for every tab's unfinished saves and runs any that never started. A failed or unfinished save is Run's error and names the file; after ctrl+c or SIGINT that error is returned instead of "aborted".

**T237: `internal/atomicfile` (amends T181).** `WriteFile` and `WriteNew` moved there unchanged from `backup` (temp file, sync, rename or link, folder sync; files 0600, folders 0700). `backup` and `library` use it, so `library` no longer imports `backup`; `backup.WriteFile` and `backup.WriteNew` stay as wrappers for the CLI and hwtest callers.
