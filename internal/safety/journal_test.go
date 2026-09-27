package safety_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
)

func journalLines(t testing.TB, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestJournalFile(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(pairChange(t))
	if f.j.Path() != "" {
		t.Fatal("a journal file exists before the first run")
	}
	res, err := f.apply(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := f.j.Path()
	if filepath.Dir(path) != filepath.Join(f.root, identity.Key()) || filepath.Dir(path) != f.j.Dir() || !strings.HasSuffix(path, ".jsonl") {
		t.Fatalf("journal at %s", path)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]fs.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
				t.Fatalf("%s: %v %v, want %v", p, fi.Mode().Perm(), err, want)
			}
		}
	}
	lines := journalLines(t, path)
	var got []string
	for _, l := range lines {
		if l["run"] != res.Run {
			t.Fatalf("line for run %v", l["run"])
		}
		s := l["type"].(string)
		if st, ok := l["state"]; ok {
			s += ":" + st.(string)
		}
		got = append(got, s)
	}
	want := []string{"run", "op:planned", "op:sending", "op:sent", "op:verified", "end"}
	if !slices.Equal(got, want) {
		t.Fatalf("entries %v, want %v", got, want)
	}
	run, op := lines[0], lines[1]
	dev := run["device"].(map[string]any)
	if run["v"] != 1.0 || run["kind"] != "apply" || run["profile"] != 0.0 || run["ops"] != 1.0 ||
		dev["key"] != identity.Key() || dev["vid"] != "260d" || dev["cid"] != "7b" || dev["mid"] != 4.0 {
		t.Fatalf("run entry %v", run)
	}
	ext := op["extent"].(map[string]any)
	if op["old"] != "0352" || op["new"] != "0154" || op["phase"] != "record" || op["tier"] != "untested" ||
		ext["addr"] != 4.0 || ext["len"] != 2.0 || op["seq"] != 1.0 {
		t.Fatalf("planned op %v", op)
	}
	if lines[len(lines)-1]["result"] != "complete" {
		t.Fatalf("end entry %v", lines[len(lines)-1])
	}
}

// Each process gets its own file, and Load merges them.
func TestJournalSessions(t *testing.T) {
	f := newFixture(t, setup{})
	if _, err := f.apply(f.plan(pairChange(t)), nil); err != nil {
		t.Fatal(err)
	}
	first := f.j.Path()
	f.restart()
	if _, err := f.apply(f.plan(dpiChange(t, f.model)), nil); err != nil {
		t.Fatal(err)
	}
	if f.j.Path() == first {
		t.Fatal("two sessions share a journal file")
	}
	st := f.status()
	if len(st.Runs) != 2 || st.Runs[0].ID == st.Runs[1].ID || st.Last != st.Runs[1] || !st.Clean() {
		t.Fatalf("runs %+v", st.Runs)
	}
	if st.Find(st.Runs[0].ID) != st.Runs[0] || st.Find("nope") != nil {
		t.Fatal("Find")
	}
}

// A resolve entry names a run of another file; which file sorts first, as
// when the clock went back between sessions, does not matter.
func TestResolveInAnEarlierFile(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	f.applyKilled(p, 1)
	if _, err := f.x.Recover(context.Background(), f.status().Open[0], safety.Back, f.device(), nil); err != nil {
		t.Fatal(err)
	}
	later := f.j.Path()
	f.j.Close()
	if err := os.Rename(later, filepath.Join(filepath.Dir(later), "00000000T000000.000000000Z-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	st := f.status()
	if !st.Clean() || st.Runs[1].Resolved != safety.Back {
		t.Fatalf("runs %+v", st.Runs)
	}
}

// A crash in the middle of an append leaves a torn last line, which says
// nothing was sent after it; a line that is torn anywhere else is corruption.
func TestLoadTornAndCorruptLines(t *testing.T) {
	f := newFixture(t, setup{})
	if _, err := f.apply(f.plan(pairChange(t)), nil); err != nil {
		t.Fatal(err)
	}
	path := f.j.Path()
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write := func(b []byte) {
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(append(slices.Clone(good), `{"type":"op","run":"x","se`...))
	if st, err := safety.Load(f.j.Dir()); err != nil || !st.Clean() || len(st.Runs) != 1 {
		t.Fatalf("torn last line: %v, %+v", err, st)
	}
	i := bytes.IndexByte(good, '\n')
	write(slices.Concat(good[:i+1], []byte("{\"type\":\n"), good[i+1:]))
	if _, err := safety.Load(f.j.Dir()); !errors.Is(err, safety.ErrCorrupt) {
		t.Fatalf("corrupt line: %v", err)
	}
	write(bytes.Replace(good, []byte(`"state":"verified"`), []byte(`"state":"approved"`), 1))
	if _, err := safety.Load(f.j.Dir()); !errors.Is(err, safety.ErrCorrupt) {
		t.Fatalf("unknown state: %v", err)
	}
	write(bytes.Replace(good, []byte(`"key":"`+identity.Key()), []byte(`"key":"7b04-112233`), 1))
	if _, err := safety.Load(f.j.Dir()); !errors.Is(err, safety.ErrCorrupt) {
		t.Fatalf("key that does not match its fields: %v", err)
	}
	write(bytes.Replace(good, []byte(`"mid":4`), []byte(`"mid":6`), 1))
	if _, err := safety.Load(f.j.Dir()); !errors.Is(err, safety.ErrCorrupt) {
		t.Fatalf("run of another device: %v", err)
	}
}

// The process died while journaling a run's ops: none of them was sent, so
// the run needs no recovery.
func TestTruncatedRunIsNotOpen(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	f.applyKilled(f.plan(macroChanges(t, 5, "new", 20)), 1)
	st := f.status()
	path := filepath.Join(f.j.Dir(), filepath.Base(st.Open[0].ID[:strings.IndexByte(st.Open[0].ID, '/')])+".jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(b, []byte{'\n'})
	if err := os.WriteFile(path, slices.Concat(lines[0], lines[1], lines[2][:10]), 0o600); err != nil {
		t.Fatal(err)
	}
	st = f.status()
	if len(st.Runs) != 1 || !st.Runs[0].Truncated || !st.Clean() || st.Last != nil {
		t.Fatalf("runs %+v, open %v", st.Runs, st.Open)
	}
}

// A journal that cannot be written stops the run before its first packet,
// and every later run too.
func TestJournalFailureSendsNothing(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(pairChange(t))
	safety.SetSync(f.j, func(*os.File) error { return errors.New("disk full") })
	if _, err := f.apply(p, nil); !errors.Is(err, safety.ErrJournal) {
		t.Fatalf("err = %v, want ErrJournal", err)
	}
	safety.SkipSync(f.j)
	if _, err := f.apply(p, nil); !errors.Is(err, safety.ErrJournal) {
		t.Fatalf("second run: %v, want ErrJournal", err)
	}
	if len(f.dev.Writes()) != 0 {
		t.Fatalf("%d packets went out", len(f.dev.Writes()))
	}
}

func TestProfileRoundTrip(t *testing.T) {
	for _, profile := range []*byte{nil, ptr(byte(0)), ptr(byte(3))} {
		root := t.TempDir()
		j, err := safety.OpenJournal(root, identity)
		if err != nil {
			t.Fatal(err)
		}
		p := plan.Plan{Device: identity, Profile: profile, Ops: []plan.Op{{
			Seq: 1, Extent: flash.Extent{Addr: 4, Len: 2}, Old: []byte{3, 0x52}, New: []byte{1, 0x54}, Phase: plan.Record, Tier: catalog.Untested,
		}}}
		safety.Begin(t, j, p)
		j.Close()
		st, err := safety.Load(j.Dir())
		if err != nil {
			t.Fatal(err)
		}
		r := st.Runs[0]
		if (r.Profile == nil) != (profile == nil) || profile != nil && *r.Profile != *profile {
			t.Fatalf("profile %v came back as %v", profile, r.Profile)
		}
		if r.Device != identity || r.Ops[0].Op.Seq != 1 || !bytes.Equal(r.Ops[0].New, []byte{1, 0x54}) {
			t.Fatalf("run %+v", r)
		}
		if r.Open() {
			t.Fatal("a run whose ops are all planned sent nothing, yet it is open")
		}
		if err := r.Matches(identity, profile); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenJournalNeedsAFolder(t *testing.T) {
	if _, err := safety.OpenJournal("", identity); !errors.Is(err, safety.ErrJournal) {
		t.Fatalf("err = %v", err)
	}
}

// The folders a journal needs are made durable as they are created: the
// parent of each new folder is synced, so a power loss cannot drop the
// device's folder with the run in it (I6). Folders that exist are left alone.
func TestNewJournalFoldersAreSynced(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "data", "journal")
	dir := filepath.Join(root, identity.Key())
	var synced []string
	record := func(d string) error {
		synced = append(synced, d)
		return nil
	}
	if err := safety.MkdirSynced(dir, record); err != nil {
		t.Fatal(err)
	}
	if want := []string{base, filepath.Join(base, "data"), root}; !slices.Equal(synced, want) {
		t.Fatalf("synced %v, want %v", synced, want)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("%s: %v", dir, err)
	}
	synced = nil
	if err := safety.MkdirSynced(dir, record); err != nil || len(synced) != 0 {
		t.Fatalf("again: %v, synced %v", err, synced)
	}
	full := errors.New("disk full")
	if err := safety.MkdirSynced(filepath.Join(root, "other"), func(string) error { return full }); !errors.Is(err, full) {
		t.Fatalf("a failed sync: %v", err)
	}
}

func runID(journal []byte) string {
	var e struct{ Run string }
	_ = json.Unmarshal(journal[:bytes.IndexByte(journal, '\n')], &e)
	return e.Run
}

func FuzzLoad(f *testing.F) {
	j, err := safety.OpenJournal(f.TempDir(), identity)
	if err != nil {
		f.Fatal(err)
	}
	safety.SkipSync(j)
	safety.Begin(f, j, plan.Plan{Device: identity, Profile: ptr(byte(0)), Ops: []plan.Op{{
		Seq: 1, Extent: flash.Extent{Addr: 4, Len: 2}, Old: []byte{3, 0x52}, New: []byte{1, 0x54}, Phase: plan.Record, Tier: catalog.Untested,
	}}})
	good, err := os.ReadFile(j.Path())
	if err != nil {
		f.Fatal(err)
	}
	j.Close()
	f.Add(good)
	f.Add(append(slices.Clone(good), `{"type":"op","run":"`+runID(good)+`","time":"2026-09-26T10:00:00Z","seq":1,"state":"verified"}`+"\n"+
		`{"type":"end","run":"`+runID(good)+`","time":"2026-09-26T10:00:00Z","result":"complete"}`+"\n"...))
	f.Add([]byte("{}\n"))
	f.Add([]byte(`{"type":"resolve","run":"a","result":"forward"}` + "\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		st, err := safety.Parse(identity.Key(), b)
		if err != nil {
			if !errors.Is(err, safety.ErrCorrupt) {
				t.Fatalf("error %v is not ErrCorrupt", err)
			}
			return
		}
		for _, r := range st.Open {
			if !r.Open() || r.Kind == safety.KindRecover {
				t.Fatalf("open list holds %+v", r)
			}
		}
	})
}
