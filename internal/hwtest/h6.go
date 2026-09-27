//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
)

// The H6 slots: a system function on Backward, another shortcut body on
// Forward (two-phase), a macro bound to the DPI button's slot.
const (
	h6SystemSlot   = 3
	h6ShortcutSlot = 4
	h6MacroSlot    = 5
)

const usageF13 = 0x68

var h6Macro = taps("arcctl H6", 20, usageH, usage5)

func h6Reads() []flash.Extent {
	e, _ := mouse.MacroExtent(h6MacroSlot)
	return []flash.Extent{e}
}

// H6: five changes of five kinds in one apply; a restore of the fresh
// backup whose dry run must list exactly the records they changed; the
// restore; a full read from a new session that must equal the backup.
func buildH6(b *builder) error {
	if b.src == nil {
		return errors.New("H6 needs its fresh backup as the restore source")
	}
	b0 := b.img.Clone()
	count, cur, err := stagesOf(b.img)
	if err != nil {
		return err
	}
	if count < 2 {
		return fmt.Errorf("H6 changes the current stage, which needs two DPI stages; the mouse has %d", count)
	}
	to := cur - 1
	if cur == 0 {
		to = 1
	}
	pr, err := mouse.EncodeCurrentStage(to)
	if err != nil {
		return err
	}
	var changes []plan.Change
	add := func(c plan.Change, err error) error {
		changes = append(changes, c)
		return err
	}
	if err := add(b.change(pairExtent(mouse.AddrCurrentDPI), pr[:], fmt.Sprintf("current stage: %d", to+1), -1, mouse.FeatureCurrent)); err != nil {
		return err
	}
	e, rec, dpi, err := b.dpiRecord(0, 900, 800)
	if err != nil {
		return err
	}
	if err := add(b.change(e, rec, fmt.Sprintf("DPI stage 1: %d", dpi), -1, mouse.FeatureDPI)); err != nil {
		return err
	}
	sysLabel, sysWas := b.button(h6SystemSlot)
	was := fmt.Sprintf("Before the stage %s ran %s, %s ran %s and %s ran %s.", sysLabel, b.was(h6SystemSlot),
		b.label(h6ShortcutSlot), b.was(h6ShortcutSlot), b.label(h6MacroSlot), b.was(h6MacroSlot))
	if err := add(b.systemChange(h6SystemSlot)); err != nil {
		return err
	}
	scLabel, scWas := b.button(h6ShortcutSlot)
	scChanges, err := b.shortcutChange(h6ShortcutSlot)
	if err != nil {
		return err
	}
	changes = append(changes, scChanges...)
	macLabel, macWas := b.button(h6MacroSlot)
	body, err := b.macroChange(h6MacroSlot, h6Macro, fmt.Sprintf("macro %d: %q", h6MacroSlot, h6Macro.Name))
	if err != nil {
		return err
	}
	changes = append(changes, body)
	fn, err := mouse.MacroBinding(h6MacroSlot, 1)
	if err != nil {
		return err
	}
	bind, err := mouse.EncodeKeyFn(fn)
	if err != nil {
		return err
	}
	be, _ := mouse.KeyFnExtent(h6MacroSlot)
	if err := add(b.change(be, bind, fmt.Sprintf("slot %d: macro %d, once", h6MacroSlot, h6MacroSlot), h6MacroSlot, mouse.FeatureMacro)); err != nil {
		return err
	}
	if _, err := b.apply("five changes: the current stage, DPI stage 1, "+sysLabel+"'s function, "+scLabel+"'s shortcut, a macro on "+macLabel, changes...); err != nil {
		return err
	}

	want := changedExtents(b0, b.img)
	rs, err := backup.PlanRestore(b.src, backup.Target{Model: b.m, Image: b.img, Options: b.opt}, backup.RestoreOptions{})
	if err != nil {
		return fmt.Errorf("the restore of the fresh backup: %w", err)
	}
	if err := exactly(rs, want); err != nil {
		return fmt.Errorf("H6: the restore does not cover the five changes: %w", err)
	}
	st := &h6State{src: b.src, want: want, preview: rs.Plan}
	var touched []flash.Extent
	for _, op := range rs.Plan.Ops {
		touched = append(touched, op.Extent)
	}
	b.custom("restore the fresh backup: its dry run lists exactly the changed records, then it runs", &custom{
		plans:   []plan.Plan{rs.Plan},
		lines:   append([]string{"The restore is planned again on the mouse when the step runs; it must list exactly:"}, indent(recordNames(rs))...),
		touched: touched,
		run: func(ctx context.Context, r *runner, c *conn) error {
			return r.restoreB0(ctx, c, st)
		},
	})
	if b.img, err = applyPlan(b.img, rs.Plan); err != nil {
		return err
	}
	b.custom("a full read from a new session equals the fresh backup", &custom{
		run: func(ctx context.Context, r *runner, c *conn) error {
			return r.readEqualsBackup(ctx, c, st)
		},
	})
	b.askAside("buttons", fmt.Sprintf("Press %s, %s and %s, and move the pointer. Does each button do what it did before the stage (%s: %s; %s: %s; %s: %s), "+
		"and the pointer move at its old speed?", sysLabel, scLabel, macLabel, sysLabel, sysWas, scLabel, scWas, macLabel, macWas), was, true)
	return nil
}

type h6State struct {
	src     *backup.Source
	want    []flash.Extent
	preview plan.Plan
}

func indent(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = "  " + l
	}
	return out
}

// systemChange puts Scroll Up on slot, or Scroll Down when it holds Scroll
// Up already.
func (b *builder) systemChange(slot int) (plan.Change, error) {
	e, _ := mouse.KeyFnExtent(slot)
	fn := mouse.KeyFn{Type: mouse.TypeWheel, Param: mouse.ParamScrollUp}
	rec, err := mouse.EncodeKeyFn(fn)
	if err != nil {
		return plan.Change{}, err
	}
	if cur, _ := b.img.Get(e); bytes.Equal(cur, rec) {
		fn.Param = mouse.ParamScrollDown
		if rec, err = mouse.EncodeKeyFn(fn); err != nil {
			return plan.Change{}, err
		}
	}
	return b.change(e, rec, fmt.Sprintf("slot %d: scroll", slot), slot, mouse.FeatureSystem)
}

// shortcutChange gives slot the shortcut Ctrl+F13, or Ctrl+F14 when it
// holds that already, and binds it to its shortcut when it runs another
// function.
func (b *builder) shortcutChange(slot int) ([]plan.Change, error) {
	combo := keys.Combo{keys.LCtrl.Stroke(), {Kind: keys.KindKey, Value: usageF13}}
	body, err := mouse.EncodeShortcut(combo)
	if err != nil {
		return nil, err
	}
	e, _ := mouse.ShortcutExtent(slot)
	if cur, _ := b.img.Get(flash.Extent{Addr: e.Addr, Len: len(body)}); bytes.Equal(cur, body) {
		combo[1].Value++
		if body, err = mouse.EncodeShortcut(combo); err != nil {
			return nil, err
		}
	}
	if _, err := b.current(flash.Extent{Addr: e.Addr, Len: len(body)}); err != nil {
		return nil, err
	}
	c, err := b.change(e, body, fmt.Sprintf("shortcut %d", slot), slot, mouse.FeatureShortcut)
	if err != nil {
		return nil, err
	}
	out := []plan.Change{c}
	ke, _ := mouse.KeyFnExtent(slot)
	bind, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeShortcut})
	if err != nil {
		return nil, err
	}
	if cur, _ := b.img.Get(ke); !bytes.Equal(cur, bind) {
		c, err := b.change(ke, bind, fmt.Sprintf("slot %d: shortcut", slot), slot, mouse.FeatureShortcut)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// changedExtents are the records of the layout, whole bindings and whole
// declared bodies, where a and b differ.
func changedExtents(a, b *flash.Image) []flash.Extent {
	var out []flash.Extent
	for _, e := range backup.SettingsRecords() {
		x, _ := a.Get(e)
		y, _ := b.Get(e)
		if !bytes.Equal(x, y) {
			out = append(out, e)
		}
	}
	for k := range mouse.Slots {
		for _, table := range []func(int) (flash.Extent, bool){mouse.ShortcutExtent, mouse.MacroExtent} {
			s, _ := table(k)
			e := bodySpan(flash.Extent{Addr: s.Addr, Len: 1}, a, b)
			x, _ := a.Get(e)
			y, _ := b.Get(e)
			if !bytes.Equal(x, y) {
				out = append(out, e)
			}
		}
	}
	slices.SortFunc(out, func(x, y flash.Extent) int { return x.Addr - y.Addr })
	return out
}

// exactly checks that rs writes every extent of want and lists no other
// record.
func exactly(rs *backup.Restore, want []flash.Extent) error {
	var got []flash.Extent
	var bad []string
	for _, x := range rs.Records {
		got = append(got, x.Extent)
		if x.Fate != backup.FateWrite {
			bad = append(bad, fmt.Sprintf("%s at %s: %s (%s)", x.Name, x.Extent, x.Fate, x.Why))
		}
	}
	slices.SortFunc(got, func(x, y flash.Extent) int { return x.Addr - y.Addr })
	if !slices.Equal(got, want) {
		bad = append(bad, fmt.Sprintf("it lists %v, the changes are %v", got, want))
	}
	if len(bad) > 0 {
		return errors.New(strings.Join(bad, "; "))
	}
	return nil
}

func applyPlan(im *flash.Image, p plan.Plan) (*flash.Image, error) {
	out := im.Clone()
	for _, op := range p.Ops {
		if err := out.Set(op.Extent.Addr, op.New); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// restoreB0 plans the restore of the fresh backup on the mouse, checks
// that its dry run lists exactly the changed records and sends what the
// preview showed, writes it, and plans it again, which must write nothing.
func (r *runner) restoreB0(ctx context.Context, c *conn, st *h6State) error {
	rs, err := r.planRestore(ctx, c, st.src)
	if err != nil {
		return err
	}
	title := "restore --dry-run lists exactly the changed records"
	if err := exactly(rs, st.want); err != nil {
		r.step(title, false, append(recordNames(rs), err.Error())...)
		return err
	}
	dry, err := c.s.Apply(ctx, rs.Plan, safety.Gates{DryRun: true}, nil)
	switch {
	case err != nil:
	case !slices.Equal(dry.Packets, packets(rs.Plan)):
		err = fmt.Errorf("the dry run sent %s, the plan has %s", shownPackets(dry.Packets), shownPackets(packets(rs.Plan)))
	case !slices.Equal(dry.Packets, packets(st.preview)):
		err = fmt.Errorf("the dry run sent %s, the preview showed %s", shownPackets(dry.Packets), shownPackets(packets(st.preview)))
	}
	if err != nil {
		r.step(title, false, err.Error())
		return err
	}
	r.step(title, true, append(recordNames(rs), fmt.Sprintf("%d ops, %d packets, as the preview showed", len(rs.Plan.Ops), len(dry.Packets)))...)
	if _, err := r.write(ctx, c, "restore the fresh backup", rs.Plan, r.event); err != nil {
		return err
	}
	again, err := r.planRestore(ctx, c, st.src)
	if err == nil && (len(again.Plan.Ops) > 0 || len(again.Records) > 0) {
		err = fmt.Errorf("it still lists %s", strings.Join(recordNames(again), "; "))
	}
	if err != nil {
		r.step("the restore planned again writes nothing", false, err.Error())
		return err
	}
	r.step("the restore planned again writes nothing", true)
	return nil
}

// readEqualsBackup reads everything a full backup covers from a new session
// and compares it with the fresh backup, byte for byte.
func (r *runner) readEqualsBackup(ctx context.Context, c *conn, st *h6State) error {
	title := "a full read equals the fresh backup"
	f, err := r.fullRead(ctx, c, "hwtest "+r.def.name+" after")
	if err != nil {
		r.step(title, false, err.Error())
		return err
	}
	a, b := st.src.Image, f.Image()
	if ka, kb := a.KnownExtents(), b.KnownExtents(); !slices.Equal(ka, kb) {
		err := fmt.Errorf("the full read covers %v, the backup %v", kb, ka)
		r.step(title, false, err.Error())
		return err
	}
	var diff []string
	for _, e := range a.KnownExtents() {
		for _, d := range diffRuns(a, b, e) {
			diff = append(diff, d.String())
		}
	}
	if len(diff) > 0 {
		err := fmt.Errorf("%d byte runs differ: %s", len(diff), strings.Join(diff, ", "))
		r.step(title, false, err.Error())
		return err
	}
	r.step(title, true, fmt.Sprintf("%d bytes over %d ranges, all equal", f.Known(), len(a.KnownExtents())))
	return nil
}
