//go:build hwtest

package hwtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/positronico/arcctl/internal/backup"
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
	doc = setStatus(doc, r.res) + "\n\n" + entry(r.res) + "\n"
	return writeRepoFile(path, []byte(doc), true)
}

func status(res *Result) string {
	s := "failed"
	if res.Passed {
		s = "passed"
	}
	s += " " + res.Started.UTC().Format("2006-01-02")
	if res.Device.Mouse != "" {
		s += " (" + res.Device.Mouse + ")"
	}
	if res.Rehearsal {
		s += ", rehearsal"
	}
	return s
}

// setStatus rewrites the last cell of the stage's row in the table of
// stages; a log without the row is left as it is.
func setStatus(doc string, res *Result) string {
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "| "+res.Stage+" |") {
			continue
		}
		cells := strings.Split(strings.TrimSuffix(l, "|"), "|")
		if len(cells) < 3 {
			break
		}
		cells[len(cells)-1] = " " + status(res) + " "
		lines[i] = strings.Join(cells, "|") + "|"
		break
	}
	return strings.Join(lines, "\n")
}

// entry is one run in the log. It holds nothing private: no address, no key
// bytes, no local path.
func entry(res *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s %s (%s): %s\n\n", res.Started.UTC().Format("2006-01-02"), res.Stage, res.Title, map[bool]string{true: "passed", false: "failed"}[res.Passed])
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
			fmt.Fprintf(&b, "  %d. %s: %s\n", i+1, oneLine(s.Title), map[bool]string{true: "ok", false: "failed"}[s.OK])
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
