//go:build hwtest

// Package hwtest runs the hardware test stages of docs/hardware-tests.md with
// the user at the keyboard: it previews what a stage writes, asks before it
// writes, writes through the session's executor, asks the user what the mouse
// did, and records a redacted transcript, a dated log entry and, on success,
// the features the stage verified.
//
// It builds the plans the release planner refuses (identity writes, inactive
// DPI stages, hidden slots) with plan.New, and owns the raw path, which sends
// packets no guard policy allows: identity writes, the NAK probe and H7's
// factory reset. Only builds with the hwtest tag contain it.
package hwtest
