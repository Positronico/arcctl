# Protocol notes

What arcctl knows about the HID protocol that ProtoArc receivers, mice and keyboards speak, written in our own words. Each fact names the evidence tag it rests on and the code that implements it. A `(v)` after a tag means a second, independent research pass confirmed the fact.

## Evidence tags

Facts here carry a tag for the research area they came from. The research itself is not part of this repo.

| Tag | Area |
|---|---|
| TS | transport and session |
| WE | write encodings |
| UI | web app user interface inventory |
| GO | Go HID libraries |
| MC | model catalog |
| GM | gaming mouse features |
| KP | keyboard protocol |
| KU | keyboard user interface |
| dump | a settings dump read from the maintainer's own mouse |
| H0 | the read-only hardware stage on the maintainer's EM11 Pro, mouse firmware v1.26 (`docs/hardware-tests.md`) |

## Framing

Code: `internal/wire` (`Packet`, `Build`, `BuildRead`, `Match`, `Policy.Check`).

Commands travel as 16-byte output reports with report ID 8, and replies come back the same way [TS] [GO](v). The report ID is not one of the 16 bytes.

| Byte | Request | Reply |
|---|---|---|
| 0 | command | the same command |
| 1 | 0 | status: 0 OK, 1 NAK, anything else an error |
| 2–3 | flash address, big-endian (cmds 7 and 8 only, 0 otherwise) | the address echoed (cmds 7 and 8) |
| 4 | target flag (0x80 keyboard, 0x00 mouse) OR data length (low nibble, 0–10); bits 0x70 are 0 | target flag and length |
| 5–14 | data, zero padded | data |
| 15 | checksum | checksum |

- **Checksum.** 8 (the report ID) plus the sum of all 16 bytes must be 0x55 modulo 256, so byte 15 is `(0x55 − 8 − Σ bytes 0..14) & 0xFF` [WE]. Replies follow the rule too: all 3,222 frames the unit sent during H0 did [TS] [H0].
- **Target.** A keyboard session sets 0x80 in byte 4 of every packet; replies are read with `& 0x0F` for the length [KP](v). `Policy.Check` refuses a packet whose flag does not match the session's target.
- **Reads.** A cmd-8 request carries the length it wants (1–10) and no data bytes; the reply carries the bytes.
- **Writes.** A cmd-7 request carries up to 10 bytes. Longer records go out in consecutive 10-byte chunks, each its own write [WE] [UI](v).
- **Reply matching.** A reply answers a request when byte 0 matches; for cmds 7 and 8 the address and the length nibble must match as well. Other commands are matched on the command alone, because the cmd-3 reply carries 4 bytes where the request carries none [TS](v).
- **Strict shape.** `Policy.Check` accepts only packets `Build` could have produced: byte 1 is 0, the reserved bits are clear, nothing past the data, an address only on cmds 7 and 8, a length of at least 1 on cmds 7 and 8, and the per-command request shapes below.

## Commands

Code: `internal/wire/cmd.go`, `internal/wire/policy.go`.

| Cmd | Name | Request | What it does | Tag |
|---|---|---|---|---|
| 1 | handshake | 8 bytes: 4 random, 4 zero | reply bytes 9, 10 and 11 give cid, mid and the connection type | [TS] [GO] |
| 3 | online | no data | answered by the receiver itself; reply byte 5 is the online flag, bytes 6–8 the paired address (reversed) | [TS] [GO](v) |
| 4 | battery | no data | reply byte 5 is the level in %, 6 charging, 7–8 millivolts big-endian | [TS](v) [GM] |
| 7 | write | address, 1–10 bytes | writes flash | [WE] |
| 8 | read | address, length 1–10 | reads flash | [WE] |
| 9 | clear | no data | factory reset (D4: sent once, never resent, disabled until H7) | [WE] [UI](v) |
| 14 | get profile | no data | status 0 means onboard profiles are present | [GM] [UI](v) |
| 18 | firmware version | no data | shown as `v%d.%02x` | [TS](v) |
| 22 | set long range | 10 bytes: `[0 or 1, 0 × 9]` | long-range radio mode; Experimental only | [TS] [MC](v) |
| 23 | get long range | no data | probe | [TS] |
| 29 | receiver version | no data | shown as `v%d.%02x`; a NAK means v1.0. The EM11 Pro's receiver passes it to the mouse: the awake mouse NAKs it, and nothing answers while it sleeps | [TS](v) [H0] |

The reply layouts are in [Replies](#replies) below.

**Never sent.** Cmds 2, 20, 21, 24, 25, 44, 45, 46–50 and 176–183, report 13 and any feature report [TS](v) [GO](v) [GM]. Cmd 10 only arrives as a push. Pairing (cmds 5 and 6, D6) and cmd 15 (set profile) are not sent in v1. No policy allows any of them.

**Policies.** A session holds one policy at a time, and `Policy.Check` refuses anything else before any I/O. The policies are least privilege, not a chain:

| Policy | Mouse | Keyboard |
|---|---|---|
| ReadOnly (the zero value) | 1, 3, 4, 8, 14, 18, 23, 29 | the same |
| Edit | the reads plus 7 | the reads only (D7) |
| Reset | the reads plus 9 | the reads only |
| Experimental | the reads plus 7 and 22 | the reads only |

The M2 build enables only ReadOnly: `hidio.Guard.Set` refuses every other policy.

## Replies

Code: `internal/session` (`connect.go`, `job.go`, `poll.go`, `txn.go`).

Byte positions count from 0 over the 16-byte frame, as in the framing table. H0 checked the layouts on the maintainer's EM11 Pro; what it found is tagged [H0].

| Cmd | Reply | Tag |
|---|---|---|
| 1 | byte 9 cid, 10 mid, 11 connection type (6 is the charging base); cid or mid 0 means no usable mouse. The EM11 Pro sends 8 data bytes: the 4 random bytes back, then cid 0x7B, mid 4, type 0 (wireless 1 kHz) and a zero | [TS] [GO] [H0] |
| 3 | byte 5 is 1 when the mouse is online; bytes 6–8 hold the paired address in reverse order (`33 22 11` is address 11 22 33). Answered by the receiver itself in 2–6 ms, also while the mouse sleeps. On the EM11 Pro the address stayed the same across sleep, wake and a receiver replug | [TS] [GO](v) [H0] |
| 4 | byte 5 level in %, 6 charging (1), 7–8 millivolts big-endian; when byte 9 is 1, byte 10 is the level to show. The EM11 Pro sends only the first 4 bytes, so byte 9 is 0 there. The raw level is shown; the web app's smoothing is not ported | [TS](v) [GM] [H0] |
| 10 | a push, never a reply: 10 data bytes, the two flag bytes first and the rest zero | [TS] [H0] |
| 14 | status 0 means onboard profiles are present, and byte 5 is the active profile; a NAK means none. The EM11 Pro NAKs it | [GM] [UI](v) [H0] |
| 18, 29 | bytes 5 and 6, shown as `v%d.%02x`; a NAK on 29 means receiver v1.0. The EM11 Pro reports mouse v1.26 and NAKs cmd 29 | [TS](v) [H0] |
| 23 | byte 5 is the long-range flag; a NAK means unsupported. The EM11 Pro NAKs it | [TS] [H0] |
| NAK | status 1 with the request's command (and address, for cmds 7 and 8), no data. The NAKs H0 saw (cmds 14, 23 and 29) had length 0, except cmd 14's, which had 1; the length is not checked. NAKs to writes wait for H1 | [TS] [WE] [H0] |

## Transport

Code: `internal/hidio`, `internal/third_party/usbhid`.

- **Interfaces.** A receiver shows up as two HID interfaces with the same VID, PID and report descriptors (0, the boot keyboard, and 1, the boot mouse). Only interface 1 answered cmd 3 in every library tried; interface 0 stays silent [TS] [GO](v). H0 found the same, also with the mouse on its USB-C cable, which adds no interface that answers [H0]. arcctl finds the right one by probing, never by position.
- **Vendor channel.** Before anything is sent, the interface's report descriptor must declare output report 8, 16 bytes, in an application collection on usage page 0xFF02. VID 0x062A is shared with other vendors, so a VID and PID match alone is not enough. Windows gives no descriptor and splits each top-level collection into its own device, so there enumeration keeps only usage page 0xFF02 instead.
- **Shared access.** Interfaces are opened shared, never seized, so other clients (Karabiner's observer, the web app in a browser) can hold the same interface. A second client can see or take replies meant for arcctl; the session watches for that (below).
- **Input.** Replies and pushes arrive as report-8 input frames of 16 bytes. Every other input report (mouse movement, keyboard and consumer reports from the same receiver) only says that the mouse is awake; arcctl keeps no content from them.
- **Write errors.** IOKit errors come back as `IOReturn` codes inside the backends' error text; `hidio.IOReturn` extracts them and `hidio.Classify` groups them. 0xE00002BC (general error) is retried up to 3 times; 0xE00002D6 is a write timeout; 0xE00002E2 means the screen is locked or Secure Input is on, and it is also what opening the receiver returns once the Input Monitoring grant is revoked, so the permission check comes before any open [H0]; 0xE00002C1 is a missing permission; 0xE00002C5 means another process seized the device; 0xE00002C0 and similar codes mean the device is gone. A write that has not completed after 2 s marks the handle stalled.

## Session and transactions

Code: `internal/session`.

**Connect sequence.**
1. On macOS, check Input Monitoring for the app that runs arcctl; a denial means NeedsPermission.
2. Enumerate every catalog VID and PID pair.
3. Probe every candidate with cmd 3 (3 tries of 150 ms). Silent interfaces and NAKs are closed. A keyboard PID is probed with the 0x80 flag first and again without it when that gets no reply [KP](v). Several answering devices (a second receiver, or a mouse on a cable) need a choice.
4. Ask the receiver for its version (cmd 29), then poll cmd 3 until the mouse is online. The receiver answers cmd 3 itself while the mouse sleeps [TS](v) [H0]. The EM11 Pro's receiver passes cmd 29 to the mouse, which leaves it unanswered while it sleeps [H0], so a sleeping mouse gets one try, and the question is asked once more when it wakes, before the handshake.
5. Handshake (cmd 1: 4 random bytes and 4 zero bytes). The (cid, mid) pair picks the model; an unknown pair or the charging base is shown but not read.
6. Load (see Reads in the flash layout above), then cmds 14, 18 and 4, plus 23 when the connection is wireless.
7. On every wake, handshake again: a different paired mouse starts a fresh load.

cmd 2, cmds 21, 25 and 45 and report 13, which the web app sends while connecting, are skipped [TS].

**Transactions.** One request at a time. Reports that arrived before the request are handled first (drain before send). Each request gets 5 tries of 200 ms; reports that do not answer it are handled on the side and do not use up a try, and reports already queued when a try runs out are still read before the try counts as unanswered. A reply answers when its command matches, and for cmds 7 and 8 its address and length as well; status 1 ends the transaction as a NAK [TS](v) [WE](v). Inbound checksums are counted but not enforced; every frame of the H0 run was valid [H0].

**Latency and loss.** Replies that cross the radio took about 12 ms on the EM11 Pro (2,810 reads: p50 12.1 ms, p99 43.7 ms, max 56 ms), and the receiver's own cmd-3 replies 2–6 ms (max 10 ms); a full backup took 9.9 s [H0]. The receiver serves one radio request at a time: each of the 6 times a second request reached it before the reply to the first, the first reply never came and the second did. With one client talking, none of 2,565 tries went unanswered [H0].

**Other frames.**

| Frame | Treatment |
|---|---|
| cmd 10 (StatusChanged) | a push: re-read what it names |
| matches a request answered in the last 2 s | a duplicate; logged |
| matches a request that timed out in the last 2 s | a late reply; logged |
| cmd 3 that nobody asked for | logged; an online flag counts as a wake hint. The EM11 Pro never sent one: no frame marked sleep, wake, a screen lock or a replug [H0] |
| anything else | foreign: another client is talking to the receiver, and the session enters Conflict |

**Pushes.** A StatusChanged push carries two flag bytes (5 and 6). For a mouse, each flag names a range to read again [TS] [GM]:

| Byte | Flag | Re-read |
|---|---|---|
| 5 | 0x01 | 4+2 (current stage) |
| 5 | 0x02 | 0+2 (report rate) |
| 5 | 0x04 | the onboard profile changed: cmd 14 and a full reload |
| 5 | 0x08 | 76+8 (DPI light) |
| 5 | 0x20 | 160+7 (light effect) |
| 5 | 0x40 | battery (cmd 4) |
| 6 | 0x01 | 10+2 |
| 6 | 0x02 | 169+2 |
| 6 | 0x04 | 171+2 |
| 6 | 0x08 | 233+6 |
| 6 | 0x10 | 225+2 |

Keyboard flags mean other things [KP] and are not acted on yet.

What the EM11 Pro pushed in H0 [H0]: 19 pushes, all 0x40 (battery), about one a second during two steps in which the host was reading and the mouse was probably being moved; none on sleep, wake or a screen lock. Whether a DPI press pushes 0x01 is not known: the unit's DPI button (slot 5) is bound to Cmd+C, so pressing it sent keyboard reports only. Without a push, a DPI press shows up only when the device is read again: a reload, or the re-read of the records a write changes, which the preflight does and which refuses a plan made on the old bytes.

**Offline during reads.** A mouse command that runs out of tries is followed by cmd 3. If the mouse is offline, the read pauses and resumes from the failed chunk once it wakes; if it is online, the chunk gets one more attempt and is then left unknown.

**Polling.** cmd 3 every 1.5 s while offline and every 5 s while ready; cmd 4 every 30 s while ready. A rescan with no receiver starts at 1 s and backs off to 5 s; a locked, seized or unpermitted device is retried from 2 s up to 10 s. The EM11 Pro sleeps about 13 s after its last input; reads keep it awake, cmd 3 does not, since it never reaches the mouse [H0].

**Conflict signals.** Two ways to notice another client [TS](v):
- *Broadcast*: every client sees every reply, so arcctl sees replies to requests it never sent. Such a foreign reply enters Conflict, which the user clears after 10 s without one.
- *Steal*: each reply reaches one client, so arcctl's replies go missing. The receiver answers cmd 3 within milliseconds, so 2 unanswered cmd-3 tries in a row, or a mouse command that goes unanswered 4 times while cmd 3 says online, enter SuspectedConflict. It clears after 3 clean transactions and a client scan that shows nobody else.

The EM11 Pro's receiver broadcasts: with the web app connected, a listener that sent nothing saw 8 replies meant for the browser, the client scan named Google Chrome, and a session next to it entered Conflict [H0]. Two clients that talk at once also lose replies to each other (Latency and loss, above). Both signals disable writes, and the steal thresholds stay for receivers not yet tested. They are set so that a 2.5% loss per try, far above what H0 saw, raises no suspicion: 4 unanswered tries of one command happen once in 2.6 million transactions at that rate, and once in 16 when another client takes half the replies.

## Flash layout

Code: `internal/mouse/zz_offsets.go` (generated from `internal/catalog/facts/offsets.json`), `internal/mouse/layout.go`, `internal/mouse/config.go`, `internal/flash`.

The mouse keeps its settings in a 16 KiB flash image. arcctl uses 0..6987 and preserves 9504..9760 in backups [WE] [GM](v) [KP](v).

**Pairs and records** [WE]:
- A *pair* is `[v, 0x55 − v]`. `ff ff` is erased. `ff 56` holds the value 0xFF with a valid complement, which arcctl calls unset. Any other pair that does not sum to 0x55 is invalid.
- A *record* is a body followed by one checksum byte, chosen so that all bytes sum to 0x55. An all-0xFF record is erased.
- A shortcut or macro slot that is all 0xFF or all zero is empty. The web app's restore leaves zeros [WE](v).

**Mouse map** (addresses in decimal):

| Address | Size | Field | arcctl |
|---|---|---|---|
| 0 | pair | report rate | decoded; written from M6/M7 |
| 2 | pair | stage count, 1–8 | edited |
| 4 | pair | current stage, 0–7 | edited |
| 6 | pair | not mapped | preserved |
| 8 | pair | key operation (left/right behaviour) | read-only (D5) |
| 10 | pair | lift-off distance | Experimental (D5) |
| 12 + 4i | record | DPI of stage i, i = 0–7 | edited |
| 44 + 4i | record | colour of stage i | decoded |
| 76, 78, 80, 82 | pairs | DPI light mode, brightness, speed, on/off | Experimental |
| 84–95 | | not mapped (94 holds a light power-save pair the generated offsets lack) | preserved |
| 96 + 4i | record | key function of button slot i, i = 0–15 | edited |
| 160 | 7-byte record | light effect | decoded; no edit yet |
| 169–185, odd | pairs | debounce, motion sync, sleep, angle snapping, ripple, light off while moving, performance on/off, performance, sensor mode | Experimental |
| 187 | pair | not mapped | preserved |
| 189, 191, 225, 227, 229, 233, 239–250 | pairs | optional features on other models | shown when the pair is valid; never written |
| 235–238 | record | flywheel | never written |
| 256 + 32i | 32 bytes | shortcut body of slot i | edited |
| 768 + 384i | 384 bytes | macro body of slot i | edited |
| 6912–6987 | | sensor 3955 DPI and other optional features | read; never written |

Hidden settings take only the values the web app's own setters write [GM]: lift-off distance and DPI light mode 1–2, DPI light speed 1–5, on/off fields 0–1, DPI light brightness one of the ten table bytes (16, 30, 60, 90, 128, 150, 180, 210, 230, 255), debounce up to the model's maximum, sleep and performance 1, 6, 30, 90 or 180. Brightness level 10 is 0xFF and is stored as `ff 56`.

**Reads.** A working load reads the first 256 bytes and 6912..6987 in 10-byte chunks. For each button bound to a shortcut it then reads the whole 32-byte slot. For each button bound to a macro it reads the header (bytes 0..31 of the slot, the name padding included), then the events from byte 31 on: 5 × count + 2 bytes. arcctl never decodes bytes it did not read, so an unread slot is Unknown, not Empty; the web app decodes its 0xFF fill instead [KP](v).

## Record encodings

Code: `internal/mouse` (`rate.go`, `dpi.go`, `keyfn.go`, `shortcut.go`, `macro.go`), checked against `testdata/vectors.json` and `testdata/oracle/`.

**Report rate** (pair at 0) [WE](v) [MC](v):

| Hz | 125 | 250 | 500 | 1000 | 2000 | 4000 | 8000 |
|---|---|---|---|---|---|---|---|
| Value | 8 | 4 | 2 | 1 | 16 | 32 | 64 |

The highest rate offered depends on handshake byte 11: 0 and 2 → 1000, 1 → 4000, 4 → 2000, 3 and 5 → 8000; any other type gets no rate options. Sensor 3212 offers 125 Hz only.

**DPI** (record at 12 + 4i) `[x, y, flags, cks]` [WE] [MC](v):
- Bytes 0 and 1 are the low 8 bits of the X and Y raw codes.
- Flags: bits 0–1 are the X range flags, bits 2–3 the X raw bits 8–9, bits 4–5 the Y range flags, bits 6–7 the Y raw bits 8–9.
- Each set range flag doubles the DPI.
- For a sensor with a value table, the raw code is looked up in the table, and table index k means `min + k × step` of the first range. For a sensor without one, the DPI is `(raw + 1) × step`.
- arcctl writes X equal to Y with the range's own flag byte (0x11 above 4000 on sensors 3104 and 3212) and leaves an unchanged asymmetric stage alone. A code that decodes to no legal DPI of the sensor is invalid; the web app shows 4100 instead.

**Colour** (record at 44 + 4i): `[r, g, b, cks]`.

**Key function** (record at 96 + 4i) `[type, param high, param low, cks]` [WE] [GM](v):

| Type | Meaning | Param |
|---|---|---|
| 0 | disabled | 0 |
| 1 | mouse button | 0x0100 left, 0x0200 right, 0x0400 middle, 0x0800 back, 0x1000 forward |
| 2 | DPI | 0x0100 cycle, 0x0200 up, 0x0300 down |
| 3 | scroll left/right | 0x0100, 0x0200 |
| 4 | fire key | interval 10–255 ms in the high byte, times 0–3 in the low byte |
| 5 | shortcut or media key (body in shortcut slot i) | 0 |
| 6 | macro | macro slot in the high byte, cycle in the low byte |
| 7 | report-rate switch | 0 |
| 8 | drag scroll | 0x0500 |
| 9 | profile switch | 0 |
| 10 | DPI lock | the raw DPI code, little-endian; first range only |
| 11 | scroll up/down | 0x0100, 0x0200 |

The macro cycle is a repeat count 1–250, or 253 (until pressed again), 254 (until released) or 255 (until any key). Left Click is `01 01 00 53`; the planner refuses a plan that would reassign the last button still on Left Click.

**Shortcut** (body at 256 + 32i) `[2n, n presses, n releases, cks]` [WE] [UI]:
- A press is `[0x80 | kind, value low, value high]`; the releases repeat the presses in reverse order with 0x40.
- n is 1–5. Kinds: 0 modifier (the value is one bit: 0x01 left Ctrl, 0x02 left Shift, 0x04 left Alt, 0x08 left Cmd/Win, 0x10–0x80 the right-hand ones), 1 HID keyboard usage, 2 consumer usage (16-bit), 7 the context-menu key.
- The keys stay in the order they are pressed, which is the order the web app's presets use.
- A media key is a one-key kind-2 shortcut, `[2, 0x82, lo, hi, 0x42, lo, hi, cks]`, bound with `[5, 0, 0, 0x50]`.

**Macro** (body at 768 + 384i) [WE](v) [GM]:
- Byte 0 is the name length (1–30). The name follows in UTF-8, padded with 0xFF up to byte 30.
- Byte 31 is the event count (1–70).
- Each event is `[0x80 | kind (press) or 0x40 | kind (release), value low, value high, delay high, delay low]`, the delay in ms. Kinds are 0, 1 and 7 as above, plus 4 for mouse buttons (0x01 left, 0x02 right, 0x04 middle, 0x08 back, 0x10 forward). Consumer usages are not allowed in macros.
- The checksum byte follows the last event and covers bytes 31 to the end, so 33 + 5 × count ≤ 384.
- arcctl writes delays of at least 10 ms but decodes any 16-bit delay.
- The binding is `[6, slot, cycle, cks]`.

## Keyboards

TODO (M8): key slots, the 0x80 flag, lighting, settings and CRC entries.
