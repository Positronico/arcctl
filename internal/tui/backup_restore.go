package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
)

// restoreReview is what a review of a restore (§6.6) writes back: the
// backup, and the last layout of the restore against the mouse.
type restoreReview struct {
	src    *backup.Source
	source string
	// read holds what the Backup tab read from the mouse for the restore;
	// the review lays the restore out on the loaded image with these bytes
	// wherever that image lacks them. The preflight reads every record the
	// plan writes again, so a byte that went stale refuses the write.
	read *flash.Image
	rs   *backup.Restore
	// unknown is --include-unknown, asked for in the diff: the records arcctl
	// knows no valid value for go back as the backup captured them.
	unknown bool
}

// replanRestore lays the restore out again on the image the tabs show: the
// records that differ, and the plan that writes those it can.
func (d *reviewDialog) replanRestore(c *Context) {
	r := d.restore
	if m := c.Model(); m == nil || m.Family != catalog.FamilyMouse || c.Image() == nil {
		d.planErr = errNoMouse
		return
	}
	tg := restoreTarget(c, r.source)
	tg.Image = withReads(tg.Image, r.read)
	rs, err := backup.PlanRestore(r.src, tg, backup.RestoreOptions{IncludeUnknown: r.unknown})
	if err != nil {
		d.planErr = err
		return
	}
	r.rs, d.plan = rs, rs.Plan
}

// restoreLines say which backup the restore writes back and what it
// leaves alone.
func (d *reviewDialog) restoreLines(c *Context, w int) []string {
	r := d.restore
	f := r.src.File
	text := "It writes back " + filepath.Base(r.src.Path)
	if f != nil {
		text += fmt.Sprintf(", %s of %s from %s UTC", map[bool]string{true: "a full backup", false: "a backup of the loaded bytes"}[f.Full],
			f.Key(), f.Created.UTC().Format(time.DateTime))
	}
	text += ": every record that differs, and that arcctl writes, goes back to what the backup holds."
	out := wrap(text, w, "", "")
	if r.rs == nil {
		return out
	}
	if cs := r.rs.Captured(); len(cs) > 0 {
		names := make([]string, len(cs))
		for i, x := range cs {
			names[i] = x.Name + " (" + x.Extent.String() + ")"
		}
		text := fmt.Sprintf("With unknown records included, it also writes back %s as the backup captured them, at the experimental tier: %s.",
			plural(len(cs), "record", "records"), strings.Join(names, ", "))
		out = append(out, wrap(text, w, "", "")...)
	}
	return append(out, restoreSummary(c, r.rs, w)...)
}

// restoreLeftLines list the records that differ and that the restore
// leaves alone; they follow the plan, so the plan shows first.
func (d *reviewDialog) restoreLeftLines(c *Context, w int) []string {
	r := d.restore
	if r.rs == nil || r.rs.Count(backup.FateWrite) == len(r.rs.Records) {
		return nil
	}
	out := []string{"", c.Styles.Bold.Render("Left alone")}
	return append(out, restoreRecordLines(c, r.src, r.rs, w, true)...)
}

// withReads is im with the bytes of read it does not know.
func withReads(im, read *flash.Image) *flash.Image {
	if read == nil {
		return im
	}
	out := im.Clone()
	for _, e := range read.KnownExtents() {
		for a := e.Addr; a < e.End(); a++ {
			if _, ok := out.Byte(a); !ok {
				b, _ := read.Byte(a)
				_ = out.Set(a, []byte{b})
			}
		}
	}
	return out
}

// restoring reports whether d is a review that writes a backup back.
func restoring(d Dialog) bool {
	rv, ok := d.(*reviewDialog)
	return ok && rv.restore != nil
}
