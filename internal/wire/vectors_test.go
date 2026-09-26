package wire_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/vectors"
	"github.com/positronico/arcctl/internal/wire"
)

var mouseEdit = []wire.Policy{wire.Edit, wire.Experimental}

var vectorPolicies = map[string][]wire.Policy{
	"lt(0,4)":                           mouseEdit,
	"ND(6)":                             mouseEdit,
	"FD(2)":                             mouseEdit,
	"CurrentDPI identity (H1)":          mouseEdit,
	"H1 NAK probe":                      mouseEdit,
	"DPI 800 at stage 0":                mouseEdit,
	"DPI 4800 at stage 5":               mouseEdit,
	"Red, colour stage 0":               mouseEdit,
	"btn0 Left":                         mouseEdit,
	"btn5 DPI cycle":                    mouseEdit,
	"btn5 Cmd+C write/1":                mouseEdit,
	"btn5 Cmd+C write/2":                mouseEdit,
	"cmd 3":                             allPolicies,
	"cmd 4":                             allPolicies,
	"Read 0x60/10":                      allPolicies,
	"Restore":                           {wire.Reset},
	"Long range on":                     {wire.Experimental},
	"Offline cmd-3 reply":               nil,
	"kb cmd 3":                          allPolicies,
	"kb read 9408/10":                   allPolicies,
	"kb SyncCRC write idx 0 (512 x ff)": nil,
	"kb SyncCRC write idx 0 (counting)": nil,
}

// replyVectors are packets the device sends; Check refuses them as requests.
var replyVectors = map[string]bool{"Offline cmd-3 reply": true}

func loadVectors(t *testing.T) map[string]vectors.Vector {
	t.Helper()
	path, err := vectors.DefaultPath(".")
	if err != nil {
		t.Fatal(err)
	}
	f, err := vectors.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]vectors.Vector, len(f.Vectors))
	for _, v := range f.Vectors {
		byName[v.Name] = v
	}
	return byName
}

func isPacketVector(v vectors.Vector) bool {
	return v.Group == "packet" || v.Group == "keyboard" && v.Check == "packet"
}

func vectorBytes(t *testing.T, byName map[string]vectors.Vector, name string) []byte {
	t.Helper()
	v, ok := byName[name]
	if !ok {
		t.Fatalf("vector %q missing", name)
	}
	b, err := v.Bytes()
	if err != nil {
		t.Fatalf("vector %q: %v", name, err)
	}
	return b
}

func vectorPacket(t *testing.T, byName map[string]vectors.Vector, name string) wire.Packet {
	t.Helper()
	b := vectorBytes(t, byName, name)
	if len(b) != wire.Size {
		t.Fatalf("vector %q has %d bytes, want %d", name, len(b), wire.Size)
	}
	return wire.Packet(b)
}

func TestPacketVectors(t *testing.T) {
	byName := loadVectors(t)
	seen := 0
	for name, v := range byName {
		if !isPacketVector(v) {
			continue
		}
		seen++
		t.Run(name, func(t *testing.T) {
			pk := vectorPacket(t, byName, name)
			if pk.String() != v.Hex {
				t.Errorf("String() = %q, want %q", pk.String(), v.Hex)
			}
			valid := v.Check == "packet"
			if pk.Valid() != valid {
				t.Fatalf("Valid() = %v, want %v", pk.Valid(), valid)
			}
			rebuilt, err := wire.Build(pk.Target(), pk.Cmd(), pk.Addr(), pk.Data())
			if err != nil {
				t.Fatalf("Build from the vector's own fields: %v", err)
			}
			if !valid {
				rebuilt[wire.Size-1] = pk[wire.Size-1]
			}
			if rebuilt != pk {
				t.Fatalf("Build gives %v", rebuilt)
			}

			want, ok := vectorPolicies[name]
			if !ok {
				t.Fatalf("no policy expectation for this vector")
			}
			for _, p := range allPolicies {
				err := p.Check(pk, pk.Target())
				switch {
				case replyVectors[name]:
					if !errors.Is(err, wire.ErrFraming) {
						t.Errorf("%v: %v, want ErrFraming for a reply", p, err)
					}
				case !slices.Contains(want, p):
					if !errors.Is(err, wire.ErrForbidden) {
						t.Errorf("%v: %v, want ErrForbidden", p, err)
					}
				case valid:
					if err != nil {
						t.Errorf("%v: %v", p, err)
					}
				default:
					if !errors.Is(err, wire.ErrChecksum) {
						t.Errorf("%v: %v, want ErrChecksum", p, err)
					}
				}
				other := wire.Keyboard
				if pk.Target() == wire.Keyboard {
					other = wire.Mouse
				}
				if err := p.Check(pk, other); err == nil {
					t.Errorf("%v accepts the vector for the %v", p, other)
				}
			}
		})
	}
	if seen != len(vectorPolicies) {
		t.Errorf("%d packet vectors in the file, %d expectations", seen, len(vectorPolicies))
	}
}

func TestPacketVectorsFromArguments(t *testing.T) {
	byName := loadVectors(t)
	cmdC := vectorBytes(t, byName, "Cmd+C shortcut")
	build := func(tg wire.Target, c wire.Cmd, addr uint16, data []byte) func() (wire.Packet, error) {
		return func() (wire.Packet, error) { return wire.Build(tg, c, addr, data) }
	}
	record := func(name string) []byte { return vectorBytes(t, byName, name) }
	tests := []struct {
		name  string
		build func() (wire.Packet, error)
	}{
		{"lt(0,4)", build(wire.Mouse, wire.CmdWrite, 0, []byte{4, 0x55 - 4})},
		{"ND(6)", build(wire.Mouse, wire.CmdWrite, 2, []byte{6, 0x55 - 6})},
		{"FD(2)", build(wire.Mouse, wire.CmdWrite, 4, []byte{2, 0x55 - 2})},
		{"CurrentDPI identity (H1)", build(wire.Mouse, wire.CmdWrite, 4, []byte{3, 0x55 - 3})},
		{"DPI 800 at stage 0", build(wire.Mouse, wire.CmdWrite, 12, record("3104 DPI 800"))},
		{"DPI 4800 at stage 5", build(wire.Mouse, wire.CmdWrite, 12+4*5, record("3104 DPI 4800"))},
		{"Red, colour stage 0", build(wire.Mouse, wire.CmdWrite, 44, []byte{0xff, 0, 0, 0x56})},
		{"btn0 Left", build(wire.Mouse, wire.CmdWrite, 96, []byte{1, 1, 0, 0x53})},
		{"btn5 DPI cycle", build(wire.Mouse, wire.CmdWrite, 96+4*5, []byte{2, 1, 0, 0x52})},
		{"btn5 Cmd+C write/1", build(wire.Mouse, wire.CmdWrite, 256+32*5, cmdC[:10])},
		{"btn5 Cmd+C write/2", build(wire.Mouse, wire.CmdWrite, 256+32*5+10, cmdC[10:])},
		{"cmd 3", build(wire.Mouse, wire.CmdOnline, 0, nil)},
		{"cmd 4", build(wire.Mouse, wire.CmdBattery, 0, nil)},
		{"Read 0x60/10", func() (wire.Packet, error) { return wire.BuildRead(wire.Mouse, 0x60, 10) }},
		{"Restore", build(wire.Mouse, wire.CmdClear, 0, nil)},
		{"Long range on", build(wire.Mouse, wire.CmdSetLongRange, 0, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0})},
		{"Offline cmd-3 reply", build(wire.Mouse, wire.CmdOnline, 0, []byte{0, 0x33, 0x22, 0x11})},
		{"kb cmd 3", build(wire.Keyboard, wire.CmdOnline, 0, nil)},
		{"kb read 9408/10", func() (wire.Packet, error) { return wire.BuildRead(wire.Keyboard, 9408, 10) }},
		{"kb SyncCRC write idx 0 (512 x ff)", build(wire.Keyboard, wire.CmdWrite, 9504, record("kb SyncCRC entry (512 x ff)"))},
		{"kb SyncCRC write idx 0 (counting)", build(wire.Keyboard, wire.CmdWrite, 9504, record("kb SyncCRC entry (counting 0..511)"))},
		{"H1 NAK probe", func() (wire.Packet, error) {
			p, err := wire.Build(wire.Mouse, wire.CmdWrite, 4, []byte{3, 0x55 - 3})
			p[wire.Size-1]--
			return p, err
		}},
	}
	covered := map[string]bool{}
	for _, tt := range tests {
		covered[tt.name] = true
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.build()
			if err != nil {
				t.Fatal(err)
			}
			if want := vectorPacket(t, byName, tt.name); got != want {
				t.Errorf("got  %v\nwant %v", got, want)
			}
		})
	}
	for name, v := range byName {
		if isPacketVector(v) && !covered[name] {
			t.Errorf("packet vector %q is not rebuilt from arguments", name)
		}
	}
}

func TestOfflineReplyVector(t *testing.T) {
	byName := loadVectors(t)
	rep := vectorPacket(t, byName, "Offline cmd-3 reply")
	req := vectorPacket(t, byName, "cmd 3")
	if !wire.Match(req, rep) {
		t.Error("the offline reply does not match the cmd-3 request")
	}
	if err := rep.Status().Err(); err != nil {
		t.Errorf("status: %v", err)
	}
	if got := rep.Data(); got[0] != 0 || [3]byte(got[1:4]) != [3]byte{0x33, 0x22, 0x11} {
		t.Errorf("data % x", got)
	}
	for _, other := range []string{"cmd 4", "Read 0x60/10", "Restore"} {
		if wire.Match(vectorPacket(t, byName, other), rep) {
			t.Errorf("the cmd-3 reply matches %q", other)
		}
	}
}

func TestReadVectorMatchesItsReplies(t *testing.T) {
	byName := loadVectors(t)
	req := vectorPacket(t, byName, "kb read 9408/10")
	rep := req
	rep[4] = 0x0a
	copy(rep[5:15], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	if !wire.Match(req, rep) {
		t.Error("a keyboard read reply without the 0x80 flag does not match")
	}
	rep[3]++
	if wire.Match(req, rep) {
		t.Error("a keyboard read reply for the next address matches")
	}
	w1 := vectorPacket(t, byName, "btn5 Cmd+C write/1")
	w2 := vectorPacket(t, byName, "btn5 Cmd+C write/2")
	if wire.Match(w1, w2) || wire.Match(w2, w1) {
		t.Error("the two Cmd+C chunks match each other's acks")
	}
}
