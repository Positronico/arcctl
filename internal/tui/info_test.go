package tui

import (
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/session"
)

// emuAddr is the emulator's placeholder mouse address; no real address may
// reach a golden file.
var emuAddr = [3]byte{0x11, 0x22, 0x33}

func infoSnap(t *testing.T) *session.Snapshot {
	sn := ready(t)
	sn.Identity.Addr = emuAddr
	sn.Battery.MilliVolts = 3900
	sn.LongRange = session.Probe{Asked: true, Supported: true, Value: 1}
	sn.Stats = session.Stats{Transactions: 57, Pushes: 1}
	return sn
}

func showInfo(h *harness) { dilKeys(h, "2") }

func TestInfoView(t *testing.T) {
	h := dilHarness(t, infoSnap(t))
	showInfo(h)
	h.golden("info")
	dilKeys(h, "G")
	h.golden("info-end")
	dilKeys(h, "g")
	h.screen(80, 24)
	dilKeys(h, "pgdown")
	if s := h.screen(80, 24); !strings.Contains(s, "lines 17-33") {
		t.Errorf("pgdown:\n%s", s)
	}
}

func TestInfoNeverPrintsTheAddress(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		sn := infoSnap(t)
		sn.Identity.AddrTrusted = trusted
		h := dilHarness(t, sn)
		showInfo(h)
		want := map[bool]string{false: "read, not trusted yet", true: "trusted: backups and the journal key on it"}[trusted]
		var all strings.Builder
		for _, size := range [][2]int{{80, 24}, {120, 40}} {
			dilKeys(h, "g")
			for range 6 {
				all.WriteString(h.screen(size[0], size[1]))
				dilKeys(h, "pgdown")
			}
		}
		s := all.String()
		for _, bad := range []string{"112233", "11:22:33", "11 22 33"} {
			if strings.Contains(s, bad) {
				t.Errorf("trusted %v: the address shows as %q", trusted, bad)
			}
		}
		if !strings.Contains(s, want) {
			t.Errorf("trusted %v: no %q", trusted, want)
		}
	}
}

func TestInfoBeforeHandshake(t *testing.T) {
	h := dilHarness(t, attached(session.Offline))
	s := h.screen(120, 40)
	for _, want := range []string{"not known yet", "cmd 14 not asked yet", "not read yet", "decoded once a mouse configuration is loaded"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q:\n%s", want, s)
		}
	}
	if strings.Contains(dilTabBar(h), "DPI") {
		t.Error("the DPI tab shows before a model is known")
	}
}

func TestInfoUnknownDevice(t *testing.T) {
	sn := attached(session.Unknown)
	sn.Handshake = &session.Handshake{CID: 0x7B, MID: 0x42}
	sn.Identity = plan.Identity{CID: 0x7B, MID: 0x42, VID: 0x260D, PID: 0x1282}
	h := dilHarness(t, sn)
	h.golden("info-unknown")
}

func dilTabBar(h *harness) string {
	return strings.Split(h.screen(80, 24), "\n")[1]
}

// What arcctl said before the TUI started shows as the first notice, and
// the Info tab keeps it with the data folder.
func TestStartupNotes(t *testing.T) {
	note := "The emulated mouse's journal and backups go to /tmp/arcctl-emulated-1."
	h := dilHarness(t, ready(t), func(o *Options) {
		o.Notes = []string{note}
		o.DataDir = "/tmp/arcctl-emulated-1"
	})
	if s := strings.Join(strings.Fields(h.screen(80, 24)), " "); !strings.Contains(s, note) {
		t.Errorf("the note is not shown at startup:\n%s", h.screen(80, 24))
	}
	h.keys("2")
	s := strings.Join(strings.Fields(h.screen(80, 80)), " ")
	if !strings.Contains(s, "Data /tmp/arcctl-emulated-1") || !strings.Contains(s, note) {
		t.Errorf("the Info tab lacks the data folder or the note:\n%s", h.screen(80, 80))
	}
}
