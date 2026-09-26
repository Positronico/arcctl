# Oracle vectors

The JSON files in this folder are differential test vectors. A harness in the private mirror repo (`tools/oracle/`) loads the vendor web app's own encoder and decoder functions at runtime, feeds them seeded pseudo-random inputs from the valid domain, and records the bytes they would write to the mouse. Each vector holds only input values and the resulting bytes, so arcctl's Go codecs can be checked against the web app without any vendor file in this repo.

The files are committed, and arcctl's tests read them without the mirror. Regenerating them needs the mirror checkout:

```
node tools/oracle/run.mjs --seed 1 --cases 200 --out ../arcctl                            # from the mirror root
node tools/oracle/run.mjs --seed 1 --cases 64 --areas macro,macro_decode --out ../arcctl  # the macro areas are kept small
node tools/oracle/run.mjs --verify --out ../arcctl                                         # regenerate in memory and compare
```

`--verify` reuses the seed and case count recorded in each file, so it needs no other flags. With `ARCCTL_MIRROR` set, `make oracle` in this repo runs `--verify`, then regenerates every file with its recorded seed and case count into a temp dir and diffs the result against this folder.

Do not edit the files by hand. A rerun with the same seed and case count produces the same bytes.

## File format

```json
{
  "version": 1,
  "generator": "protoarc-hub-mirror tools/oracle",
  "seed": 1,
  "area": "dpi_value",
  "cases": [
    {"in": {"sensor": "3104", "stage": 0, "dpi": 800},
     "writes": [{"addr": 12, "hex": "15 15 00 2b"}],
     "packets": ["07 00 00 0c 04 15 15 00 2b 00 00 00 00 00 00 e1"]}
  ]
}
```

- `version` is 1. `seed` is the run's seed. `area` names the codec, and each file holds one area.
- Each case is on its own line. The `in` fields are plain JSON values (numbers, strings, booleans, arrays); their meaning depends on the area (below).
- Byte strings are lowercase hex pairs separated by single spaces, as in `testdata/vectors.json`. Addresses are decimal flash offsets.

**Encoder areas.** A case has `in`, `writes` and usually `packets`.
- `writes` lists the flash-level writes: the start address and the exact data bytes. Every encoder case has exactly one write. Multi-byte records already include their own checksum byte.
- `packets` lists the full 16-byte output reports (report ID 8, which is not included) that carry the write: `[07, 00, addrHi, addrLo, len, data (zero padded to 10), cks]`, one packet per 10-byte chunk. The field is optional; `macro` has it only on its first six cases (see below).

**Decoder areas.** A case has `in` and `out` instead.
- `in` is `{"slot": n, "hex": "..."}`: the bytes found at the start of that slot. The rest of the slot is taken to be 0xFF.
- `out` is the decoded value, or `null` when the web app finds no record there.

## Areas

| Area | Web app function | `in` | Bytes |
|---|---|---|---|
| `report_rate` | `BD` | `hz` | pair at 0: `[v, 0x55-v]` |
| `dpi_value` | `KD` (with its converter `as`) | `sensor`, `stage` 0–7, `dpi` | record at 12+4·stage: `[raw, raw, flags, cks]` |
| `key_function` | `DP` | `slot` 0–15, `type`, `param`; for type 10 `sensor` and `dpi` instead of `param` | record at 96+4·slot |
| `shortcut` | `BP` | `slot`, optional `preset`, `keys`, `events` | record at 256+32·slot |
| `media` | `PP` | `slot`, `code` (consumer usage) | record at 256+32·slot |
| `macro` | `zP` (body built by `S_`) | `slot`, `name`, `events` | body at 768+384·slot |
| `macro_binding` | `DP` with type 6 | `slot`, `cycle` | record at 96+4·slot: `[6, slot, cycle, cks]` |
| `shortcut_decode` | `I0` | `slot`, `hex` | `out`: `media`, `events` |
| `macro_decode` | `h_` (through its mouse wrapper) | `slot`, `hex` | `out`: `name`, `events`, or `null` |

The function names are minified identifiers from the bundle the vectors came from. They are listed only to trace where each area comes from.

Field details:
- **`key_function`.** `type` is the button type byte and `param` the 16-bit parameter, written big-endian. Types covered: 0 disable, 1 mouse button, 2 DPI switch, 3 scroll left/right, 4 fire key (`param` = interval<<8 | times, interval 10–255, times 0–3), 5 shortcut binding, 7 report-rate switch, 8 drag scroll (0x0500), 9 profile switch, 10 DPI lock, 11 scroll up/down. Type 6 has its own area. For type 10 the web app stores the sensor's raw DPI code little-endian and drops the range flag, so only DPIs from the sensor's first range are used (3104 and 3212).
- **`shortcut`.** `events` lists the pressed keys in order as `{kind, value}`: kind 0 is a modifier bit (0x01 LCtrl … 0x80 RWin), kind 1 a HID keyboard usage, kind 7 the context-menu key. The record lists the presses, then the releases in reverse order. `keys` names the same keys by their key-table name, which is the browser `KeyboardEvent.code` for all but a few international keys. `preset` is set when the combo is one of the web app's per-OS presets (`win/diy5`, `mac/diy17`, …); those keep the preset's own modifier order. Custom combos have up to four modifiers, in random order, and one key. Most use left-side modifiers only, as the web app's composer does; about a fifth also draw right-side modifiers.
- **`macro`.** `name` is 1–30 UTF-8 bytes in the web app's sanitised form (no whitespace or punctuation, some multi-byte characters). `events` has 1–70 entries `{press, kind, value, delay}`: `press` false means release, kind 0/1/7 as above plus kind 4 for mouse buttons (value 1, 2, 4, 8, 0x10), `delay` 10–65535 ms. Packets are kept only on the first six (coverage) cases to keep the file small; the other cases carry `writes` only.
- **`macro_binding`.** `cycle` is 1–250 (repeat count), 253, 254 or 255.
- **`shortcut_decode`.** `media` is true for a single consumer-usage event (kind 2). The web app's decoder reads only the press half of the record, so `events` has one entry per key.
- **`macro_decode`.** `events` use the same shape as in `macro`. Two cases hold an erased (all 0xFF) and an all-zero 384-byte slot; both decode to `null`.

## Input domain

Each area starts with a fixed coverage set, followed by random cases until it holds the requested number of cases. Each area draws from its own random stream, seeded from the run seed and the area name, so areas do not affect one another.

- The coverage sets are: every report rate in the web app's list; every legal DPI of sensors 3104 and 3212 (the only sensors ProtoArc models use), and the ends of every DPI range of the sensors without a value table; every type and parameter listed above plus the fire-key and DPI-lock corners; every OS preset combo plus a few edge combos (five keys, one key, the context-menu key, all right-side modifiers); every media code; macros with 1 and 70 events, 1-, 30- and multi-byte names and the delay extremes; the cycle values 1, 250, 253, 254 and 255, and every slot.
- DPI values come from each sensor's legal ranges and must be written as one 4-byte record. For a sensor with a value table (3104, 3212, 3311, 3325, 3335, 4090, 8920) a value is also kept only if the web app decodes the written record back to the same X and Y DPI. The sensors without a value table (3370, 3395, 3950) compute the raw code as DPI / step − 1, so their codes pass 0xFF and set the high raw bits in byte 2; they are checked on the encoder, and the harness reports any value the web app decodes differently.
- Inputs stay inside the domain the web app handles correctly, so its known bugs do not become expectations: no macro over 70 events, no key text that the web app resolves to a different key, no DPI-lock value above the first range, no unrounded DPI.
