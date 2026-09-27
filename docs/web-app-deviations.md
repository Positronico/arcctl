# Where arcctl differs from the web app

arcctl speaks the same protocol as the vendor's web app but does not copy its behaviour where that behaviour is unsafe or wrong. This page lists each deliberate difference, so a user who switches between the two knows what to expect.

## Transport and matching

- **Which interface.** The receiver has two HID interfaces with identical descriptors, and only one answers. The web app opens and handshakes every interface with a report-8 collection and keeps the last one it opened. arcctl sends cmd 3 to each and keeps the one that answers; when several devices answer (a second receiver, or a mouse on a cable), it asks which one to use [TS](v) [GO](v).
- **One transaction at a time.** The web app lets its 5 s poll, the reads a push triggers and the user's changes run at the same time, sharing one "waiting for a reply" flag and one timer, so they can take each other's replies. arcctl runs every request from one owner, one at a time; push re-reads wait their turn [TS](v).
- **Strict matching.** The web app accepts a write reply that matches only the command and the high byte of the address. arcctl requires the command, and for reads and writes the full address and the length [TS](v).
- **Status 1 is a failure.** The web app ends its transaction on any status-1 frame, even one for another command, and treats it as success, so a rejected write still updates its copy of the settings. arcctl treats a status-1 frame with the same command (and address) as a NAK, and ignores one for another command [TS](v).
- **Unrelated reports.** In the web app, any report-8 frame that is not the reply uses up one of the five tries and causes an immediate resend. arcctl handles such frames on the side without using up a try, and handles frames that arrived earlier before it sends [TS](v).
- **No stale online flag.** The web app reads the online flag from the last frame it received even when its cmd-3 request timed out. arcctl uses only a matching reply [TS](v).
- **Duplicate, late and foreign replies.** arcctl remembers the requests of the last 2 s. A second reply to one of them, or a late reply to one that timed out, is logged. Any other reply means another program is talking to the receiver: arcctl stops trusting the link (Conflict) and never puts such bytes in its copy of the flash. The web app has no such check [TS].
- **Handshake.** The web app handshakes twice when it connects, before it knows whether the mouse is online, and a reconnect from a remembered device card uses the model stored in the browser. arcctl handshakes once the mouse is online and again on every wake, and always takes the model from that live reply, so a different paired mouse is noticed [TS](v).
- **Skipped traffic.** While connecting, the web app also sends cmds 21, 25 and 45 (receiver lighting) and cmd 29 twice; arcctl sends cmd 29 once and skips the others [TS](v).
- **Waking up.** The web app notices a woken mouse at its next 1.5 s cmd-3 poll and then reads everything again. arcctl also takes any other input report from the receiver (the mouse moving) as a hint and checks at once; a load that the mouse interrupted by falling asleep resumes from the chunk that failed [TS].
- **Polling.** The web app sends cmds 3 and 4 every 5 s while connected. arcctl sends cmd 3 every 5 s and cmd 4 every 30 s, and shows the raw battery level without the web app's smoothing [TS](v).

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
