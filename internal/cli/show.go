package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
)

func runShow(r *runner, args []string) error {
	fs := r.flagSet("show", synopsis("show"))
	asJSON := fs.Bool("json", false, "print the decoded summary as JSON")
	pos, err := r.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	src, err := backup.Open(pos[0])
	if err != nil {
		return err
	}
	m, err := r.modelOf(src)
	if err != nil {
		return err
	}
	if m.Family != catalog.FamilyMouse {
		return fail(ExitFailure, m.Name+" is a keyboard; keyboard backups are decoded from M8", "")
	}
	s := backup.Summarize(m, src.Image, r.keyOS())
	if *asJSON {
		enc := json.NewEncoder(r.out)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	r.printSource(r.out, src, m)
	fmt.Fprintln(r.out)
	printSummary(r.out, s)
	return nil
}

func known(im *flash.Image) int {
	n := 0
	for _, e := range im.KnownExtents() {
		n += e.Len
	}
	return n
}

func (r *runner) printSource(w io.Writer, src *backup.Source, m *catalog.Model) {
	line := func(label, text string) { fmt.Fprintf(w, "%-10s %s\n", label, text) }
	model := m.Name + " (" + m.Key + ")"
	if m.Sensor != nil {
		model += ", sensor " + m.Sensor.ID
	}
	switch src.Kind {
	case backup.KindDump:
		line("File", src.Path)
		line("Kind", fmt.Sprintf("dump, %d bytes from address 0", known(src.Image)))
		if r.g.model == "" {
			model += "; assumed, --model changes it"
		}
		line("Model", model)
	case backup.KindBin:
		line("File", src.Path)
		line("Kind", fmt.Sprintf("web .bin, type %s, sensor %s", src.Bin.Type, src.Bin.Sensor))
		line("Model", model)
		line("Captured", fmt.Sprintf("%d bytes: what the web app reads, leaving out records of all 0xFF", known(src.Image)))
	case backup.KindBackup:
		f := src.File
		line("File", src.Path)
		line("Kind", "backup")
		created := f.Created.UTC().Format(time.DateTime) + " UTC by " + f.Tool
		if f.Source != backup.SourceDevice {
			created += ", from the " + f.Source
		}
		line("Created", created)
		if f.Label != "" {
			line("Label", f.Label)
		}
		dev := fmt.Sprintf("%04X:%04X, cid 0x%02X mid %d", uint16(f.Device.VID), uint16(f.Device.PID), byte(f.Device.CID), f.Device.MID)
		if f.Device.Conn != nil {
			dev += ", connection type " + strconv.Itoa(int(*f.Device.Conn))
		}
		line("Device", dev)
		id := f.Key()
		if f.Device.Addr != "" && !f.Device.AddrTrusted {
			id += " (address " + f.Device.Addr + ", not part of the identity yet)"
		}
		line("Identity", id)
		if fw := firmware(f); fw != "" {
			line("Firmware", fw)
		}
		line("Profile", profileText(f.Device.Profile))
		line("Model", model)
		kind := "loaded bytes only"
		if f.Full {
			kind = "full backup"
		}
		line("Captured", fmt.Sprintf("%d bytes, %s", f.Known(), kind))
		if miss := f.Missing(); len(miss) > 0 {
			words := make([]string, len(miss))
			for i, e := range miss {
				words[i] = e.String()
			}
			fmt.Fprintln(w, wrap(fmt.Sprintf("%-10s ", "Missing"), strings.Repeat(" ", 11), words, ", ", 80))
		}
	}
}

func firmware(f *backup.File) string {
	var parts []string
	if f.Device.FWMouse != "" {
		parts = append(parts, "mouse "+f.Device.FWMouse)
	}
	if f.Device.FWReceiver != "" {
		parts = append(parts, "receiver "+f.Device.FWReceiver)
	}
	return strings.Join(parts, ", ")
}

func profileText(p *backup.Profile) string {
	switch {
	case p == nil:
		return "not asked"
	case !p.Supported:
		return "no onboard profiles (cmd 14 refused)"
	}
	return "onboard profile " + strconv.Itoa(int(p.Value))
}

func intText(v *int, unit string) string {
	if v == nil {
		return "unknown"
	}
	return strconv.Itoa(*v) + unit
}

func printSummary(w io.Writer, s *backup.Summary) {
	fmt.Fprintln(w, "Settings")
	stages := intText(s.Stages, "")
	if s.Current != nil {
		stages += ", current " + strconv.Itoa(*s.Current)
	}
	table(w, "  ", [][]string{
		{"Report rate", intText(s.Rate, " Hz")},
		{"DPI stages", stages},
		{"Key names", s.OS},
	})

	fmt.Fprintln(w, "\nDPI")
	rows := [][]string{{"Stage", "DPI", "Colour", ""}}
	for _, d := range s.DPI {
		if d.State == flash.Unknown.String() {
			continue
		}
		dpi := d.State
		if d.X != 0 {
			dpi = strconv.Itoa(d.X)
			if d.Y != d.X {
				dpi += " x " + strconv.Itoa(d.Y)
			}
		}
		note := ""
		switch {
		case d.Current:
			note = "current"
		case !d.Active:
			note = "inactive"
		}
		rows = append(rows, []string{"  " + strconv.Itoa(d.Stage), dpi, d.Color, note})
	}
	table(w, "  ", rows)

	fmt.Fprintln(w, "\nButtons")
	rows = [][]string{{"Slot", "Button", "Action"}}
	for _, b := range s.Buttons {
		name := b.Button
		if b.Hidden {
			name += " (hidden)"
		}
		if name == "" {
			name = "-"
		}
		rows = append(rows, []string{fmt.Sprintf("%4d", b.Slot), name, b.Action})
	}
	table(w, "  ", rows)

	if len(s.Shortcuts) > 0 {
		fmt.Fprintln(w, "\nShortcut bodies")
		rows = [][]string{{"Slot", "Keys", ""}}
		for _, sc := range s.Shortcuts {
			keys := sc.Keys
			if sc.State != flash.SlotValid.String() {
				keys = sc.State
			}
			if sc.Preset != "" {
				keys += " (" + sc.Preset + ")"
			}
			note := ""
			if !sc.Bound {
				note = "not bound"
			}
			rows = append(rows, []string{fmt.Sprintf("%4d", sc.Slot), keys, note})
		}
		table(w, "  ", rows)
	}

	if len(s.Macros) > 0 {
		fmt.Fprintln(w, "\nMacros")
		for _, m := range s.Macros {
			head := fmt.Sprintf("  Slot %d", m.Slot)
			if m.State == flash.SlotValid.String() {
				head += " " + strconv.Quote(m.Name) + fmt.Sprintf(", %d events", len(m.Events))
			} else {
				head += ": " + m.State
			}
			if len(m.BoundBy) > 0 {
				slots := make([]string, len(m.BoundBy))
				for i, b := range m.BoundBy {
					slots[i] = strconv.Itoa(b)
				}
				head += ", used by slot " + strings.Join(slots, ", ")
			}
			fmt.Fprintln(w, head)
			if len(m.Events) > 0 {
				words := make([]string, len(m.Events))
				for i, e := range m.Events {
					verb := "release"
					if e.Press {
						verb = "press"
					}
					words[i] = fmt.Sprintf("%s %s %dms", verb, e.Key, e.Delay)
				}
				fmt.Fprintln(w, wrap("    ", "    ", words, ", ", 80))
			}
		}
	}

	if len(s.Settings) > 0 {
		fmt.Fprintln(w, "\nOther fields")
		rows = [][]string{{"Name", "Addr", "Raw", "Value"}}
		for _, st := range s.Settings {
			v := ""
			if st.Value != nil {
				v = strconv.Itoa(*st.Value)
			}
			if st.State == flash.Unset.String() {
				v = "unset"
			}
			rows = append(rows, []string{st.Name, fmt.Sprintf("%4d", st.Addr), st.Raw, v})
		}
		table(w, "  ", rows)
	}

	if len(s.Invalid) > 0 {
		fmt.Fprintln(w, "\nInvalid")
		for _, iv := range s.Invalid {
			fmt.Fprintf(w, "  %s @%d+%d: %s\n", iv.Name, iv.Addr, iv.Len, iv.Raw)
			if iv.Error != "" {
				fmt.Fprintf(w, "    %s\n", iv.Error)
			}
		}
	}
}
