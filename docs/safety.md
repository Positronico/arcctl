# Safety

A mouse keeps its settings only in its own memory, and arcctl is designed around that fact. arcctl never sends feature reports or report ID 6, which the firmware updater uses, so it cannot change the firmware; the realistic failure is a scrambled or half-written configuration, and everything below aims to prevent that or undo it.

## Before you start

Close the ProtoArc web app and any Chrome tab that uses it. Two programs talking to the receiver at the same time can steal or garble each other's replies, and arcctl blocks writes while it can see another client.

## Invariants

TODO (M1-M3): the rules the code enforces, such as backup before the first write, a single allowlist check on every packet, record-level writes, read-back verification and the journal.

## Recovery ladder

TODO (M3-M5): the steps to take when something goes wrong, in order:

1. Roll back from the journal.
2. Restore from a backup.
3. Factory reset, once hardware test H7 has documented its effect.
4. The vendor web app, and the limits it has on this mouse.

## Re-pairing without arcctl

TODO (before H7): how to pair the mouse to its receiver again using the offline web app, and which pointing device to keep at hand while doing it.
