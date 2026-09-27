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
- `--emulate` runs a stage as a rehearsal on the emulator. Its records go to a temporary folder, and it promotes nothing.

## Log format

`arcctl hwtest` writes the log below itself; nothing in it is typed by hand. For each run it:

- appends a dated entry under Log: the device and its firmware versions, each step with the bytes it sent and what came back, the questions with their answers, the findings, what it promoted, and why it stopped when it did;
- sets the stage's status in the table of stages;
- commits a redacted copy of its transcript to `testdata/transcripts/`, named by date and stage (H0 also commits the transcript of its info session on its own, which a session can replay); the unredacted transcript stays in the logs folder;
- on success, adds its features to `internal/catalog/verified.json` for the model and the mouse firmware of the run, and regenerates `zz_verified.go` with `go generate ./internal/catalog` (so it needs the Go toolchain; when the generator fails, `verified.json` is left as it was).

Entries never hold the receiver's address, the bytes of your shortcuts and macros, or local paths.

## Stages

| Stage | Covers | Status |
|---|---|---|
| H0 | read-only session, latency, coexistence | not run |
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

No runs yet.
