package backup_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/session"
)

func TestStoreSavesLabelledBackups(t *testing.T) {
	at := created
	st := backup.Store{Root: t.TempDir(), Tool: "arcctl test", Source: backup.SourceEmulator, Now: func() time.Time { return at }}
	c := capture(t, dumpImage(t))
	c.Handshake = &session.Handshake{CID: 0x7B, MID: 4, Conn: 1}
	c.Full, c.Missing = true, []flash.Extent{{Addr: 9504, Len: 256}}
	path, err := st.Save(c, session.LabelFirstWrite)
	must(t, err)
	if want := filepath.Join(st.Root, "em11-pro-260d-1282-7b04", "20260926T120000Z-auto-first-write.json"); path != want {
		t.Errorf("saved to %s, want %s", path, want)
	}
	f, err := backup.Load(path)
	must(t, err)
	switch {
	case f.Label != session.LabelFirstWrite || !f.Automatic():
		t.Errorf("label %q, automatic %v", f.Label, f.Automatic())
	case f.Tool != "arcctl test" || f.Source != backup.SourceEmulator || !f.Full:
		t.Errorf("tool %q, source %q, full %v", f.Tool, f.Source, f.Full)
	case f.Device.Conn == nil || *f.Device.Conn != 1:
		t.Errorf("conn %v, want the handshake's", f.Device.Conn)
	case !slices.Equal(f.Missing(), c.Missing):
		t.Errorf("missing %v, want %v", f.Missing(), c.Missing)
	}
	again, err := st.Save(c, session.LabelFirstWrite)
	must(t, err)
	if again == path {
		t.Error("a second save in the same second replaced the first")
	}
}

func TestStoreListsOneDevice(t *testing.T) {
	root := t.TempDir()
	at := created
	st := backup.Store{Root: root, Now: func() time.Time { return at }}
	mine := capture(t, dumpImage(t))
	other := mine
	other.Device.PID = 0x1283
	for i, c := range []session.Capture{mine, other, mine} {
		at = created.Add(time.Duration(i) * time.Minute)
		_, err := st.Save(c, session.LabelBeforeWrite)
		must(t, err)
	}
	f, err := backup.New(mine, backup.Meta{Created: created.Add(-time.Hour), Label: "by hand"})
	must(t, err)
	_, err = backup.Save(root, f)
	must(t, err)

	list, err := st.List(mine.Device)
	must(t, err)
	var labels []string
	for _, l := range list {
		if l.File.Key() != mine.Device.Key() {
			t.Errorf("%s belongs to %s", l.Path, l.File.Key())
		}
		labels = append(labels, l.File.Label)
	}
	if want := []string{"by hand", session.LabelBeforeWrite, session.LabelBeforeWrite}; !slices.Equal(labels, want) {
		t.Errorf("labels %q, want %q oldest first", labels, want)
	}
	if list[0].File.Automatic() || !list[1].File.Automatic() {
		t.Error("Automatic does not follow the label")
	}
	none, err := st.List(plan.Identity{CID: 1, MID: 1})
	must(t, err)
	if len(none) != 0 {
		t.Errorf("%d backups for a device with none", len(none))
	}
}

func TestStoreNeedsARoot(t *testing.T) {
	if _, err := (backup.Store{}).Save(capture(t, dumpImage(t)), "x"); err == nil {
		t.Error("Save without a root succeeded")
	}
}
