package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/platform"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// Host is what the OS says about access to the devices. The emulator and a
// replay answer for themselves.
type Host interface {
	Permission() (platform.Permission, error)
	RequestPermission() (platform.Access, error)
	Console() (platform.Console, error)
	Clients() ([]platform.DeviceClients, error)
	Diagnose(err error) platform.Diagnosis
}

type systemHost struct{}

func (systemHost) Permission() (platform.Permission, error)    { return platform.CheckPermission() }
func (systemHost) RequestPermission() (platform.Access, error) { return platform.RequestPermission() }
func (systemHost) Console() (platform.Console, error)          { return platform.ConsoleState() }
func (systemHost) Clients() ([]platform.DeviceClients, error)  { return platform.HIDClients() }
func (systemHost) Diagnose(err error) platform.Diagnosis       { return platform.Diagnose(err) }

func hidDevices(backend string) (session.Devices, error) {
	if backend != "" && !slices.Contains(hidio.Backends(), backend) {
		return nil, usageError("unknown backend %q (this build has %s)", backend, strings.Join(hidio.Backends(), ", "))
	}
	return session.HID(backend), nil
}

// world is where the devices come from: the real HID backend, the emulator
// or a replay.
type world struct {
	source  string // backup.SourceDevice, SourceEmulator or SourceReplay
	devices session.Devices
	host    Host
	device  string // the only interface to use, "" for all
	locks   bool   // the real device: take the single-instance lock
	replay  *replayDevices
	close   func()
}

func (r *runner) world() (*world, error) {
	switch {
	case r.g.emulate != "":
		return r.emulator(r.g.emulate)
	case r.g.replay != "":
		return r.replayer(r.g.replay)
	}
	if r.env.HID == nil {
		return nil, errors.New("no HID backend in this environment")
	}
	d, err := r.env.HID(r.g.backend)
	if err != nil {
		return nil, err
	}
	return &world{source: backup.SourceDevice, devices: d, host: r.env.Host, device: r.g.device, locks: true, close: func() {}}, nil
}

// options builds the session options for w.
func (r *runner) options(w *world) session.Options {
	o := session.Options{Devices: w.devices, Device: w.device, Timing: r.env.Timing}
	if w.locks {
		o.Preflight = func() error { return preflight(w.host) }
	}
	if _, err := w.host.Clients(); !errors.Is(err, errors.ErrUnsupported) {
		o.Clients = foreignClients(w.host)
	}
	if r.writes != nil {
		wr := *r.writes
		wr.Console = func() (safety.Console, error) { return consoleOf(w.host) }
		o.Writes = &wr
	}
	return o
}

// consoleOf is the screen lock and the Secure Input holder as the preflight
// takes them; an OS without either reports neither.
func consoleOf(h Host) (safety.Console, error) {
	c, err := h.Console()
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		return safety.Console{}, nil
	case err != nil:
		return safety.Console{}, err
	}
	out := safety.Console{ScreenLocked: c.ScreenLocked}
	if c.SecureInput.PID != 0 {
		out.SecureInput = c.SecureInput.String()
	}
	return out, nil
}

// preflight refuses to open devices when the OS says access is denied.
func preflight(h Host) error {
	p, err := h.Permission()
	if err != nil || p.Access != platform.AccessDenied {
		return nil
	}
	who := "arcctl"
	if p.App.Name != "" {
		who = p.App.Name
	}
	return fmt.Errorf("%w: device access is denied to %s", fs.ErrPermission, who)
}

func foreignClients(h Host) func(hidio.Candidate) ([]session.Client, error) {
	return func(c hidio.Candidate) ([]session.Client, error) {
		devs, err := h.Clients()
		if err != nil {
			return nil, err
		}
		var out []session.Client
		for _, d := range devs {
			if !d.Matches(c.Path) {
				continue
			}
			for _, cl := range platform.Foreign(d.Clients, nil) {
				out = append(out, session.Client{PID: cl.PID, Name: cl.Name, Seized: cl.Seized})
			}
		}
		return out, nil
	}
}

// emulator builds a bus with one receiver whose mouse holds the file's image.
func (r *runner) emulator(path string) (*world, error) {
	src, err := backup.Open(path)
	if err != nil {
		return nil, err
	}
	m, err := r.modelOf(src)
	if err != nil {
		return nil, err
	}
	if m.Family != catalog.FamilyMouse {
		return nil, fail(ExitFailure, "only mice can be emulated in this build", "")
	}
	ms := &emu.Mouse{
		Model:     m,
		Image:     src.Image.Clone(),
		Firmware:  emu.Version{Major: 1},
		Battery:   emu.Battery{Level: 100, MilliVolts: 4100},
		LongRange: m.LongRangeDefault,
	}
	cfg := emu.Config{Mouse: ms}
	if f := src.File; f != nil {
		if v, ok := parseVersion(f.Device.FWMouse); ok {
			ms.Firmware = v
		}
		if v, ok := parseVersion(f.Device.FWReceiver); ok && v != (emu.Version{Major: 1}) {
			cfg.RxVersion = &v
		}
		ms.Addr = f.Identity().Addr
		if f.Device.Conn != nil {
			ms.Conn = *f.Device.Conn
		}
		if p := f.Device.Profile; p != nil && p.Supported {
			v := p.Value
			ms.Profile = &v
		}
		if f.Device.VID != 0 && catalog.Classify(uint16(f.Device.VID), uint16(f.Device.PID)) != catalog.ClassUnknown {
			cfg.VID, cfg.PID = uint16(f.Device.VID), uint16(f.Device.PID)
		}
	}
	bus := emu.New(emu.Options{})
	if _, err := bus.Add(cfg); err != nil {
		bus.Close()
		return nil, err
	}
	return &world{source: backup.SourceEmulator, devices: bus, host: emuHost{bus}, device: r.g.device, close: bus.Close}, nil
}

// modelOf picks the model of a source: the backup's own, else --model, else
// the EM11 Pro, which must then match a .bin's sensor.
func (r *runner) modelOf(src *backup.Source) (*catalog.Model, error) {
	if src.File != nil && r.g.model == "" {
		if m, ok := src.File.CatalogModel(); ok {
			return m, nil
		}
		return nil, fail(ExitFailure, "the backup's model is not in this build's catalog", "Pass --model with a model key.")
	}
	key := strings.ToUpper(r.g.model)
	if key == "" {
		key = defaultModel
	}
	m, ok := catalog.ByKey(key)
	if !ok {
		return nil, usageError("unknown model %q", r.g.model)
	}
	if src.Bin != nil && m.Sensor != nil && src.Bin.Sensor != "" && src.Bin.Sensor != m.Sensor.ID {
		return nil, fail(ExitFailure, fmt.Sprintf("the .bin is for sensor %s; %s has sensor %s", src.Bin.Sensor, m.Name, m.Sensor.ID),
			"Pass --model with the key of the model the file came from.")
	}
	return m, nil
}

const defaultModel = "7B04"

func parseVersion(s string) (emu.Version, bool) {
	major, minor, ok := strings.Cut(strings.TrimPrefix(s, "v"), ".")
	if !ok {
		return emu.Version{}, false
	}
	a, err1 := strconv.ParseUint(major, 10, 8)
	b, err2 := strconv.ParseUint(minor, 16, 8)
	if err1 != nil || err2 != nil {
		return emu.Version{}, false
	}
	return emu.Version{Major: byte(a), Minor: byte(b)}, true
}

// emuHost answers the OS questions for the emulator: no permission is needed,
// the console is free, and the clients are the bus's fake IORegistry.
type emuHost struct{ bus *emu.Bus }

func (emuHost) Permission() (platform.Permission, error) {
	return platform.Permission{Access: platform.AccessNotNeeded}, nil
}

func (emuHost) RequestPermission() (platform.Access, error) { return platform.AccessNotNeeded, nil }

func (emuHost) Console() (platform.Console, error) { return platform.Console{}, nil }

func (h emuHost) Clients() ([]platform.DeviceClients, error) {
	cands, err := h.bus.Enumerate()
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	var out []platform.DeviceClients
	for _, c := range cands {
		d := platform.DeviceClients{VID: c.VID, PID: c.PID, Interface: c.Interface, Path: c.Path}
		for _, cl := range h.bus.Clients() {
			if cl.Path == c.Path {
				d.Clients = append(d.Clients, platform.Client{
					Process: platform.Process{PID: cl.PID, Name: cl.Process}, Seized: cl.Seized, Self: cl.PID == self,
				})
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (emuHost) Diagnose(err error) platform.Diagnosis { return plainDiagnosis(err) }

// plainDiagnosis explains an error from its class alone, without asking the OS.
func plainDiagnosis(err error) platform.Diagnosis {
	d := platform.Diagnosis{Class: hidio.Classify(err)}
	d.Code, _ = hidio.IOReturn(err)
	switch d.Class {
	case hidio.ClassLocked:
		d.Summary, d.Hint = "the screen is locked or Secure Input is on", "Unlock the Mac and close any password prompt, then retry."
	case hidio.ClassPermission:
		d.Summary, d.Hint = "the OS refused access to the device", "Grant access (arcctl doctor explains how), then retry."
	case hidio.ClassSeized:
		d.Summary, d.Hint = "another process has the device open exclusively", "Quit the process that holds it, then retry."
	case hidio.ClassStalled:
		d.Summary, d.Hint = "a write never completed", "Replug the receiver; if it happens again, restart arcctl."
	case hidio.ClassTimeout:
		d.Summary = "the device did not take the report in time"
	case hidio.ClassRetry:
		d.Summary = "transient IOKit error; the write is retried"
	case hidio.ClassGone:
		d.Summary = "the device was unplugged or closed"
	case hidio.ClassNone:
	default:
		d.Summary = err.Error()
	}
	return d
}

// replayDevices offers one interface that plays a transcript back. A
// transcript does not say which device it came from, so the interface is an
// EM11 Pro receiver's.
type replayDevices struct {
	c    hidio.Candidate
	data []byte
	mu   sync.Mutex
	rp   *hidio.Replay
}

func (d *replayDevices) Enumerate() ([]hidio.Candidate, error) { return []hidio.Candidate{d.c}, nil }

func (d *replayDevices) Open(c hidio.Candidate, g *hidio.Guard, _ *hidio.Recorder) (hidio.Transport, error) {
	if c.Path != d.c.Path {
		return nil, hidio.ErrNotFound
	}
	tr, rp, err := hidio.OpenReplay(bytes.NewReader(d.data), g)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.rp = rp
	d.mu.Unlock()
	return tr, nil
}

// diverged returns the first write the replay did not expect.
func (d *replayDevices) diverged() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rp == nil {
		return nil
	}
	return d.rp.Diverged()
}

func (r *runner) replayer(path string) (*world, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if _, _, err := hidio.ReadTranscript(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	const vid, pid = 0x260D, 0x1282
	d := &replayDevices{data: data, c: hidio.Candidate{
		Backend: backup.SourceReplay, Path: "replay:" + filepath.Base(path), VID: vid, PID: pid,
		Class: catalog.Classify(vid, pid), Interface: -1, Product: "Replayed transcript",
	}}
	return &world{source: backup.SourceReplay, devices: d, host: replayHost{}, device: d.c.Path, replay: d, close: func() {}}, nil
}

type replayHost struct{}

func (replayHost) Permission() (platform.Permission, error) {
	return platform.Permission{Access: platform.AccessNotNeeded}, nil
}
func (replayHost) RequestPermission() (platform.Access, error) { return platform.AccessNotNeeded, nil }
func (replayHost) Console() (platform.Console, error)          { return platform.Console{}, nil }
func (replayHost) Clients() ([]platform.DeviceClients, error)  { return nil, nil }
func (replayHost) Diagnose(err error) platform.Diagnosis       { return plainDiagnosis(err) }
