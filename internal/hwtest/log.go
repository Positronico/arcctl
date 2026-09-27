//go:build hwtest

package hwtest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

const noRuns = "No runs yet."

// log appends the stage's entry to the hardware-test log and sets the
// stage's status in the table of stages.
func (r *runner) log() error {
	path := filepath.Join(r.cfg.Repo, filepath.FromSlash(logPath))
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc := strings.TrimRight(string(old), "\n")
	if strings.HasSuffix(doc, noRuns) {
		doc = strings.TrimRight(strings.TrimSuffix(doc, noRuns), "\n")
	}
	r.res.Status = status(doc, r.res, r.def.steps)
	doc = setStatus(doc, r.res.Stage, r.res.Status) + "\n\n" + entry(r.res) + "\n"
	return writeRepoFile(path, []byte(doc), true)
}

// status is the stage's cell in the table of stages. A stage that names its
// steps passes once each of them passed in a recorded run on the model and
// mouse firmware of this one, this run included; rehearsals count only
// rehearsals.
func status(doc string, res *Result, steps []stepInfo) string {
	head := res.Started.UTC().Format("2006-01-02")
	if res.Device.Mouse != "" {
		head += " (" + res.Device.Mouse + ")"
	}
	rehearsal := ""
	if res.Rehearsal {
		rehearsal = ", rehearsal"
	}
	verdict := map[bool]string{true: "passed ", false: "failed "}
	if len(steps) == 0 {
		return verdict[res.Passed] + head + rehearsal
	}
	runs := []loggedRun{{key: res.Device.Key, firmware: res.Device.Mouse, rehearsal: res.Rehearsal}}
	for _, s := range res.Steps {
		if s.OK && s.Name != "" {
			runs[0].passed = append(runs[0].passed, s.Name)
		}
	}
	if res.Device.Mouse != "" {
		past := loggedRuns(doc, res.Stage, steps)
		for i := len(past) - 1; i >= 0; i-- {
			if p := past[i]; p.key == res.Device.Key && p.firmware == res.Device.Mouse && p.rehearsal == res.Rehearsal {
				runs = append(runs, p)
			}
		}
	}
	done := map[string]bool{}
	used := 0
	for _, run := range runs {
		if len(done) == len(steps) {
			break
		}
		added := false
		for _, n := range run.passed {
			if !done[n] {
				done[n], added = true, true
			}
		}
		if added {
			used++
		}
	}
	var missing []string
	for _, s := range steps {
		if !done[s.name] {
			missing = append(missing, s.name)
		}
	}
	tail := ""
	switch {
	case len(missing) == 0 && used > 1:
		tail = fmt.Sprintf(", its steps over %d runs", used)
	case len(missing) > 3:
		tail = fmt.Sprintf("; %d of %d steps not passed yet", len(missing), len(steps))
	case len(missing) > 0:
		tail = "; not passed yet: " + strings.Join(missing, ", ")
	}
	return verdict[len(missing) == 0] + head + rehearsal + tail
}

// loggedRun is what an entry of the log says about a run of a stage: the
// model and mouse firmware, and the named steps that passed.
type loggedRun struct {
	key, firmware string
	rehearsal     bool
	passed        []string
}

var (
	entryHead  = regexp.MustCompile(`^### \d{4}-\d{2}-\d{2} (\S+) \(`)
	deviceLine = regexp.MustCompile(`^- Device: .* \(([^,()]+), mid \d+\); mouse firmware ([^,]+),`)
	stepLine   = regexp.MustCompile("^  \\d+\\. (.+?)(?: \\(`([a-z0-9-]+)`\\))?: (ok|failed)$")
)

// loggedRuns reads the entries of stage from the log, oldest first. A step
// is known by the name its line gives or, in entries written before steps
// had names, by its title.
func loggedRuns(doc, stage string, steps []stepInfo) []loggedRun {
	var out []loggedRun
	cur := -1
	for _, l := range strings.Split(doc, "\n") {
		if m := entryHead.FindStringSubmatch(l); m != nil {
			cur = -1
			if m[1] == stage {
				out = append(out, loggedRun{})
				cur = len(out) - 1
			}
			continue
		}
		if strings.HasPrefix(l, "#") {
			cur = -1
		}
		if cur < 0 {
			continue
		}
		run := &out[cur]
		switch m := stepLine.FindStringSubmatch(l); {
		case m != nil:
			i := slices.IndexFunc(steps, func(s stepInfo) bool { return m[2] == s.name || m[2] == "" && m[1] == s.title })
			if i >= 0 && m[3] == "ok" {
				run.passed = append(run.passed, steps[i].name)
			}
		case strings.HasPrefix(l, "- Run: "):
			run.rehearsal = strings.Contains(l, "a rehearsal")
		default:
			if d := deviceLine.FindStringSubmatch(l); d != nil {
				run.key, run.firmware = d[1], d[2]
			}
		}
	}
	return out
}

// setStatus rewrites the last cell of the stage's row in the table of
// stages; a log without the row is left as it is.
func setStatus(doc, stage, cell string) string {
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "| "+stage+" |") {
			continue
		}
		cells := strings.Split(strings.TrimSuffix(l, "|"), "|")
		if len(cells) < 3 {
			break
		}
		cells[len(cells)-1] = " " + cell + " "
		lines[i] = strings.Join(cells, "|") + "|"
		break
	}
	return strings.Join(lines, "\n")
}

// entry is one run in the log. It holds nothing private: no address, no key
// bytes, no local path.
func entry(res *Result) string {
	var b strings.Builder
	what := res.Title
	if len(res.Selected) > 0 {
		what = "steps: " + strings.Join(res.Selected, ", ")
	}
	fmt.Fprintf(&b, "### %s %s (%s): %s\n\n", res.Started.UTC().Format("2006-01-02"), res.Stage, what, map[bool]string{true: "passed", false: "failed"}[res.Passed])
	d := res.Device
	if d.Key != "" {
		fmt.Fprintf(&b, "- Device: %s (%s, mid %d); mouse firmware %s, receiver %s; %s; profile %s\n", d.Model, d.Key, d.MID, orNone(d.Mouse), orNone(d.Receiver), orNone(d.Conn), d.Profile)
	}
	run := fmt.Sprintf("- Run: %s to %s UTC", res.Started.UTC().Format("15:04"), res.Ended.UTC().Format("15:04"))
	if res.Rehearsal {
		run += "; a rehearsal, not a hardware result"
	}
	b.WriteString(run + "\n")
	if len(res.Transcripts) > 0 {
		quoted := make([]string, len(res.Transcripts))
		for i, t := range res.Transcripts {
			quoted[i] = "`" + t + "`"
		}
		fmt.Fprintf(&b, "- Transcripts: %s\n", strings.Join(quoted, ", "))
	}
	if len(res.Steps) > 0 {
		b.WriteString("- Steps:\n")
		for i, s := range res.Steps {
			title := oneLine(s.Title)
			if s.Name != "" && s.Name != title {
				title += " (`" + s.Name + "`)"
			}
			fmt.Fprintf(&b, "  %d. %s: %s\n", i+1, title, map[bool]string{true: "ok", false: "failed"}[s.OK])
			for _, d := range s.Detail {
				fmt.Fprintf(&b, "     - %s\n", oneLine(d))
			}
		}
	}
	if len(res.Answers) > 0 {
		b.WriteString("- Answers:\n")
		for _, a := range res.Answers {
			mark := ""
			if !a.OK {
				mark = " (not the expected answer)"
			}
			fmt.Fprintf(&b, "  - %s %s%s\n", oneLine(a.Question), oneLine(a.Answer), mark)
		}
	}
	if len(res.Findings) > 0 {
		b.WriteString("- Findings:\n")
		for _, f := range res.Findings {
			fmt.Fprintf(&b, "  - %s\n", oneLine(f))
		}
	}
	for _, v := range res.Promoted {
		fmt.Fprintf(&b, "- Promoted: `%s` for %s on %s\n", v.Feature, v.Model, v.Firmware)
	}
	if res.Err != nil {
		fmt.Fprintf(&b, "- Stopped: %s\n", oneLine(res.Err.Error()))
	}
	return strings.TrimRight(b.String(), "\n")
}

// scrub replaces what names this machine in a line bound for the public
// records: file paths, and device paths such as IOService:/... or
// \\?\hid#...; error texts can carry either.
func scrub(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		core := strings.TrimRight(strings.TrimLeft(f, "(\"'`"), ")\"'`,;:.")
		if strings.HasPrefix(core, "/") || strings.HasPrefix(core, "~/") || strings.Contains(core, "IOService") ||
			strings.HasPrefix(core, `\\`) || (len(core) > 2 && core[1] == ':' && (core[2] == '\\' || core[2] == '/')) {
			fields[i] = strings.Replace(f, core, "<path>", 1)
		}
	}
	return strings.Join(fields, " ")
}

// private reports whether e reaches the shortcut and macro slots, whose
// bytes are the user's own: the records never show them, as the redacted
// transcripts do not.
func private(e flash.Extent) bool {
	first, _ := mouse.ShortcutExtent(0)
	last, _ := mouse.MacroExtent(mouse.Slots - 1)
	return e.Overlaps(flash.Extent{Addr: first.Addr, Len: last.End() - first.Addr})
}

// shown is b, the bytes at e, as the records may show them.
func shown(e flash.Extent, b []byte) string {
	if private(e) {
		return fmt.Sprintf("%d bytes (not shown)", len(b))
	}
	return fmt.Sprintf("% x", b)
}

// holds compares what e holds with want, in words the records may show.
func holds(e flash.Extent, got, want []byte) string {
	switch {
	case !private(e):
		return fmt.Sprintf("%s holds % x, want % x", e, got, want)
	case bytes.Equal(got, want):
		return fmt.Sprintf("%s holds the %d bytes wanted", e, len(want))
	}
	return fmt.Sprintf("%s holds other bytes: %d of %d differ", e, differ(got, want), len(want))
}

// shownPacket is p as a redacted transcript shows it: the bytes of shortcut
// and macro slots, and the checksum over them, read xx.
func shownPacket(p wire.Packet) string {
	return hidio.Redact(hidio.Entry{Dir: hidio.DirIn, ID: wire.ReportID, Data: hidio.Frame{Bytes: p[:]}}).Data.String()
}

func shownPackets(ps []wire.Packet) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = shownPacket(p)
	}
	return "[" + strings.Join(out, " ") + "]"
}

func orNone(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func oneLine(s string) string { return scrub(strings.Join(strings.Fields(s), " ")) }

// writeRepoFile writes a file of the checkout, which git reads, so it gets
// the usual 0644 rather than the backups' 0600.
func writeRepoFile(path string, data []byte, replace bool) error {
	var err error
	if replace {
		err = backup.WriteFile(path, data)
	} else {
		err = backup.WriteNew(path, data)
	}
	if err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
}
