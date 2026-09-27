package cli

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/platform"
	"github.com/positronico/arcctl/internal/session"
)

// doctor collects findings; the first problem sets the exit code.
type doctor struct {
	r        *runner
	problems []*exitError
}

func (d *doctor) problem(code int, msg, hint string) {
	d.problems = append(d.problems, &exitError{code: code, msg: msg, hint: hint})
}

func (d *doctor) line(label, text string) {
	fmt.Fprintf(d.r.out, "%-13s %s\n", label, text)
}

func runDoctor(r *runner, args []string) error {
	fs := r.flagSet("doctor", synopsis("doctor"))
	request := fs.Bool("request", false, "ask macOS for Input Monitoring first (shows the system prompt once)")
	if _, err := r.parse(fs, args, 0, 0); err != nil {
		return err
	}
	w, err := r.world()
	if err != nil {
		return err
	}
	defer w.close()
	d := &doctor{r: r}
	d.system(w)
	if *request {
		if a, err := w.host.RequestPermission(); err != nil {
			d.line("Request", "failed: "+err.Error())
		} else {
			d.line("Request", "Input Monitoring is "+a.String())
		}
	}
	d.permission(w)
	d.console(w)
	d.lockState()
	fmt.Fprintln(r.out)
	d.interfaces(w)
	fmt.Fprintln(r.out)
	d.clients(w)
	return d.finish()
}

func (d *doctor) system(w *world) {
	backend := d.r.g.backend
	if backend == "" {
		backend = hidio.DefaultBackend
	}
	switch w.source {
	case backup.SourceEmulator:
		backend = "emulator (" + d.r.g.emulate + ")"
	case backup.SourceReplay:
		backend = "replay (" + d.r.g.replay + ")"
	default:
		backend += " (built in: " + strings.Join(hidio.Backends(), ", ") + ")"
	}
	d.line("System", runtime.GOOS+"/"+runtime.GOARCH+", arcctl "+d.r.env.Version)
	d.line("Backend", backend)
}

func (d *doctor) permission(w *world) {
	p, err := w.host.Permission()
	if err != nil {
		d.line("Permission", "cannot tell: "+err.Error())
		return
	}
	app := p.App.Name
	if app == "" {
		app = "the terminal app"
	}
	switch p.Access {
	case platform.AccessNotNeeded:
		d.line("Permission", "none needed")
	case platform.AccessGranted:
		d.line("Permission", "Input Monitoring granted to "+app+via(p.App))
	case platform.AccessDenied:
		d.line("Permission", "denied to "+app+via(p.App))
		d.problem(ExitPermission, "device access is denied", p.Hint())
	default:
		d.line("Permission", "not decided yet for "+app+via(p.App)+"; 'arcctl doctor --request' asks")
	}
	if p.App.Tmux {
		d.line("", "in tmux: granted to the app that started the tmux server")
	}
	for _, n := range p.Nodes {
		state := n.Access.String()
		if n.Err != nil {
			state += ": " + n.Err.Error()
		}
		d.line("", fmt.Sprintf("%s %04x:%04x %s", n.Path, n.VID, n.PID, state))
	}
	if runtime.GOOS == "linux" && w.locks && (p.Access != platform.AccessGranted) {
		fmt.Fprintf(d.r.out, "\nTo give your login access to the receiver, install the udev rules:\n%s\n", indent(platform.UdevHint(), "  "))
	}
}

func via(a platform.App) string {
	if a.Via == platform.ViaNone {
		return ""
	}
	return " (found by " + a.Via.String() + ")"
}

func (d *doctor) console(w *world) {
	c, err := w.host.Console()
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		return
	case errors.Is(err, platform.ErrNoSession):
		d.line("Console", "no login session (SSH?); the screen lock cannot be read")
		return
	case err != nil:
		d.line("Console", "cannot tell: "+err.Error())
		return
	}
	screen := "screen unlocked"
	if c.ScreenLocked {
		screen = "screen locked"
		d.problem(ExitBlocked, "the screen is locked", "Unlock the Mac; macOS refuses HID reports while it is locked.")
	}
	secure := "Secure Input off"
	if c.SecureInput.PID != 0 {
		secure = "Secure Input on, held by " + c.SecureInput.String()
		// The lock screen holds Secure Input itself; unlocking clears both.
		if !c.ScreenLocked {
			d.problem(ExitBlocked, "Secure Input is on, held by "+c.SecureInput.String(),
				"Close the password field, or turn off Secure Keyboard Entry in the terminal's menu.")
		}
	}
	d.line("Console", screen+", "+secure)
}

// lockState reports the single-instance lock. For the real device the probe
// below takes it; elsewhere it is only tried.
func (d *doctor) lockState() {
	paths, err := d.r.env.Paths()
	if err != nil {
		d.line("Lock", "cannot tell: "+err.Error())
		return
	}
	l, err := platform.AcquireLock(paths.Lock)
	var le *platform.LockedError
	switch {
	case errors.As(err, &le):
		d.line("Lock", "held: "+le.Error())
		d.problem(ExitBlocked, "another arcctl is running", "Close it (the TUI or a running command), then retry.")
	case err != nil:
		d.line("Lock", "cannot take "+paths.Lock+": "+err.Error())
	default:
		_ = l.Release()
		d.line("Lock", "free ("+paths.Lock+")")
	}
}

func (d *doctor) interfaces(w *world) {
	r := d.r
	cands, err := w.devices.Enumerate()
	if err != nil {
		fmt.Fprintf(r.out, "Interfaces: cannot enumerate: %v\n", err)
		d.problem(ExitNoReceiver, "enumeration failed", "")
		return
	}
	if len(cands) == 0 {
		fmt.Fprintln(r.out, "Interfaces: none found")
		d.problem(ExitNoReceiver, "no ProtoArc receiver or mouse found", "Plug in the receiver, or the mouse with its USB cable.")
		return
	}
	if slices.ContainsFunc(d.problems, func(p *exitError) bool { return p.code == ExitPermission || p.code == ExitBlocked }) {
		fmt.Fprintln(r.out, "Interfaces (not probed until the problems below are fixed)")
		d.listCandidates(cands, nil)
		return
	}
	c, sn, err := r.attach(w, probed)
	if c != nil {
		c.stop()
	}
	var ee *exitError
	switch {
	case errors.As(err, &ee) && ee.code == ExitOffline:
	case ee != nil:
		d.problems = append(d.problems, ee)
	case err != nil:
		d.problem(ExitFailure, err.Error(), "")
	}
	fmt.Fprintln(r.out, "Interfaces")
	d.listCandidates(cands, sn)
}

func (d *doctor) listCandidates(cands []hidio.Candidate, sn *session.Snapshot) {
	for _, c := range cands {
		iface := "?"
		if c.Interface >= 0 {
			iface = fmt.Sprint(c.Interface)
		}
		name := c.Product
		if name == "" {
			name = c.Class.String()
		}
		fmt.Fprintf(d.r.out, "  %04x:%04x if%s  %s\n", c.VID, c.PID, iface, name)
		fmt.Fprintf(d.r.out, "    %s\n", c.Path)
		if sn != nil {
			fmt.Fprintf(d.r.out, "    %s\n", d.answer(c, sn))
		}
	}
}

func (d *doctor) answer(c hidio.Candidate, sn *session.Snapshot) string {
	if sn.Device != nil && sn.Device.Path == c.Path {
		return "answers: " + mouseState(sn.Online, sn.Model, sn.Handshake)
	}
	for _, a := range sn.Answers {
		if a.Candidate.Path == c.Path {
			return "answers: " + mouseState(a.Online, a.Model, a.Handshake)
		}
	}
	if d.r.g.device != "" && c.Path != d.r.g.device {
		return "not probed (--device names another)"
	}
	return "silent"
}

func mouseState(online bool, m *catalog.Model, h *session.Handshake) string {
	switch {
	case !online:
		return "the mouse is offline (asleep, out of range, on Bluetooth or not paired)"
	case m != nil:
		return "mouse online, " + m.Name
	case h != nil:
		return fmt.Sprintf("online, unknown device cid 0x%02x mid %d", h.CID, h.MID)
	}
	return "mouse online"
}

func (d *doctor) clients(w *world) {
	devs, err := w.host.Clients()
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		fmt.Fprintln(d.r.out, "HID clients: not available on this system")
		return
	case err != nil:
		fmt.Fprintf(d.r.out, "HID clients: cannot scan: %v\n", err)
		return
	}
	fmt.Fprintln(d.r.out, "HID clients")
	if len(devs) == 0 {
		fmt.Fprintln(d.r.out, "  none")
	}
	for _, dev := range devs {
		foreign := platform.Foreign(dev.Clients, nil)
		var parts []string
		for _, c := range dev.Clients {
			tag := "benign"
			switch {
			case c.Self:
				tag = "this arcctl"
			case slices.ContainsFunc(foreign, func(f platform.Client) bool { return f.PID == c.PID }):
				tag = "foreign"
				if c.Seized {
					tag = "foreign, seized"
				}
			}
			parts = append(parts, fmt.Sprintf("%s (pid %d, %s)", orName(c.Name), c.PID, tag))
		}
		if len(parts) == 0 {
			parts = []string{"none"}
		}
		iface := "?"
		if dev.Interface >= 0 {
			iface = fmt.Sprint(dev.Interface)
		}
		fmt.Fprintf(d.r.out, "  %04x:%04x if%s  %s\n", dev.VID, dev.PID, iface, strings.Join(parts, ", "))
		if len(foreign) > 0 {
			d.problem(ExitBlocked, fmt.Sprintf("another program holds %04x:%04x interface %s", dev.VID, dev.PID, iface), closeOthers)
		}
	}
}

func orName(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func (d *doctor) finish() error {
	out := d.r.out
	fmt.Fprintln(out)
	if len(d.problems) == 0 {
		fmt.Fprintln(out, "No problems found.")
		return nil
	}
	fmt.Fprintln(out, "Problems")
	seen := map[string]bool{}
	for _, p := range d.problems {
		if seen[p.msg] {
			continue
		}
		seen[p.msg] = true
		fmt.Fprintln(out, fill("  - "+p.msg, "  ", 80))
		if p.hint != "" {
			fmt.Fprintln(out, fill(indent(p.hint, "    "), "", 80))
		}
	}
	return &exitError{code: d.problems[0].code, msg: fmt.Sprintf("%d problem(s) found", len(seen))}
}
