package backup_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
)

func target(t *testing.T, dev *flash.Image) backup.Target {
	t.Helper()
	p := byte(0)
	return backup.Target{
		Model: em11(t),
		Image: dev,
		Options: mouse.Options{Device: capture(t, dev).Device, Profile: &p, Firmware: "v1.05",
			Verified: catalog.Verifications{}},
		Source: backup.SourceDevice,
	}
}

func fromFile(f *backup.File) *backup.Source {
	return &backup.Source{Kind: backup.KindBackup, File: f, Image: f.Image()}
}

func fromBin(t *testing.T, im *flash.Image) *backup.Source {
	t.Helper()
	b, err := backup.ExportPartialBin(em11(t), im)
	must(t, err)
	src, err := backup.Parse(b)
	must(t, err)
	return src
}

// fullImage knows the whole of the first 6987 bytes of richImage, erased
// slots included, as a full backup of it would.
func fullRich(t *testing.T) *flash.Image {
	t.Helper()
	im, err := flash.FromDump(0, richImage(t).Bytes()[:mouse.AddrEndEeprom])
	must(t, err)
	return im
}

// edit applies what mouse.PlanEdits writes for edits to im, as a real write
// would, and returns the extents it wrote.
func edit(t *testing.T, im *flash.Image, edits ...mouse.Edit) []flash.Extent {
	t.Helper()
	tg := target(t, im)
	p, err := mouse.PlanEdits(em11(t), im, edits, tg.Options)
	must(t, err)
	return applyPlan(t, im, p)
}

func applyPlan(t *testing.T, im *flash.Image, p plan.Plan) []flash.Extent {
	t.Helper()
	var out []flash.Extent
	for _, op := range p.Ops {
		must(t, im.Set(op.Extent.Addr, op.New))
		if !slices.Contains(out, op.Extent) {
			out = append(out, op.Extent)
		}
	}
	return out
}

func records(r *backup.Restore) map[string]backup.Fate {
	out := map[string]backup.Fate{}
	for _, x := range r.Records {
		out[x.Name] = x.Fate
	}
	return out
}

func TestSettingsRecordsCoverTheWebAppReads(t *testing.T) {
	at := 0
	for _, e := range backup.SettingsRecords() {
		if e.Addr == mouse.AddrShortcutKey {
			t.Fatal("a record starts past the settings page")
		}
		if at == mouse.AddrShortcutKey {
			at = mouse.AddrSensor3955DPI
		}
		if e.Addr != at {
			t.Fatalf("record %v leaves a gap or overlaps after %d", e, at)
		}
		at = e.End()
	}
	if at != mouse.AddrEndEeprom {
		t.Errorf("the records end at %d, want %d", at, mouse.AddrEndEeprom)
	}
}

// Restoring a backup of the mouse as it is writes nothing (§6.6).
func TestRestoreOwnBackupWritesNothing(t *testing.T) {
	for name, im := range map[string]*flash.Image{"loaded": richImage(t), "full": fullRich(t)} {
		r, err := backup.PlanRestore(fromFile(newFile(t, im)), target(t, im.Clone()), backup.RestoreOptions{})
		must(t, err)
		if len(r.Plan.Ops) != 0 || len(r.Records) != 0 {
			t.Errorf("%s: %d ops, records %v", name, len(r.Plan.Ops), records(r))
		}
		if r.Equal == 0 {
			t.Errorf("%s: nothing compared", name)
		}
	}
}

// A restore lists exactly the records that changed since the backup and
// writes back the ones arcctl writes, and nothing else.
func TestRestoreDryRunIsExact(t *testing.T) {
	b0 := fullRich(t)
	edit(t, b0, mouse.SetMacro{Slot: 5, Macro: macroAB, Cycle: 1})
	dev := b0.Clone()
	longer := mouse.Macro{Name: "a longer one", Events: slices.Repeat([]mouse.Event{
		{Press: true, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x04}, Delay: 30},
		{Press: false, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x04}, Delay: 30},
	}, 5)}
	changed := edit(t, dev,
		mouse.SetDPI{Stage: 1, DPI: 1600},
		mouse.SetStages{Count: 5},
		mouse.SetShortcut{Slot: 4, Combo: keys.Combo{keys.LMeta.Stroke(), keys.LShift.Stroke(), {Kind: keys.KindKey, Value: 0x2B}}},
		mouse.SetMedia{Slot: 2, Usage: 0xB5},
		mouse.SetMacro{Slot: 5, Macro: longer, Cycle: 2},
		mouse.SetMacro{Slot: 1, Macro: mouse.Macro{Name: "x", Events: macroAB.Events[:2]}, Cycle: 1},
	)
	col, _ := mouse.ColorExtent(0)
	must(t, dev.Set(col.Addr, mouse.EncodeColor([3]byte{1, 2, 3})))
	must(t, dev.Set(84, []byte{0x00, 0x55}))
	sleep := flash.NewPair(6)
	must(t, dev.Set(mouse.AddrSleepTime, sleep[:]))

	r, err := backup.PlanRestore(fromFile(newFile(t, b0)), target(t, dev.Clone()), backup.RestoreOptions{})
	must(t, err)
	want := map[string]backup.Fate{
		"DPI stage 2":    backup.FateWrite,
		"DPI stages":     backup.FateWrite,
		"Button slot 1":  backup.FateWrite,
		"Button slot 5":  backup.FateWrite,
		"Shortcut 2":     backup.FateWrite,
		"Shortcut 4":     backup.FateWrite,
		"Macro 1":        backup.FateWrite,
		"Macro 5":        backup.FateWrite,
		"Colour 1":       backup.FateRefused,
		"SleepTime @173": backup.FateRefused,
		"unmapped @84":   backup.FateUnknown,
	}
	if got := records(r); !mapsEqual(got, want) {
		t.Errorf("records\n%v\nwant\n%v", got, want)
	}
	for _, x := range r.Records {
		if x.Fate != backup.FateWrite && x.Why == "" {
			t.Errorf("%s: %s without a reason", x.Name, x.Fate)
		}
	}
	after := dev.Clone()
	wrote := applyPlan(t, after, r.Plan)
	for _, x := range r.Records {
		got, _ := after.Get(x.Extent)
		switch {
		case x.Fate == backup.FateWrite && !bytes.Equal(got, x.Source):
			t.Errorf("%s holds % x after the restore, want % x", x.Name, got, x.Source)
		case x.Fate != backup.FateWrite && !bytes.Equal(got, x.Device):
			t.Errorf("%s was written: % x", x.Name, got)
		}
	}
	for _, e := range changed {
		if !slices.ContainsFunc(wrote, func(w flash.Extent) bool { return w.Addr == e.Addr }) {
			t.Errorf("%v changed since the backup and is not restored", e)
		}
	}
	for a := range mouse.AddrEndEeprom {
		x, _ := after.Byte(a)
		y, _ := dev.Byte(a)
		inWrite := slices.ContainsFunc(wrote, func(e flash.Extent) bool { return e.Contains(flash.Extent{Addr: a, Len: 1}) })
		if x != y && !inWrite {
			t.Fatalf("byte %d changed outside the plan's extents", a)
		}
	}
	for _, e := range changed {
		if slot := e.Addr; slot >= mouse.AddrMacro {
			continue
		}
		got, _ := after.Get(e)
		want, _ := b0.Get(e)
		if !bytes.Equal(got, want) {
			t.Errorf("%v holds % x, the backup % x", e, got, want)
		}
	}
	macro5, _ := mouse.MacroExtent(5)
	got, _ := after.Get(macro5)
	want5, _ := b0.Get(macro5)
	if !bytes.Equal(got, want5) {
		t.Error("macro slot 5 differs from the backup: the tail of the longer macro outlived the restore")
	}
	macro1, _ := mouse.MacroExtent(1)
	if got, _ := after.Get(macro1); !bytes.Equal(got, bytes.Repeat([]byte{0xFF}, macro1.Len)) {
		t.Error("macro slot 1 was not emptied again")
	}

	r2, err := backup.PlanRestore(fromFile(newFile(t, b0)), target(t, after), backup.RestoreOptions{})
	must(t, err)
	if len(r2.Plan.Ops) != 0 {
		t.Errorf("a second restore plans %d ops", len(r2.Plan.Ops))
	}
}

func mapsEqual(a, b map[string]backup.Fate) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestRestoreChecksTheDeviceAndProfile(t *testing.T) {
	im := richImage(t)
	own := newFile(t, im)
	other := capture(t, im)
	other.Device.Addr = [3]byte{0x44, 0x55, 0x66}
	other.Device.AddrTrusted = true
	otherFile, err := backup.New(other, backup.Meta{Tool: "arcctl test", Created: created})
	must(t, err)
	profile1 := capture(t, im)
	profile1.Profile.Value = 1
	profileFile, err := backup.New(profile1, backup.Meta{Tool: "arcctl test", Created: created})
	must(t, err)
	emulated, err := backup.New(capture(t, im), backup.Meta{Tool: "arcctl test", Created: created, Source: backup.SourceEmulator})
	must(t, err)
	em16 := capture(t, im)
	em16.Model, _ = catalog.ByKey("7B05")
	em16.Device.MID = 5
	em16File, err := backup.New(em16, backup.Meta{Tool: "arcctl test", Created: created})
	must(t, err)
	bin := fromBin(t, im)
	untrusted := capture(t, im)
	untrusted.Device.Addr = [3]byte{0x44, 0x55, 0x66}
	untrustedFile, err := backup.New(untrusted, backup.Meta{Tool: "arcctl test", Created: created})
	must(t, err)

	tests := []struct {
		name string
		src  *backup.Source
		o    backup.RestoreOptions
		tg   func(*backup.Target)
		want error
	}{
		{"own backup", fromFile(own), backup.RestoreOptions{}, nil, nil},
		{"another device", fromFile(otherFile), backup.RestoreOptions{}, nil, backup.ErrOtherDevice},
		{"another device allowed", fromFile(otherFile), backup.RestoreOptions{OtherDevice: true}, nil, nil},
		{"another address, untrusted", fromFile(untrustedFile), backup.RestoreOptions{}, nil, backup.ErrOtherDevice},
		{"another address, untrusted, allowed", fromFile(untrustedFile), backup.RestoreOptions{OtherDevice: true}, nil, nil},
		{"another model", fromFile(em16File), backup.RestoreOptions{OtherDevice: true, OtherProfile: true}, nil, backup.ErrOtherModel},
		{"another profile", fromFile(profileFile), backup.RestoreOptions{}, nil, backup.ErrOtherProfile},
		{"another profile allowed", fromFile(profileFile), backup.RestoreOptions{OtherProfile: true}, nil, nil},
		{"no profiles now", fromFile(own), backup.RestoreOptions{}, func(tg *backup.Target) { tg.Options.Profile = nil }, backup.ErrOtherProfile},
		{"emulated backup", fromFile(emulated), backup.RestoreOptions{OtherDevice: true, OtherProfile: true}, nil, backup.ErrNotSource},
		{"emulated backup to the emulator", fromFile(emulated), backup.RestoreOptions{}, func(tg *backup.Target) { tg.Source = backup.SourceEmulator }, nil},
		{"web .bin", bin, backup.RestoreOptions{}, nil, backup.ErrOtherDevice},
		{"web .bin allowed", bin, backup.RestoreOptions{OtherDevice: true}, nil, backup.ErrOtherProfile},
		{"web .bin on profiles", bin, backup.RestoreOptions{OtherDevice: true, OtherProfile: true}, nil, nil},
		{"web .bin without profiles", bin, backup.RestoreOptions{OtherDevice: true}, func(tg *backup.Target) { tg.Options.Profile = nil }, nil},
		{"dump", &backup.Source{Kind: backup.KindDump, Image: im}, backup.RestoreOptions{OtherDevice: true, OtherProfile: true}, nil, backup.ErrNotSource},
	}
	for _, tt := range tests {
		tg := target(t, im.Clone())
		if tt.tg != nil {
			tt.tg(&tg)
		}
		r, err := backup.PlanRestore(tt.src, tg, tt.o)
		switch {
		case !errors.Is(err, tt.want):
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		case err == nil && strings.HasSuffix(tt.name, "allowed") && len(r.Notes) == 0:
			t.Errorf("%s: no note says what the option allowed", tt.name)
		}
	}
}

// A web .bin exported by arcctl restores exactly the records that changed,
// and never a record it holds as 0xFF.
func TestRestoreFromBin(t *testing.T) {
	im := fullRich(t)
	o := backup.RestoreOptions{OtherDevice: true, OtherProfile: true}
	r, err := backup.PlanRestore(fromBin(t, im), target(t, im.Clone()), o)
	must(t, err)
	if len(r.Plan.Ops) != 0 || len(r.Records) != 0 {
		t.Fatalf("round trip: %d ops, records %v", len(r.Plan.Ops), records(r))
	}

	dev := im.Clone()
	edit(t, dev, mouse.SetDPI{Stage: 2, DPI: 2000}, mouse.SetKey{Slot: 3, Fn: mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamBack}})
	erased := im.Clone()
	dpi4, _ := mouse.DPIExtent(3)
	must(t, erased.Set(dpi4.Addr, []byte{0xFF, 0xFF, 0xFF, 0xFF}))
	must(t, erased.Set(84, bytes.Repeat([]byte{0xFF}, 12)))
	r, err = backup.PlanRestore(fromBin(t, erased), target(t, dev.Clone()), backup.RestoreOptions{OtherDevice: true, OtherProfile: true, IncludeUnknown: true})
	must(t, err)
	want := map[string]backup.Fate{
		"DPI stage 3":   backup.FateWrite,
		"Button slot 3": backup.FateWrite,
		"DPI stage 4":   backup.FateNotCaptured,
		"unmapped @84":  backup.FateNotCaptured,
	}
	if got := records(r); !mapsEqual(got, want) {
		t.Errorf("records\n%v\nwant\n%v", got, want)
	}
	if n := len(r.Captured()); n != 0 {
		t.Errorf("--include-unknown writes back %d records the .bin never captured", n)
	}
	for _, op := range r.Plan.Ops {
		if op.Extent.Overlaps(dpi4) || op.Extent.Overlaps(flash.Extent{Addr: 84, Len: 12}) {
			t.Errorf("op %d writes %v, which the .bin did not capture", op.Seq, op.Extent)
		}
	}
}

func TestClampBin(t *testing.T) {
	im := dumpImage(t)
	for addr, v := range map[int]byte{mouse.AddrReportRate: 16, mouse.AddrMaxDpiStage: 8, mouse.AddrCurrentDPI: 7} {
		p := flash.NewPair(v)
		must(t, im.Set(addr, p[:]))
	}
	conn := byte(0)
	got := backup.ClampBin(em11(t), &conn, im)
	want := map[int]byte{mouse.AddrReportRate: 1, mouse.AddrMaxDpiStage: 6, mouse.AddrCurrentDPI: 5}
	if len(got) != len(want) {
		t.Fatalf("clamps %+v", got)
	}
	for addr, v := range want {
		b, _ := im.Get(flash.Extent{Addr: addr, Len: 2})
		if b[0] != v || b[0]+b[1] != 0x55 {
			t.Errorf("@%d holds % x, want %02x with its complement", addr, b, v)
		}
	}
	if got := backup.ClampBin(em11(t), nil, im); len(got) != 0 {
		t.Errorf("clamped again: %+v", got)
	}
}

func TestRestoreKeepsTheLastLeftClick(t *testing.T) {
	b0 := richImage(t)
	key0, _ := mouse.KeyFnExtent(0)
	disable := []byte{0x00, 0x00, 0x00, 0x55}
	must(t, b0.Set(key0.Addr, disable))
	r, err := backup.PlanRestore(fromFile(newFile(t, b0)), target(t, richImage(t)), backup.RestoreOptions{})
	must(t, err)
	if got := records(r)["Button slot 0"]; got != backup.FateRefused {
		t.Errorf("slot 0 is %v", got)
	}
	if len(r.Plan.Ops) != 0 {
		t.Errorf("%d ops", len(r.Plan.Ops))
	}
}

func TestRestoreRefusesBindingsToBodiesItCannotWrite(t *testing.T) {
	b0 := richImage(t)
	fn, err := mouse.MacroBinding(4, 1)
	must(t, err)
	rec, err := mouse.EncodeKeyFn(fn)
	must(t, err)
	key4, _ := mouse.KeyFnExtent(4)
	must(t, b0.Set(key4.Addr, rec))
	r, err := backup.PlanRestore(fromFile(newFile(t, b0)), target(t, richImage(t)), backup.RestoreOptions{})
	must(t, err)
	if got := records(r)["Button slot 4"]; got != backup.FateRefused {
		t.Errorf("slot 4 is %v: the backup never read macro 4", got)
	}
	if len(r.Plan.Ops) != 0 {
		t.Errorf("%d ops", len(r.Plan.Ops))
	}
}

func TestRestoreReads(t *testing.T) {
	full := fullRich(t)
	macro3, _ := mouse.MacroExtent(3)
	dev, err := flash.FromDump(0, full.Bytes()[:256])
	must(t, err)
	for _, e := range []flash.Extent{{Addr: 256, Len: 512}, macro3} {
		b, _ := full.Get(e)
		must(t, dev.Set(e.Addr, b))
	}
	longer := full.Clone()
	e5, _ := mouse.MacroExtent(5)
	mb, err := mouse.EncodeMacro(macroAB)
	must(t, err)
	must(t, longer.Set(e5.Addr, mb))
	rounds := 0
	for {
		need := backup.RestoreReads(full, dev)
		if len(need) == 0 {
			break
		}
		if rounds++; rounds > 3 {
			t.Fatalf("still reading %v", need)
		}
		for _, e := range need {
			b, _ := longer.Get(e)
			must(t, dev.Set(e.Addr, b))
		}
	}
	if rounds != 2 {
		t.Errorf("%d rounds, want 2: the headers, then the records", rounds)
	}
	if !dev.Known(flash.Extent{Addr: e5.Addr, Len: len(mb)}) {
		t.Error("the mouse's longer macro 5 was not read, so its tail would outlive the restore")
	}
	tg := target(t, dev)
	r, err := backup.PlanRestore(fromFile(newFile(t, full)), tg, backup.RestoreOptions{})
	must(t, err)
	if got := records(r); !mapsEqual(got, map[string]backup.Fate{"Macro 5": backup.FateWrite}) {
		t.Errorf("records %v, want macro 5 emptied again", got)
	}
}

// Whatever a web .bin holds, a restore from it writes only bytes it
// captured, as whole records, and plans only what validates.
func FuzzRestoreFromBin(f *testing.F) {
	f.Add(richImage(f).Bytes()[:mouse.AddrShortcutKey+64], uint16(0))
	f.Add([]byte{0x02, 0xAB}, uint16(mouse.AddrKeyFunction+12))
	f.Add([]byte("000\x06O"), uint16(1))
	f.Fuzz(func(t *testing.T, b []byte, at uint16) {
		im := richImage(t)
		if int(at)+len(b) > mouse.AddrEndEeprom {
			return
		}
		must(t, im.Set(int(at), b))
		src := fromBin(t, im)
		tg := target(t, fullRich(t))
		r, err := backup.PlanRestore(src, tg, backup.RestoreOptions{OtherDevice: true, OtherProfile: true, IncludeUnknown: true})
		if err != nil {
			return
		}
		captured, _ := backup.RestoreImage(src, tg)
		for _, op := range r.Plan.Ops {
			now, _ := tg.Image.Get(op.Extent)
			if op.Phase == plan.Neutralise || bytes.Equal(op.New, now) {
				continue
			}
			if b, ok := captured.Get(op.Extent); !ok || !bytes.Equal(b, op.New) {
				t.Fatalf("op %d writes %v, which the .bin did not capture as % x", op.Seq, op.Extent, op.New)
			}
		}
		if err := r.Plan.Validate(tg.Image, mouse.Layout(tg.Model)); err != nil {
			t.Fatalf("the plan does not validate: %v", err)
		}
	})
}

// Slot 8 of richImage runs macro 3 and, by the planner's rule, macro 8,
// which is empty: rewriting macro 3 would leave slot 8 unable to be bound
// again, so the restore leaves macro 3 alone instead of failing.
func TestRestoreLeavesBodiesItCannotRebind(t *testing.T) {
	b0 := fullRich(t)
	dev := b0.Clone()
	e, _ := mouse.MacroExtent(3)
	mb, err := mouse.EncodeMacro(mouse.Macro{Name: "ab", Events: macroAB.Events[:2]})
	must(t, err)
	must(t, dev.Set(e.Addr, mb))
	r, err := backup.PlanRestore(fromFile(newFile(t, b0)), target(t, dev), backup.RestoreOptions{})
	must(t, err)
	if got := records(r); !mapsEqual(got, map[string]backup.Fate{"Macro 3": backup.FateRefused}) {
		t.Errorf("records %v", got)
	}
	if !strings.Contains(r.Records[0].Why, "macro 8") {
		t.Errorf("why: %s", r.Records[0].Why)
	}
}

// §3.3 and I8: H6 promotes the restore itself. Until its record exists,
// every restored record is Untested, whatever the stages of its own
// features recorded, so a restore needs the typed phrase.
func TestRestoreTakesTheRestoreTier(t *testing.T) {
	b0 := fullRich(t)
	dev := b0.Clone()
	edit(t, dev, mouse.SetDPI{Stage: 2, DPI: 2000}, mouse.SetKey{Slot: 3, Fn: mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamBack}},
		mouse.SetMacro{Slot: 5, Macro: macroAB, Cycle: 1})
	record := func(fs ...mouse.Feature) catalog.Verifications {
		var vs catalog.Verifications
		for _, f := range fs {
			vs = append(vs, catalog.Verification{Model: "7B04", Feature: string(f), Firmware: "v1.05", Stage: "H2", Date: "2026-10-01"})
		}
		return vs
	}
	own := []mouse.Feature{mouse.FeatureDPI, mouse.FeatureSystem, mouse.FeatureStages, mouse.FeatureCurrent, mouse.FeatureShortcut,
		mouse.FeatureMedia, mouse.FeatureMacro}
	for _, tc := range []struct {
		name string
		vs   catalog.Verifications
		want catalog.Tier
	}{
		{"no records", nil, catalog.Untested},
		{"H1 to H5 but no H6", record(own...), catalog.Untested},
		{"H6 but not the features", record(mouse.FeatureRestore), catalog.Untested},
		{"H1 to H6", record(append(own, mouse.FeatureRestore)...), catalog.Verified},
	} {
		tg := target(t, dev.Clone())
		tg.Options.Verified = tc.vs
		r, err := backup.PlanRestore(fromFile(newFile(t, b0)), tg, backup.RestoreOptions{})
		must(t, err)
		if len(r.Plan.Ops) < 3 {
			t.Fatalf("%s: %d ops", tc.name, len(r.Plan.Ops))
		}
		for _, op := range r.Plan.Ops {
			if op.Tier != tc.want {
				t.Errorf("%s: op %d (%s) is %v, want %v", tc.name, op.Seq, op.Desc, op.Tier, tc.want)
			}
		}
		for _, x := range r.Records {
			if x.Fate == backup.FateWrite && x.Tier != tc.want {
				t.Errorf("%s: %s is %v, want %v", tc.name, x.Name, x.Tier, tc.want)
			}
		}
	}
}

// What a restore never writes back follows the model, the layout and the
// records in force.
func TestRestoreLeavesOut(t *testing.T) {
	m := em11(t)
	got := strings.Join(backup.RestoreLeavesOut(m, mouse.Options{Firmware: "v1.05"}), ", ")
	for _, want := range []string{"the report rate, the DPI colours, DPI stages 7 to 8, button slots 6 to 15", "KeyOperation", "LOD",
		"SleepTime", "the bytes arcctl knows no field for"} {
		if !strings.Contains(got, want) {
			t.Errorf("leaves out %q, want %q in it", got, want)
		}
	}
	vs := catalog.Verifications{{Model: m.Key, Feature: "setting.lod", Firmware: "v1.05", Stage: "H8", Date: "2026-10-01"}}
	if got := strings.Join(backup.RestoreLeavesOut(m, mouse.Options{Firmware: "v1.05", Verified: vs}), ", "); strings.Contains(got, "LOD") {
		t.Errorf("with LOD verified it still leaves out %q", got)
	}
}
