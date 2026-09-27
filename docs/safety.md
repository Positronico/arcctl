# Safety

A mouse keeps its settings only in its own memory, and arcctl is designed around that fact. arcctl never sends feature reports or report ID 6, which the firmware updater uses, so it cannot change the firmware; the realistic failure is a scrambled or half-written configuration, and everything below aims to prevent that or undo it.

## Before you start

Close the ProtoArc web app and any Chrome tab that uses it. Two programs talking to the receiver at the same time can steal or garble each other's replies, and arcctl blocks writes while it can see another client.

## Invariants

These are the rules the code enforces. Each one has tests, most of them run against the emulator's fault matrix.

1. **Backup before writing.** Before a session first writes to a mouse and onboard profile, it saves everything it has loaded as a backup labelled "auto before write". Before the first write ever to a mouse and profile (its journal holds no run on that profile), it first runs a full backup, labelled "auto first write", which must complete or be accepted as partial after you have seen the ranges it lacks. The session saves again after the profile switches, after it loses the mouse, and after a reload finds bytes the saved copy does not hold.
2. **One gate for every packet.** Every transport that can write passes each packet through `hidio.Guarded`, which checks it against the active policy. Only the session changes the policy: to Edit for the duration of a write, and back to read-only when the write ends, even when it fails or panics. Long range stays disabled. The factory reset has its own policy, which the guard grants only for a mouse whose model and firmware hardware test H7 has recorded, only for the one packet of a reset, and never twice in a row (see "Factory reset" below).
3. **Whole records only.** A write covers whole records the layout knows. Records that would not change and bytes never read are never written. Unknown bytes are written only by a restore with `--include-unknown`, exactly as the backup captured them, as whole records of the settings page, at the experimental tier and after a typed phrase that names each one.
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

- one entry for each run: its kind (apply, revert, recover or reset), the device and the onboard profile;
- one entry for each record the run writes, with its address and its old and new bytes; all of them are synced to disk before the run's first packet;
- the state of each record as it goes: sending (written before its first packet), sent, verified or failed, with what the record held when it was read back after a failure;
- how the run ended, and later how a recovery settled it.

A run is unfinished when it neither completed nor was settled and at least one of its records reached "sending". A run that stopped before that sent nothing, and needs nothing. If the journal cannot be written, the write stops before its next packet.

`arcctl journal status` lists the unfinished runs of every device and the last run that changed each one. `arcctl journal recover` settles them.

A factory reset is a run of its own kind with no records: it names the full backup taken just before it, it is marked "sending" on disk before its packet goes out, and it then records the mouse's reply and what the reload after it found. It is never unfinished in the sense above, since there is nothing to roll forward or back; the way back is its backup. A reset whose process ended before that reload is unchecked: `arcctl journal status` lists it, every write and reset waits, and the next session that loads the mouse reads it again, compares it with the backup and records what the reset did.

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

Once the journal is clean, the last run that changed the mouse can be undone: every record it changed goes back to the bytes it held before, through the same checks and executor. The revert is refused when a record no longer holds what that run left there (the mouse changed since), or when another mouse or another profile answers. Nothing before a factory reset can be reverted: restore the backup the reset took instead.

## Factory reset

The factory reset asks the mouse to go back to its own defaults. Nobody knows yet exactly what it clears on this mouse, or whether the mouse stays paired afterwards, so arcctl offers it only where hardware test H7 has recorded it for the mouse's model and firmware. Until then the Backup tab's `X` explains why it is not available, and arcctl sends nothing.

Where H7 has recorded it, the reset goes through its own review in the Backup tab:

1. The same checks as a write: the mouse online and on the profile you saw, its firmware as loaded, no other program on the receiver, no screen lock or Secure Input, and a clean journal.
2. A full backup, read afresh from the mouse and saved as "auto before reset". The reset stops here if any range could not be read.
3. You type `reset`. The checks run again.
4. One reset packet, journaled first and sent once. arcctl waits up to 3 seconds for the reply and never sends it again, whether a reply came or not; even a write error that the operating system calls temporary does not make arcctl send it again, since the receiver may have taken the packet.
5. arcctl reads the whole configuration again and compares it with the backup. The comparison, not the reply, says what happened: some ranges changed, nothing changed (the mouse ignored the reset or was at its defaults already), or the mouse could not be read (then arcctl loads it when it answers again).

Nothing follows the reset: arcctl sends no long-range or receiver-light command after it, unlike the vendor web app. To go back, select the "auto before reset" backup on the Backup tab and restore it with `w`. A restore writes back only what arcctl writes: the review lists what it leaves out (the report rate, the DPI colours, button slots the web app hides and the hidden settings, among others), which stays as the reset left it. `u` in the Backup tab's diff, or `arcctl restore --include-unknown --experimental`, also writes back the unknown settings-page records as the backup captured them; the hidden settings stay out until their own hardware test.

## Recovery ladder

When something goes wrong, in order:

1. **Settle the journal.** `arcctl journal recover` finishes an interrupted write or rolls it back; once the journal is clean, the last write can be reverted from the TUI with `U`, which opens the revert review.
2. **Restore from a backup.** arcctl saved one before its first write and before every factory reset, and `arcctl backups` lists them. `arcctl restore BACKUP`, or `w` on the Backup tab, writes back the records that differ, with the same checks, journal and read-back as any write; `arcctl show BACKUP` prints every setting a backup holds.
3. **Factory reset.** Available only where hardware test H7 has recorded what it clears on this mouse's firmware (see "Factory reset" above). If the mouse stops answering afterwards, pair it again as described below.
4. **The vendor web app.** It can reset the mouse and edit what its pages show, but it cannot restore a configuration: on the maintainer's unit an invalid record cuts its import short, so it writes a single byte at address 0. Whatever its pages cannot show has to be entered again by hand from `arcctl show BACKUP`.

## Re-pairing without arcctl

arcctl never pairs a mouse with a receiver. If the mouse stops answering its receiver, for example after a factory reset, the vendor's web app does the pairing, from its online page or from a copy saved for offline use (arcctl does not ship one).

Before you start, keep another pointing device at hand: the web app needs one, and the mouse cannot help while it is unpaired. A trackpad or a second mouse will do, and so will this mouse on its Bluetooth channel: its Bluetooth pairing with the computer is separate from the receiver's and keeps working. arcctl itself needs no mouse at any step.

1. Quit arcctl, so the web app is the only program talking to the receiver.
2. Put the mouse in pairing mode: hold the left, right and middle buttons down together for about three seconds, until its light flashes.
3. In Chrome or another browser with WebHID, open the web app, connect it to the receiver and run its pairing flow: "Pairing a New Receiver" in its settings, or the pairing shortcut on its home page (the space bar).
4. Once it reports the mouse paired, close the web app's tab, start arcctl and check that the header shows the mouse online.

Rehearse these steps once before the first factory reset, so that the fallback is known to work before it is needed.
