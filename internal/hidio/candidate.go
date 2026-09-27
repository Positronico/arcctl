package hidio

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
)

const (
	DefaultBackend  = "usbhid"
	vendorUsagePage = 0xFF02
)

// Candidate is one HID interface that may be the vendor channel of a receiver
// or device. Enumerate never picks one: the session probes them all.
type Candidate struct {
	Backend      string
	Path         string
	VID, PID     uint16
	Class        catalog.PIDClass
	Interface    int // USB interface number, -1 when the backend cannot tell
	UsagePage    uint16
	Usage        uint16
	Manufacturer string
	Product      string
}

func (c Candidate) String() string {
	iface := "?"
	if c.Interface >= 0 {
		iface = strconv.Itoa(c.Interface)
	}
	return fmt.Sprintf("%04x:%04x interface %s (%s, %s) %s", c.VID, c.PID, iface, c.Class, c.Backend, c.Path)
}

type backend interface {
	enumerate() ([]Candidate, error)
	open(c Candidate) (Raw, error)
}

var backends = map[string]backend{}

// Backends lists the backends compiled into this build.
func Backends() []string {
	return slices.Sorted(maps.Keys(backends))
}

func lookup(name string) (backend, error) {
	if name == "" {
		name = DefaultBackend
	}
	b, ok := backends[name]
	if !ok {
		return nil, fmt.Errorf("%w %q (have %s)", ErrBackend, name, strings.Join(Backends(), ", "))
	}
	return b, nil
}

// Enumerate lists every interface whose VID and PID the catalog knows and,
// where the backend exposes the report descriptor at enumeration, that
// declares the vendor channel. On Windows, where each top-level collection is
// its own device, only the vendor collection (usage page 0xFF02) is kept.
func Enumerate(backendName string) ([]Candidate, error) {
	b, err := lookup(backendName)
	if err != nil {
		return nil, err
	}
	all, err := b.enumerate()
	if err != nil {
		return nil, err
	}
	return candidates(all, runtime.GOOS), nil
}

// Open opens c shared, records its traffic when rec is not nil, and returns it
// guarded by g. The device at c.Path must still have c's VID and PID, the
// catalog must know them, and its report descriptor must declare the vendor
// channel (ErrNotVendor otherwise).
func Open(c Candidate, g *Guard, rec *Recorder) (Transport, error) {
	b, err := lookup(c.Backend)
	if err != nil {
		return nil, err
	}
	if catalog.Classify(c.VID, c.PID) == catalog.ClassUnknown {
		return nil, fmt.Errorf("%w: %04x:%04x is not a device arcctl knows", ErrNotFound, c.VID, c.PID)
	}
	raw, err := b.open(c)
	if err != nil {
		return nil, err
	}
	if rec != nil {
		raw = rec.Wrap(raw)
	}
	return Guarded(raw, g), nil
}

func candidates(all []Candidate, goos string) []Candidate {
	var out []Candidate
	index := map[string]int{}
	for _, c := range all {
		c.Class = catalog.Classify(c.VID, c.PID)
		if c.Class == catalog.ClassUnknown || goos == "windows" && c.UsagePage != vendorUsagePage {
			continue
		}
		key := c.Backend + "\x00" + c.Path
		if i, ok := index[key]; ok {
			if c.UsagePage == vendorUsagePage {
				out[i].UsagePage, out[i].Usage = c.UsagePage, c.Usage
			}
			continue
		}
		index[key] = len(out)
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Candidate) int {
		return cmp.Or(
			cmp.Compare(a.VID, b.VID),
			cmp.Compare(a.PID, b.PID),
			cmp.Compare(a.Interface, b.Interface),
			strings.Compare(a.Path, b.Path),
		)
	})
	return out
}

var (
	darwinInterface  = regexp.MustCompile(`IOUSBHostInterface@([0-9]+)`)
	windowsInterface = regexp.MustCompile(`(?i)&mi_([0-9a-f]{2})`)
	sysfsInterface   = regexp.MustCompile(`:[0-9]+\.([0-9]+)/[^/]+$`)
)

// interfaceOf reads the USB interface number from a device path: the IOService
// path on macOS, the "mi_" part on Windows, and the hidraw node's sysfs parent
// on Linux. It returns -1 when the path does not say.
func interfaceOf(goos, path string) int {
	var re *regexp.Regexp
	base := 10
	switch goos {
	case "darwin":
		re = darwinInterface
	case "windows":
		re, base = windowsInterface, 16
	case "linux":
		resolved, err := filepath.EvalSymlinks(filepath.Join("/sys/class/hidraw", filepath.Base(path), "device"))
		if err != nil {
			return -1
		}
		re, path = sysfsInterface, resolved
	default:
		return -1
	}
	return interfaceMatch(re, path, base)
}

func interfaceMatch(re *regexp.Regexp, s string, base int) int {
	m := re.FindAllStringSubmatch(s, -1)
	if len(m) == 0 {
		return -1
	}
	n, err := strconv.ParseInt(m[len(m)-1][1], base, 16)
	if err != nil {
		return -1
	}
	return int(n)
}
