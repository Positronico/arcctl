//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

// pushSlot is the slot H3's push check binds: slot 3, which its Scroll Up
// case uses and reverts first, or another visible button past the left and
// right ones.
func (b *builder) pushSlot() (int, bool) {
	slot := -1
	for _, x := range b.m.Buttons {
		switch {
		case !x.Visible || x.Slot < 2:
		case x.Slot == 3:
			return 3, true
		case slot < 0:
			slot = x.Slot
		}
	}
	return slot, slot >= 0
}

// pushCase lays out H3's push check: a spare button bound to DPI Cycle, one
// press, and what came of it; then the binding is reverted and the current
// stage written back if the press moved it.
func (b *builder) pushCase() error {
	slot, ok := b.pushSlot()
	if !ok {
		return errors.New("H3's push check needs a visible button besides the left and right ones")
	}
	e, _ := mouse.KeyFnExtent(slot)
	label, action := b.button(slot)
	cycle, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle})
	if err != nil {
		return err
	}
	cur, err := b.current(e)
	if err != nil {
		return err
	}
	bound := -1
	if !bytes.Equal(cur, cycle) {
		c, err := b.change(e, cycle, fmt.Sprintf("slot %d: DPI cycle", slot), slot, mouse.FeatureSystem)
		if err != nil {
			return err
		}
		if bound, err = b.apply(fmt.Sprintf("slot %d (%s, now %s) -> DPI Cycle, for the push check", slot, label, action), c); err != nil {
			return err
		}
	}
	_, stage, err := stagesOf(b.img)
	if err != nil {
		return err
	}
	tier, err := b.tier(-1, mouse.FeatureCurrent)
	if err != nil {
		return err
	}
	pair := pairExtent(mouse.AddrCurrentDPI)
	check := "push check: the " + label + " button on DPI Cycle"
	b.custom(check, &custom{
		lines: []string{fmt.Sprintf("You press the %s button once. arcctl listens for a StatusChanged push (report 8, cmd 10) "+
			"and reads the current stage at %s again; nothing is written.", label, pair)},
		touched: []flash.Extent{pair},
		run: func(ctx context.Context, r *runner, c *conn) error {
			return r.pushCheck(ctx, c, check, label, pair)
		},
	})
	if bound >= 0 {
		if err := b.revert(fmt.Sprintf("revert: slot %d back to %s", slot, action), bound); err != nil {
			return err
		}
	}
	back := fmt.Sprintf("the current stage back to %d", stage+1)
	b.custom(back, &custom{
		lines: []string{fmt.Sprintf("When the press moved the current stage, it is written back to stage %d, as the fresh backup holds it, "+
			"at %s; otherwise nothing is written.", stage+1, pair)},
		need:    []catalog.Tier{tier},
		touched: []flash.Extent{pair},
		run: func(ctx context.Context, r *runner, c *conn) error {
			return r.stageBack(ctx, c, back, pair)
		},
	})
	return nil
}

// pushCheck has the user press a button bound to DPI Cycle, then records
// whether a StatusChanged push came and whether the current stage moved,
// read again from the mouse. A reload follows, so the session holds the
// stage as it is now, push or not.
func (r *runner) pushCheck(ctx context.Context, c *conn, title, label string, pair flash.Extent) error {
	rp, err := r.rawPath(c)
	if err != nil {
		return err
	}
	was, _, err := rp.read(ctx, pair)
	if err != nil {
		return err
	}
	stop := rp.t.follow(isCmd(wire.CmdStatusChanged))
	err = r.wait(stageID(r.def.name)+".push", fmt.Sprintf("Press the %s button once, then press Enter.", label))
	if err == nil {
		settle(ctx, r.cfg.Poll)
	}
	pushes := stop()
	if err != nil {
		return err
	}
	now, _, err := rp.read(ctx, pair)
	if err != nil {
		return err
	}
	seq := c.s.Snapshot().Seq
	if err := r.watch(ctx, c, c.s.Reload); err != nil {
		return err
	}
	if _, err := r.readyAfter(ctx, c, seq); err != nil {
		return err
	}
	push := "no report-8 StatusChanged frame"
	if len(pushes) > 0 {
		var fs []string
		for _, p := range pushes {
			fs = append(fs, fmt.Sprintf("flags %02x %02x", p.p[5], p.p[6]))
		}
		push = fmt.Sprintf("%d StatusChanged push (cmd 10, %s)", len(pushes), strings.Join(fs, "; "))
	}
	moved := "the current stage did not move"
	if !bytes.Equal(was, now) {
		moved = fmt.Sprintf("the current stage moved from %s to %s", stageName(was), stageName(now))
	}
	r.finding("DPI push: a press of the %s button bound to DPI Cycle brought %s; %s", label, push, moved)
	r.step(title, true, push, moved)
	return nil
}

func stageName(b []byte) string {
	if len(b) != 2 {
		return fmt.Sprintf("% x", b)
	}
	s, err := mouse.DecodeCurrentStage(flash.Pair{b[0], b[1]})
	if err != nil {
		return fmt.Sprintf("% x", b)
	}
	return fmt.Sprint(s + 1)
}

// stageBack writes the current stage back to the fresh backup's when the
// push check's press moved it.
func (r *runner) stageBack(ctx context.Context, c *conn, title string, pair flash.Extent) error {
	sn, err := r.ready(ctx, c)
	if err != nil {
		return err
	}
	want, _ := r.fresh.Image().Get(pair)
	if now, _ := sn.Image.Get(pair); bytes.Equal(now, want) {
		r.step(title, true, "the current stage did not move; nothing written")
		return nil
	}
	return r.putBack(ctx, c, []flash.Extent{pair}, r.fresh.Image(), title)
}
