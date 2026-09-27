package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/session"
)

type infoJSON struct {
	State        string        `json:"state"`
	Device       *deviceJSON   `json:"device,omitempty"`
	Receiver     string        `json:"receiver_firmware,omitempty"`
	Online       bool          `json:"online"`
	Address      string        `json:"address,omitempty"`
	Handshake    *handJSON     `json:"handshake,omitempty"`
	Model        *modelJSON    `json:"model,omitempty"`
	Firmware     string        `json:"mouse_firmware,omitempty"`
	Battery      *batteryJSON  `json:"battery,omitempty"`
	Profile      *probeJSON    `json:"profile,omitempty"`
	LongRange    *probeJSON    `json:"long_range,omitempty"`
	Identity     string        `json:"identity,omitempty"`
	Unread       int           `json:"unread_chunks"`
	Writes       bool          `json:"writes"`
	Tiers        []featureJSON `json:"tiers,omitempty"`
	OtherClients []string      `json:"other_clients,omitempty"`
}

type deviceJSON struct {
	Path      string `json:"path"`
	VID       string `json:"vid"`
	PID       string `json:"pid"`
	Interface int    `json:"interface"`
	Product   string `json:"product,omitempty"`
}

type handJSON struct {
	CID  string `json:"cid"`
	MID  int    `json:"mid"`
	Conn int    `json:"conn"`
	Type string `json:"type"`
}

type modelJSON struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Sensor string `json:"sensor,omitempty"`
}

type batteryJSON struct {
	Level      int  `json:"level"`
	Charging   bool `json:"charging"`
	MilliVolts int  `json:"millivolts"`
}

// probeJSON is a query the device may refuse; Value is null when it did.
type probeJSON struct {
	Supported bool `json:"supported"`
	Value     *int `json:"value"`
}

type featureJSON struct {
	Feature string `json:"feature"`
	Tier    string `json:"tier"`
}

func runInfo(r *runner, args []string) error {
	fs := r.flagSet("info", synopsis("info"))
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := r.parse(fs, args, 0, 0); err != nil {
		return err
	}
	c, sn, err := r.connect(loaded)
	if c != nil {
		defer c.stop()
	}
	var ee *exitError
	if err != nil && !(errors.As(err, &ee) && ee.code == ExitOffline && sn != nil) {
		return err
	}
	info := infoOf(sn)
	if *asJSON {
		enc := json.NewEncoder(r.out)
		enc.SetIndent("", "  ")
		if jerr := enc.Encode(info); jerr != nil {
			return jerr
		}
	} else {
		r.printInfo(r.out, sn, info)
	}
	return err
}

func infoOf(sn *session.Snapshot) infoJSON {
	in := infoJSON{State: sn.State.String(), Online: sn.Online, Receiver: sn.Versions.Receiver,
		Firmware: sn.Versions.Mouse, Unread: len(sn.Unread)}
	if d := sn.Device; d != nil {
		in.Device = &deviceJSON{Path: d.Path, VID: fmt.Sprintf("%04x", d.VID), PID: fmt.Sprintf("%04x", d.PID),
			Interface: d.Interface, Product: d.Product}
	}
	if a := sn.Identity.Addr; a != [3]byte{} {
		in.Address = fmt.Sprintf("%02x:%02x:%02x", a[0], a[1], a[2])
	}
	if h := sn.Handshake; h != nil {
		in.Handshake = &handJSON{CID: fmt.Sprintf("%02x", h.CID), MID: int(h.MID), Conn: int(h.Conn), Type: h.ConnString()}
		in.Identity = sn.Identity.Key()
	}
	if m := sn.Model; m != nil {
		in.Model = &modelJSON{Key: m.Key, Name: m.Name}
		if m.Sensor != nil {
			in.Model.Sensor = m.Sensor.ID
		}
		in.Tiers = tiers(sn)
	}
	if b := sn.Battery; b != nil {
		in.Battery = &batteryJSON{Level: int(b.Level), Charging: b.Charging, MilliVolts: int(b.MilliVolts)}
	}
	in.Profile = probe(sn.Profile)
	in.LongRange = probe(sn.LongRange)
	for _, c := range sn.Clients {
		in.OtherClients = append(in.OtherClients, fmt.Sprintf("%s (pid %d)", c.Name, c.PID))
	}
	return in
}

func probe(p session.Probe) *probeJSON {
	if !p.Asked {
		return nil
	}
	pj := &probeJSON{Supported: p.Supported}
	if p.Supported {
		v := int(p.Value)
		pj.Value = &v
	}
	return pj
}

func tiers(sn *session.Snapshot) []featureJSON {
	opt := mouse.Options{Device: sn.Identity, Firmware: sn.Versions.Mouse, Verified: catalog.VerifiedStages()}
	var out []featureJSON
	for _, f := range mouse.Features() {
		t, _ := f.Tier(sn.Model, opt)
		out = append(out, featureJSON{Feature: string(f), Tier: t.String()})
	}
	return out
}

func (r *runner) printInfo(w io.Writer, sn *session.Snapshot, in infoJSON) {
	line := func(label, text string) { fmt.Fprintf(w, "%-11s %s\n", label, text) }
	line("State", in.State)
	if d := in.Device; d != nil {
		iface := "?"
		if d.Interface >= 0 {
			iface = fmt.Sprint(d.Interface)
		}
		line("Device", fmt.Sprintf("%s:%s interface %s, %s", d.VID, d.PID, iface, d.Product))
		line("Path", d.Path)
	}
	if in.Receiver != "" {
		line("Receiver", "firmware "+in.Receiver)
	}
	mouseLine := "offline"
	if in.Online {
		mouseLine = "online"
	}
	if in.Address != "" {
		mouseLine += ", address " + in.Address
	}
	line("Mouse", mouseLine)
	if h := in.Handshake; h != nil {
		line("Handshake", fmt.Sprintf("cid 0x%s, mid %d, %s", h.CID, h.MID, h.Type))
	}
	if m := sn.Model; m != nil {
		text := m.Name + " (" + m.Key + ")"
		if m.Sensor != nil {
			text += fmt.Sprintf(", sensor %s, %d stages, up to %d DPI", m.Sensor.ID, m.Stages, m.MaxDPI)
		}
		line("Model", text)
	} else if errors.Is(sn.Err, session.ErrUnknownModel) || errors.Is(sn.Err, session.ErrChargingBase) {
		line("Model", strings.TrimPrefix(sn.Err.Error(), "session: ")+"; nothing is read from it")
	}
	if in.Firmware != "" {
		line("Firmware", in.Firmware)
	}
	if b := in.Battery; b != nil {
		text := fmt.Sprintf("%d%%, %d mV", b.Level, b.MilliVolts)
		if b.Charging {
			text += ", charging"
		}
		line("Battery", text)
	}
	if p := in.Profile; p != nil {
		line("Profile", probeText(p, "onboard profile %d", "no onboard profiles (cmd 14 refused)"))
	}
	if p := in.LongRange; p != nil {
		line("Long range", probeText(p, "", "not supported (cmd 23 refused)"))
	}
	if in.Identity != "" {
		text := in.Identity
		if !sn.Identity.AddrTrusted {
			text += " (without the address until it is confirmed stable)"
		}
		line("Identity", text)
	}
	if sn.Image != nil {
		n := 0
		for _, e := range sn.Image.KnownExtents() {
			n += e.Len
		}
		text := fmt.Sprintf("%d bytes", n)
		if in.Unread > 0 {
			text += fmt.Sprintf("; %d chunks could not be read", in.Unread)
		}
		line("Loaded", text)
	}
	if len(in.OtherClients) > 0 {
		line("Others", strings.Join(in.OtherClients, ", "))
	}
	line("Writes", "journal recovery only; editing arrives with the TUI")
	if len(in.Tiers) > 0 {
		fmt.Fprintln(w, "\nFeature tiers")
		for _, t := range []catalog.Tier{catalog.Verified, catalog.Untested, catalog.Experimental, catalog.ReadOnly, catalog.Off} {
			var names []string
			for _, f := range in.Tiers {
				if f.Tier == t.String() {
					names = append(names, f.Feature)
				}
			}
			if len(names) > 0 {
				fmt.Fprintln(w, wrap(fmt.Sprintf("  %-13s", t), strings.Repeat(" ", 15), names, " ", 80))
			}
		}
	}
}

func probeText(p *probeJSON, format, refused string) string {
	switch {
	case !p.Supported || p.Value == nil:
		return refused
	case format == "":
		if *p.Value == 1 {
			return "on"
		}
		return "off"
	}
	return fmt.Sprintf(format, *p.Value)
}
