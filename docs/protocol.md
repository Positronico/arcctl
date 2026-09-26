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

- **Checksum.** 8 (the report ID) plus the sum of all 16 bytes must be 0x55 modulo 256, so byte 15 is `(0x55 − 8 − Σ bytes 0..14) & 0xFF` [WE]. H0 checks that replies follow the rule too; the one captured reply does [TS].
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
| 29 | receiver version | no data | shown as `v%d.%02x`; a NAK means v1.0 | [TS](v) |

The reply layouts of cmds 1, 3, 4, 14, 18, 23 and 29 are used from M2 on.

**Never sent.** Cmds 2, 20, 21, 24, 25, 44, 45, 46–50 and 176–183, report 13 and any feature report [TS](v) [GO](v) [GM]. Cmd 10 only arrives as a push. Pairing (cmds 5 and 6, D6) and cmd 15 (set profile) are not sent in v1. No policy allows any of them.

**Policies.** A session holds one policy at a time, and `Policy.Check` refuses anything else before any I/O. The policies are least privilege, not a chain:

| Policy | Mouse | Keyboard |
|---|---|---|
| ReadOnly (the zero value) | 1, 3, 4, 8, 14, 18, 23, 29 | the same |
| Edit | the reads plus 7 | the reads only (D7) |
| Reset | the reads plus 9 | the reads only |
| Experimental | the reads plus 7 and 22 | the reads only |

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
