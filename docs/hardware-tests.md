# Hardware tests

A dated log of the hardware test stages (H0 to H9) run on the maintainer's EM11 Pro. A feature moves to the Verified tier only after its stage passes here, tied to the firmware versions recorded in the run.

## Rules for every stage

- Stages run from a build with the `hwtest` tag: `go build -tags hwtest ./cmd/arcctl`, then `arcctl hwtest --stage H0` (or H1, H2, H3, H3b). A release build has no such command.
- Close the ProtoArc web app first, except where H0 asks for it.
- Every write stage starts from a fresh full backup, which must be complete, and a dry-run preview of every write. It writes only after you confirm, and after you type the confirmation for features no stage has verified yet (`--allow-untested`, and `--experimental` for H3 and H3b).
- `arcctl hwtest --stage H1 --dry-run` (or H2, H3, H3b) shows only that preview: it reads the mouse, prints every step with its exact packets and exits 0. It takes no backup, asks nothing, writes nothing, records nothing and needs no tier flag; it says which flags the stage itself needs. H0 writes nothing and has no dry run.
- The left and right buttons (slots 0 and 1) are never touched.
- The raw writes (identity writes and H1's probe) pass the same checks as any write first: the same mouse and profile, the bytes they write still on it, no other program on the receiver unless you pass `--allow-foreign-client`, the lock, the console and a clean journal.
- Physical checks are yes/no questions. An unexpected answer fails the stage, but the stage still undoes every change it made. So does Ctrl-C: the first one stops the stage and undoes what it applied; a second one quits at once, and the stage names what it could not undo.
- H1 promotes the current stage, H2 the DPI values and the stage count, H3 the system functions. H0 and H3b promote nothing; H3b maps slots 6 to 11, and 12 and 13 on a unit that reports mid 6.
- `arcctl hwtest --stage H0 --steps unplug,trace` runs only the H0 steps named, in the stage's order. The names are doctor, trace, info, latency, loads, backups, dump, interfaces, cycles, sleep-wake, unplug, coexist, cable, typed and revoke (`arcctl hwtest -h` lists them too); dump needs backups in the same run. Such a run gets its own entry, "H0 (steps: ...)", and its own transcript.
- H0's trace presses a DPI button only when a visible button runs a DPI function (KeyFn type 2), and its instruction names that button. Without one it presses nothing and records the push check as "not testable: no button is bound to a DPI function". The 2026-09-27 run came before this check: its DPI Cycle button ran Cmd+C, so its "DPI button" line shows only keyboard reports (report ID 2) and says nothing about pushes.
- In H0's unplug step, plug the receiver back in, press Enter, then move the mouse. The step reads the receiver's cmd-3 address as soon as the receiver answers, whether the mouse is awake or asleep, and says once when the receiver is back and the mouse still asleep. It passes when the session saw the receiver go, the address is known and the session is Ready again within `--wait`.
- H3 ends with a push check. It binds the Backward button (slot 3, once its Scroll Up case is reverted) to DPI Cycle through the same planned write and revert, asks you to press it once, and records whether a StatusChanged push (report 8, cmd 10) arrived and whether the current stage moved, read again from the mouse. It then reverts the binding and, when the stage moved, writes the current stage back to the fresh backup's.
- `--emulate` runs a stage as a rehearsal on the emulator. Its records go to a temporary folder, and it promotes nothing.

## Log format

`arcctl hwtest` writes the log below itself; nothing in it is typed by hand. For each run it:

- appends a dated entry under Log: the device and its firmware versions, each step with the bytes it sent and what came back, the questions with their answers, the findings, what it promoted, and why it stopped when it did;
- sets the stage's status in the table of stages. H0 counts as passed once each of its steps has passed in some recorded run on the same model and mouse firmware, this run included (rehearsals count only toward rehearsals); its status then reads "passed" with the date of the latest run, plus "its steps over N runs" when that took more than one. Until then it reads "failed", followed by the steps not passed yet, or their count when more than three are left. A step line gives its name in backticks; entries written before steps had names are matched by title. Every other stage shows the result of its last run;
- commits a redacted copy of its transcript to `testdata/transcripts/`, named by date and stage, `h0-steps` for a run of some H0 steps (H0 also commits the transcript of its info session on its own, which a session can replay); the unredacted transcript stays in the logs folder;
- on success, adds its features to `internal/catalog/verified.json` for the model and the mouse firmware of the run, and regenerates `zz_verified.go` with `go generate ./internal/catalog` (so it needs the Go toolchain; when the generator fails, `verified.json` is left as it was).

Entries never hold the receiver's address, the bytes of your shortcuts and macros, or local paths.

## Stages

| Stage | Covers | Status |
|---|---|---|
| H0 | read-only session, latency, coexistence | failed 2026-09-27 (v1.26) |
| H1 | settings pairs, echo and NAK behaviour | not run |
| H2 | 4-byte records (DPI) | not run |
| H3 | button system functions | not run |
| H3b | physical slot map (optional) | not run |
| H4 | shortcut and media bodies | not run |
| H5 | macros and journal recovery | not run |
| H6 | restore round trip | not run |
| H7 | factory reset | not run |
| H8 | hidden settings, one field at a time | not run |
| H9 | robustness: lock, sleep, unplug, button press during writes | not run |

## Log

### 2026-09-27 H0 (read-only session, latency, coexistence): failed

- Device: ProtoArc EM11 Pro (7B04, mid 4); mouse firmware v1.26, receiver v1.0; wireless 1 kHz; profile none
- Run: 18:54 to 19:09 UTC
- Transcripts: `testdata/transcripts/2026-09-27-h0-info.jsonl`, `testdata/transcripts/2026-09-27-h0.jsonl`
- Steps:
  1. doctor: ok
     - Input Monitoring granted, held by Ghostty
     - other HID clients on the receiver: karabiner_observer
  2. trace: DPI button, sleep and wake, screen lock: ok
     - DPI button: no report-8 frame; other reports: id 2 x4, id 7 x3
     - sleep: no report-8 frame
     - wake: no report-8 frame; other reports: id 7 x208
     - screen lock: no report-8 frame; other reports: id 7 x886
  3. info: ok
     - ProtoArc EM11 Pro (7B04), cid 7b mid 4, wireless 1 kHz
     - firmware: mouse v1.26, receiver v1.0
     - cmd 14 (profiles): NAK
     - cmd 23 (long range): NAK
     - battery: level 100, charging false, 4144 mV, byte 9 set false
     - 57 transactions; inbound checksums bad: 0; NAKs 3
  4. latency of cmd-8 reads: ok
     - 200 reads of 96+10: p50 13.2ms, p99 45.3ms, max 50ms; 5 tries unanswered, 0 reads lost after 3 tries
  5. working loads while the mouse moves: ok
     - 20 loads: median 726ms, max 820ms; 0 left bytes unread; tries unanswered 0, frames dropped 0, foreign 0
  6. two full backups: ok
     - backup 1: 7243 bytes in 9.888s, unreadable: none
     - backup 2: 7243 bytes in 9.899s, unreadable: none
     - identical: yes
     - host traffic alone kept the mouse awake: yes
  7. 0..256 against flash-dump.bin: ok
     - no difference
  8. interfaces that answer: ok
     - 260d:1282 interface 0: silent
     - 260d:1282 interface 1: answers
  9. open and close cycles: ok
     - 50 open/close cycles: 0 without an answer to cmd 3, slowest close 6ms; a close during a read returned after 2ms
  10. address across sleep and wake: ok
     - asleep 13s after the last input; cmd-3 address across sleep and wake: unchanged
  11. unplug while idle, address across a replug: failed
     - the session saw the receiver go: yes (state no receiver)
     - hwtest: the device is not ready: the session did not come back after the replug
  12. coexistence with the web app: ok
     - in 20s a listener that sends nothing saw 8 replies meant for another client
     - the IORegistry scan names: Google Chrome
     - a session next to it: conflict; foreign replies 8, unanswered tries 8
  13. USB-C cable: ok
     - 260d:1282 interface 0: silent
     - 260d:1282 interface 1: answers
  14. typed confirmation and Secure Input: ok
     - CLI: Secure Input after the typed confirmation: off
     - TUI: not built yet; repeat this check in M4
  15. Input Monitoring revoked: ok
     - permission: denied for Ghostty
     - open fails: usb hid device failed to open [vid=0x260d; pid=0x1282; mfr="CX"; prod="ProtoArc EM11 Pro"]: (iokit/common) not permitted (0xe00002e2) (IOReturn 0xE00002E2)
- Answers:
  - Stage H0 writes nothing. It asks you to press buttons, let the mouse sleep, unplug the receiver, open the web app and plug in the USB-C cable. Start? yes
  - Type "confirm H0" and press Enter: confirm H0
  - Revoke the Input Monitoring grant? yes
- Findings:
  - baseline HID clients on the receiver: karabiner_observer
  - trace: DPI button -> no report-8 frame; other reports: id 2 x4, id 7 x3 | sleep -> no report-8 frame | wake -> no report-8 frame; other reports: id 7 x208 | screen lock -> no report-8 frame; other reports: id 7 x886
  - info: mid 4, wireless 1 kHz; cmd 14 NAK; cmd 23 NAK; inbound checksums bad 0 of 57 replies
  - latency: 200 reads of 96+10: p50 13.2ms, p99 45.3ms, max 50ms; 5 tries unanswered, 0 reads lost after 3 tries
  - loads while moving: 20 loads: median 726ms, max 820ms; 0 left bytes unread; tries unanswered 0, frames dropped 0, foreign 0
  - full backups took 9.888s and 9.899s; unreadable ranges: none; host traffic kept the mouse awake: yes
  - 0..256 equals flash-dump.bin
  - interfaces answering cmd 3: 1
  - 50 open/close cycles: 0 without an answer to cmd 3, slowest close 6ms; a close during a read returned after 2ms
  - asleep 13s after the last input; cmd-3 address across sleep and wake: unchanged
  - with the web app connected, replies are broadcast (every client sees every reply); the scan names Google Chrome; a session ends up conflict
  - with the cable in, 1 interfaces answer cmd 3
  - a typed confirmation in the CLI leaves Secure Input off
  - without Input Monitoring: denied for Ghostty; open fails: usb hid device failed to open [vid=0x260d; pid=0x1282; mfr="CX"; prod="ProtoArc EM11 Pro"]: (iokit/common) not permitted (0xe00002e2) (IOReturn 0xE00002E2)
  - exit criteria: two full backups identical: yes; 0..256 equals flash-dump.bin or explained: yes; no reply lost beyond the retries: yes
  - to do by hand: update the conflict thresholds, the retry budget and the emulator's reply layouts from these findings
