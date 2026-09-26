# Device facts format

This directory holds what arcctl knows about ProtoArc devices from the vendor's web app: model table, IDs, sensor DPI tables, key codes, presets, flash offsets and a short list of UI labels. The files are generated. Do not edit them by hand.

- **Writer:** `tools/catalog/` in the private mirror repo. It reads the mirrored web app and the bundle extracts and writes the `*.json` files listed below.
- **Reader:** the generator in `internal/catalog/gen`, which turns them into Go tables. It needs no vendor file.
- **Validation:** `schema.json` (JSON Schema draft 2020-12) validates one object whose property names are the file names and whose values are the parsed files. `make check` runs it.

This file and `schema.json` define the format. The generator in the mirror follows them.

## Conventions

- UTF-8 JSON, two-space indent, a final newline. Key order is fixed by the writer. Running it twice on the same inputs gives byte-identical files.
- Every number is a decimal integer, including USB IDs, HID usages and flash addresses.
- Array order is meaningful wherever this file says so (UI order, press order, vendor table order). Elsewhere it is the vendor's order.
- `null` appears only where this file allows it.
- **Label keys.** A label key is an arcctl id: 2 to 4 lowercase segments of `a-z`, `0-9` and `_`, joined by dots, the first starting with a letter, at most 40 characters. Examples: `button.left`, `lod.0_7mm`, `button.group.system`. Every label key used in any file must be a key of `labels.json`. JSON Schema cannot check that across files, so the reader does.
- **Label text** is 1 to 40 characters (Unicode code points), printable, with no leading or trailing space. This applies to `labels.json`, the `win`/`mac` names in `keys.json` and the `label` field in `media.json`.

## Files

| File | Content | Derived from |
|---|---|---|
| `models.json` | one entry per cfg entry: identity, mouse defaults or keyboard layout | `cfg.json`, `lang/en.json`, keyboard pictures |
| `ids.json` | USB vendor IDs and product IDs by class | `cfg.json` |
| `sensors.json` | DPI ranges and raw code tables per sensor, feature gates | `sensor.json` |
| `media.json` | consumer usages the UI offers, with HID names | `cfg.json`, `lang/en.json`, the HID Usage Tables |
| `keys.json` | the web app's key table | bundle table `Hr` |
| `presets.json` | per-OS shortcut presets and macro repeat modes | bundle table `P0` |
| `offsets.json` | flash offset tables for mice and keyboards | bundle tables `ye`, `wR`, `bR` |
| `brightness.json` | DPI-light brightness levels | bundle function `w_` |
| `labels.json` | allowlisted English UI labels | `lang/en.json` |
| `sources.json` | format version, tool versions, SHA-256 of every input | all of the above |

The bundle names (`Hr`, `P0`, `ye`, `wR`, `bR`, `w_`) are the minifier's and only trace where a table came from.

## models.json

`{"models": [model, ...]}`, in cfg order: the `mouse` group, then `officeMouse`, then `keyboard`.

| Field | Meaning |
|---|---|
| `group` | cfg group: `mouse`, `officeMouse` or `keyboard` |
| `cid` | handshake cid |
| `mids` | handshake mids covered by this entry; one entry can cover several |
| `name` | the cfg device name, or `null` when the entry has none |
| `mouse` | present for the two mouse groups |
| `keyboard` | present for the keyboard group |

A (cid, mid) pair appears in at most one entry.

### mouse

| Field | Meaning |
|---|---|
| `sensor` | sensor id, a key of `sensors.json` |
| `maxDpi` | highest DPI the web app offers for this model |
| `dpis` | default stages in order: `dpi` and `color` as `[r, g, b]`; 1 to 8 stages |
| `currentDpi` | default active stage, 0-based |
| `keys` | default button functions, in the order the web app lists them |
| `debounce`, `tipsDebounce`, `maxDebounce` | default click debounce, the value below which the web app warns, and the maximum, in ms |
| `reportRate` | default polling rate in Hz |
| `sensorMode`, `performanceState`, `performance`, `ripple`, `angle`, `motionSync`, `sleepTime` | defaults, as raw values in the web app's units |
| `lod` | default lift-off distance code, or `null` where the cfg sets `false` |
| `dpiEffect` | DPI indicator defaults: `mode`, `brightness`, `speed`, and `show` (whether the web app shows the panel) |
| `lightEffect` | lighting defaults: `mode`, `brightness`, `speed`, `movingOffState` |
| `longDistance` | default of the extended range mode |
| `firmware` | latest firmware version the cfg lists, by component; absent when the cfg lists none |

Each element of `keys`:

| Field | Meaning |
|---|---|
| `index` | flash key slot, 0 to 15 (the record at 96 + 4 × index) |
| `type`, `param` | the default key function record's type byte and 16-bit parameter |
| `media` | present when the default is a consumer usage. The web app stores it as a shortcut body, so `type` is 5 and `param` is 0; `media` is the usage code and `label` is absent. |
| `visible` | whether the web app's button page draws this key |
| `label` | label key of the default function's name |

### keyboard

| Field | Meaning |
|---|---|
| `systems` | OS layer sets the web app offers (`win`, `mac`, `iOS`, `android`) |
| `layouts` | layers per system (`normal`, `fn`, `fn2`) |
| `image` | `width` and `height` in pixels of the model's picture. `rects` use its coordinates, so a rect outside it is a key the picture does not show. |
| `rects` | one `{left, top, width, height}` per key slot, indexed by slot |
| `maps` | default key map per system and layout, in `systems` × `layouts` order. `slots[i]` is slot i: `type` is the raw type byte (bit 0x80 included), `value` is 16 bits. |

## ids.json

`vids` lists the vendor IDs. `pids` has the classes `mouse`, `keyboard` and `composite`, each with `wireless` and `wired` lists (possibly empty). IDs are compared as numbers.

## sensors.json

`sensors` lists each sensor in vendor order:

| Field | Meaning |
|---|---|
| `id` | sensor id as cfg names it (for example `3104`) |
| `ranges` | DPI ranges in order: `min`, `max`, `step`, and `dpiEx`, the flag bits the DPI record carries for values in that range |
| `values` | raw code per step of the first range, when the sensor has a table; absent when codes are computed from the step |

`gates` lists the vendor's per-feature sensor lists in vendor order: a `name` and its `sensors`. Where the web app uses a list, it shows that feature's panel only for the sensors in it.

## media.json

| Field | Meaning |
|---|---|
| `usages` | arcctl's consumer usage table, sorted by `code`: `name` from the HID Usage Tables (Consumer page) and our short `label` |
| `mouse` | usages the mouse key editor offers, in UI order |
| `keyboard` | usages the keyboard media panel offers, in UI order |

`usages` holds every usage that `mouse`, `keyboard`, a mouse key default or a keyboard map slot (kind 3) names, plus other common usages arcctl labels when it decodes a record some other tool wrote. The generator writes it into both `catalog` and `keys` (`zz_consumer.go`), so the two packages cannot disagree.

These labels are ours, taken from the usage table. The vendor's keyboard labels for 0x0224 and 0x0225 are swapped, so vendor text is not used here.

## keys.json

`keys` is the web app's key table in its original order. The order matters: looking a key up by name takes the first match (`Enter` wins over `NumpadEnter`).

| Field | Meaning |
|---|---|
| `code` | key identifier (`KeyA`, `MetaLeft`, ...) |
| `kind` | 0 modifier, 1 key, 7 context menu |
| `value` | modifier bit for kind 0, HID keyboard usage for kind 1, 1 for kind 7 |
| `win`, `mac` | display names; for modifiers, the Windows or Mac half of the vendor's text |

## presets.json

`shortcuts.win` and `shortcuts.mac` list the preset shortcuts in vendor order. Each has an `id` (`diy1`, ...), a `label` key and `keys`. `keys` is in press order exactly as the vendor lists it; that order is written to the device as is. Each key gives `code`, `kind` and `value` from `keys.json`.

`macroRepeat` lists the macro repeat options in vendor order: the cycle byte `value`, its `mode` (`count`, `untilPressedAgain`, `untilReleased`, `untilAnyKey`) and a `label` key. For `count`, `value` is the option's default count.

## offsets.json

Three tables, each in vendor order: `mouse`, `keyboard` and `officeKeyboard` (the device types the web app names). Each entry has a `name` and a `value`: a flash address, size or count, as the vendor table defines it.

`vendor` appears only where arcctl corrects the vendor table. It holds the vendor's number, and `value` holds the corrected one, or `null` when the offset does not exist for that table:

| Table | Name | Vendor | Value | Why |
|---|---|---|---|---|
| `keyboard` | `End` | 9376 | 9430 | The vendor's end lies before the settings start (9408), so the settings pairs 9408..9429 are never read. |
| `officeKeyboard` | `CustomLightMaps` | 6176 | `null` | Copied from the keyboard table; it falls inside the office keyboard's macro area. |
| `officeKeyboard` | `TapeParam` | 7488 | `null` | Same. |

## brightness.json

`levels` maps each DPI-light brightness `level` (1, 2, ...) to the byte `value` the device gets. `default` is the byte used for any other level.

## labels.json

`labels` maps each label key to its English text. The writer keeps an allowlist that pairs each id with the text it takes; anything not listed there never reaches this repo.

## sources.json

| Field | Meaning |
|---|---|
| `schema` | version of this format, currently 1 |
| `cfgVersion` | the cfg's own version string |
| `bundle` | file name of the web app bundle the extracts came from |
| `tools` | the writer's `name` and `version`, and the extractor script's `name` and `sha256` |
| `inputs` | `path` (relative to the mirror root) and `sha256` of every file the writer read |

`arcctl version` prints these hashes. The mirror-only drift check regenerates the facts from the current vendor files and fails on any difference, the hashes included.

## Changing the format

Change `schema.json`, this file and the writer together, and bump `schema` in `sources.json` when a reader could misread the old shape.
