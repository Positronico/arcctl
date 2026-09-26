# Where arcctl differs from the web app

arcctl speaks the same protocol as the vendor's web app but does not copy its behaviour where that behaviour is unsafe or wrong. This page lists each deliberate difference, so a user who switches between the two knows what to expect.

## Transport and matching

TODO (M2): strict reply matching, NAK handling, draining before a send, duplicate and foreign replies, when the handshake is sent.

## Writes

TODO (M3): online check before each record, read-back verification, the journal, body-before-binding order, commands arcctl never sends, restore behaviour.

## Decoding

TODO (M1): rate and DPI decoding, macro event limits, invalid records, all 16 button slots, consumer key labels.

## Keyboards

TODO (M8): settings block, all macros, the key-type flag.

## Backups and `.bin` files

TODO (M5): which regions a web app `.bin` really holds, and the extra regions arcctl backs up.
