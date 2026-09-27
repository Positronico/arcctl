//go:build hwtest

package cli_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/cli"
	"github.com/positronico/arcctl/internal/wire"
)

const hwtestBuild = true

var previewPacket = regexp.MustCompile(`(?m)^ +(?:raw|cmd7) +((?:[0-9a-f]{2} ){15}[0-9a-f]{2})`)

func TestHWTestDryRun(t *testing.T) {
	h := newHarness(t)
	d := h.add(receiver(em11Mouse(t, dumpImage(t, h.root))))
	out, errs, code := h.run("hwtest", "--stage", "H1", "--dry-run")
	expect(t, code, cli.ExitOK, errs)
	var got []string
	for _, m := range previewPacket.FindAllStringSubmatch(out, -1) {
		got = append(got, m[1])
	}
	want := []string{
		"07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 eb",
		"07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 ea",
		"07 00 00 04 02 02 53 00 00 00 00 00 00 00 00 eb",
		"07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 eb",
	}
	if !slices.Equal(got, want) {
		t.Errorf("preview packets\n got %v\nwant %v\n%s", got, want, out)
	}
	if !strings.Contains(out, "Dry run: no backup was taken and nothing was written.") {
		t.Errorf("stdout:\n%s", out)
	}
	for _, w := range d.Writes() {
		if w.Packet.Cmd() == wire.CmdWrite {
			t.Errorf("cmd 7 reached the device: %v", w.Packet)
		}
	}
}

func TestHWTestH0HasNoDryRun(t *testing.T) {
	h := newHarness(t)
	d := h.add(receiver(em11Mouse(t, dumpImage(t, h.root))))
	_, errs, code := h.run("hwtest", "--stage", "H0", "--dry-run")
	expect(t, code, cli.ExitUsage, errs)
	if !strings.Contains(errs, "no dry run: H0 writes nothing; drop --dry-run") {
		t.Errorf("stderr:\n%s", errs)
	}
	if n := len(d.Writes()); n != 0 {
		t.Errorf("%d packets reached the device", n)
	}
}

func TestHWTestHelp(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"help", "hwtest"}, {"hwtest", "-h"}} {
		out, errs, code := h.run(args...)
		expect(t, code, cli.ExitOK, errs)
		golden(t, "help-hwtest", out)
	}
}
