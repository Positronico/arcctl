package cli_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/cli"
	"github.com/positronico/arcctl/internal/session"
)

// TestTUIGetsTheSession runs arcctl with no command and a TUI that waits
// for the session to load, as the real one shows it.
func TestTUIGetsTheSession(t *testing.T) {
	tests := []struct {
		name  string
		args  func(h *harness) []string
		check func(t *testing.T, got cli.TUI)
	}{
		{"device", func(h *harness) []string {
			h.add(receiver(em11Mouse(t, dumpImage(t, h.root))))
			return nil
		}, func(t *testing.T, got cli.TUI) {
			if got.Source != backup.SourceDevice || got.ReadOnly || got.DryRun || got.Permission == nil || got.Backups == nil {
				t.Errorf("%+v", got)
			}
		}},
		{"emulated dry run", func(h *harness) []string {
			return []string{"--emulate", h.dump(), "--dry-run", "--allow-untested"}
		}, func(t *testing.T, got cli.TUI) {
			if got.Source != backup.SourceEmulator || !got.DryRun || !got.Gates.DryRun || !got.Gates.AllowUntested || got.Permission != nil {
				t.Errorf("%+v", got)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			args := tc.args(h)
			var out, errb bytes.Buffer
			env := h.env(&out, &errb)
			env.Terminal = true
			var got cli.TUI
			env.TUI = func(ctx context.Context, tui cli.TUI) error {
				got = tui
				ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				_, err := session.Await(ctx, tui.Session, func(sn *session.Snapshot) bool { return sn.State == session.Ready })
				return err
			}
			if code := cli.Run(context.Background(), args, env); code != cli.ExitOK {
				t.Fatalf("exit %d: %s", code, errb.String())
			}
			if got.Session == nil {
				t.Fatal("the TUI did not run")
			}
			tc.check(t, got)
			if tmp, ok := strings.CutPrefix(strings.TrimSpace(errb.String()), "arcctl: the emulated mouse's journal and backups go to "); ok {
				os.RemoveAll(tmp)
			}
		})
	}
}
