# Protocol notes

What arcctl knows about the HID protocol that ProtoArc receivers, mice and keyboards speak, written in our own words. Each fact names the evidence tag it rests on and the code that implements it.

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

Commands travel as 16-byte output reports with report ID 8 [TS]. An outgoing packet is valid when 8 plus the sum of its 16 bytes, modulo 256, equals 0x55 [WE]; whether every reply follows the same rule is checked in H0. The test vectors in `testdata/vectors.json` check this rule.

TODO (M1): header layout, target byte, how replies are matched to requests.

## Commands

TODO (M1): each command arcctl sends or receives, what it means, and which policy allows it.

## Flash layout

TODO (M1): mouse settings map (pairs, 4-byte records, key functions, shortcut and macro bodies) and the keyboard map.

## Record encodings

TODO (M1): DPI codec, key functions, shortcuts, media keys, macros and their checksums.

## Keyboards

TODO (M8): key slots, the 0x80 flag, lighting, settings and CRC entries.
