# Safety

A mouse keeps its settings only in its own memory, and arcctl is designed around that fact. arcctl never sends feature reports or report ID 6, which the firmware updater uses, so it cannot change the firmware; the realistic failure is a scrambled or half-written configuration, and everything below aims to prevent that or undo it.

## Before you start

Close the ProtoArc web app and any Chrome tab that uses it. Two programs talking to the receiver at the same time can steal or garble each other's replies, and arcctl blocks writes while it can see another client.

## Invariants

These are the rules the code enforces. Each one has tests, most of them run against the emulator's fault matrix.

1. **Backup before writing.** Before a session first writes to a mouse and onboard profile, it saves everything it has loaded as a backup labelled "auto before write". Before the first write ever to a mouse and profile (its journal holds no run on that profile), it first runs a full backup, labelled "auto first write", which must complete or be accepted as partial after you have seen the ranges it lacks. The session saves again after the profile switches, after it loses the mouse, and after a reload finds bytes the saved copy does not hold.
2. **One gate for every packet.** Every transport that can write passes each packet through `hidio.Guarded`, which checks it against the active policy. Only the session changes the policy: to Edit for the duration of a write, and back to read-only when the write ends, even when it fails or panics. Factory reset and long range stay disabled.
3. **Whole records only.** A write covers whole records the layout knows. Records that would not change, unknown bytes and bytes never read are never written.
4. **Read-back.** Every record written is read back and compared byte for byte. The mouse's reply to a write is only logged.
5. **Two-phase rebinding.** A button never points at a shortcut or macro while it is being rewritten: its binding is disabled first, the body is written and read back, then the binding is written again.
6. **Journal before writing.** Each record's old and new bytes are on disk, synced, before the first packet of the write (see below).
7. **Fresh online check.** Each record starts with a fresh "is the mouse online" query. A read or a write that goes unanswered asks again: a sleeping mouse pauses the write for up to a minute, and the record is then written again from its start. A switch of onboard profile stops the write before its next packet. When the mouse reports a change to a range the write has yet to write (a press of the DPI button, say), the write stops after the current record, or before it when none of its packets went out.
8. **Nothing untested by default.** A feature no hardware test has verified on your firmware needs `--allow-untested` and typing `write untested`; a shape the web app cannot build needs `--experimental` and `write experimental`.
9. **Recovery never needs the mouse.** No plan may take away the last Left Click, not even between the steps of a write, and the button-behaviour field at address 8 is never written. Every arcctl command works from the keyboard alone.
10. **One client at a time.** arcctl holds a single-instance lock. Writes are blocked while another program has the receiver open (unless you pass `--allow-foreign-client`), while replies nobody asked for arrive (Conflict) and while replies go missing (SuspectedConflict). After any pause in the middle of a write, these checks, the screen lock and Secure Input are checked again before the next packet.
11. **Same device, same profile.** A write, a recovery and a revert all check that the mouse answering is the one the plan, or the run, was made for, and that it is on the same onboard profile.

## The journal

Every write goes into a journal before it reaches the mouse. The journal lives in `<data>/journal/<device>/`, one file per arcctl process that wrote, named by its start time and process ID. It holds, in order:

- one entry for each run: its kind (apply, revert or recover), the device and the onboard profile;
- one entry for each record the run writes, with its address and its old and new bytes; all of them are synced to disk before the run's first packet;
- the state of each record as it goes: sending (written before its first packet), sent, verified or failed, with what the record held when it was read back after a failure;
- how the run ended, and later how a recovery settled it.

A run is unfinished when it neither completed nor was settled and at least one of its records reached "sending". A run that stopped before that sent nothing, and needs nothing. If the journal cannot be written, the write stops before its next packet.

`arcctl journal status` lists the unfinished runs of every device and the last run that changed each one. `arcctl journal recover` settles them.

## Recovery

Each time the mouse is loaded, arcctl reads the records of every unfinished run again, together with the shortcuts and macros the bindings point at before, during and after the run. Each record is then:

- **new**: it holds what the run meant to leave;
- **old**: it holds what it held before the run;
- **mid**: it holds a step in between, such as a binding disabled while its body is rewritten;
- **torn**: none of those; the write was cut short.

A run whose records are all new is settled forward, and one whose records are all old is settled back, without writing. Any other run keeps the session in Recovering, and no other write can start until you choose:

- **forward** writes what the run meant to leave;
- **back** writes what the records held before the run;
- **leave** records the run as settled and writes nothing. It is refused while a record is torn, and it records which records kept the run's bytes, so that a later revert undoes those as well.

Forward and back are written like any other run: journaled, two-phase and read back. A recovery that fails is folded into the run it was settling, which is offered again.

## Revert

Once the journal is clean, the last run that changed the mouse can be undone: every record it changed goes back to the bytes it held before, through the same checks and executor. The revert is refused when a record no longer holds what that run left there (the mouse changed since), or when another mouse or another profile answers.

## Recovery ladder

When something goes wrong, in order:

1. **Settle the journal.** `arcctl journal recover` finishes an interrupted write or rolls it back; once the journal is clean, the last write can be reverted (from the TUI, M4).
2. **Restore from a backup.** arcctl saved one before its first write, and `arcctl backups` lists them. The `restore` command arrives in M5; until then, `arcctl show BACKUP` prints every setting a backup holds, to enter again by hand.
3. **Factory reset.** Disabled until hardware test H7 has documented what it clears on this mouse.
4. **The vendor web app.** It can reset the mouse and edit what its pages show, but it cannot restore a configuration: on the maintainer's unit an invalid record cuts its import short, so it writes a single byte at address 0. Whatever its pages cannot show has to be entered again by hand from `arcctl show BACKUP`.

## Re-pairing without arcctl

TODO (before H7): how to pair the mouse to its receiver again using the offline web app, and which pointing device to keep at hand while doing it.
