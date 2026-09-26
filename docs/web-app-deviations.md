# Where arcctl differs from the web app

arcctl speaks the same protocol as the vendor's web app but does not copy its behaviour where that behaviour is unsafe or wrong. This page lists each deliberate difference, so a user who switches between the two knows what to expect.

## Transport and matching

TODO (M2): strict reply matching, NAK handling, draining before a send, duplicate and foreign replies, when the handshake is sent.

## Writes

TODO (M3): online check before each record, read-back verification, the journal, body-before-binding order, commands arcctl never sends, restore behaviour.

## Decoding

- **Report rate.** arcctl reads and writes the rate with the web app's main encoding (2000 Hz is 16, 4000 is 32, 8000 is 64). The web app's `.bin` import path uses a different formula that gets those three wrong [WE](v).
- **DPI.** X and Y are decoded separately, so an asymmetric stage shows both values (the maintainer's mouse has a factory stage of X 4800, Y 2400). A raw code that is not a legal DPI of the sensor is shown as invalid; the web app shows 4100 [MC](v) [UI](v).
- **Macros.** At most 70 events, press and release each counting as one. The web app can reach 71, which overflows the 384-byte slot. arcctl decodes any delay but writes at least 10 ms.
- **Shortcuts.** The releases must mirror the presses in reverse; otherwise the record is invalid. The web app reads only the presses.
- **Invalid and unread data.** A record whose checksum or shape is wrong is shown as invalid with its raw bytes, and the connection stays up. Bytes that were never read are shown as unknown; the web app decodes its 0xFF fill [KP](v). An all-zero slot counts as empty, like an all-0xFF one [WE](v).
- **Button slots.** All 16 key-function slots are decoded. Slots the web app does not show for the model are marked as such.
- **Consumer labels.** Media keys are labelled from the HID usage tables. The vendor's keyboard labels swap 0x0224 (Back) and 0x0225 (Forward).

## Keyboards

TODO (M8): settings block, all macros, the key-type flag.

## Backups and `.bin` files

TODO (M5): which regions a web app `.bin` really holds, and the extra regions arcctl backs up.
