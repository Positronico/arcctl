package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

const (
	factsDir     = "../facts"
	verifiedFile = "../verified.json"
	moduleRoot   = "../../.."
)

func readVerified(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(verifiedFile)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readFacts(t *testing.T) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(factsDir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			data, err := os.ReadFile(filepath.Join(factsDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[e.Name()] = data
		}
	}
	return files
}

func factsTree(t *testing.T, files map[string][]byte) map[string]any {
	t.Helper()
	root := map[string]any{}
	for name, data := range files {
		if name != schemaFile {
			root[name] = parseTree(t, data)
		}
	}
	return root
}

func toFS(files map[string][]byte) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, data := range files {
		fsys[name] = &fstest.MapFile{Data: data}
	}
	return fsys
}

func TestCommittedOutputsMatchFacts(t *testing.T) {
	out, err := build(os.DirFS(factsDir), readVerified(t))
	if err != nil {
		t.Fatal(err)
	}
	generated := map[string]bool{}
	for _, o := range out {
		generated[o.path] = true
		got, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(o.path)))
		if err != nil {
			t.Errorf("%s: %v (run go generate ./internal/catalog)", o.path, err)
			continue
		}
		if !bytes.Equal(got, o.data) {
			t.Errorf("%s differs from what facts/ generates; run go generate ./internal/catalog", o.path)
		}
	}
	for _, dir := range []string{"internal/catalog", "internal/keys", "internal/mouse", "internal/keyboard"} {
		stale, err := filepath.Glob(filepath.Join(moduleRoot, dir, "zz_*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range stale {
			rel := dir + "/" + filepath.Base(p)
			if !generated[rel] {
				t.Errorf("%s is not produced by the generator", rel)
			}
		}
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	a, err := build(os.DirFS(factsDir), readVerified(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := build(os.DirFS(factsDir), readVerified(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("%d outputs, then %d", len(a), len(b))
	}
	for i := range a {
		if a[i].path != b[i].path || !bytes.Equal(a[i].data, b[i].data) {
			t.Errorf("%s differs between two runs", a[i].path)
		}
	}
}

func TestGoFilesHaveGeneratedHeader(t *testing.T) {
	out, err := build(os.DirFS(factsDir), readVerified(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range out {
		first, _, _ := bytes.Cut(o.data, []byte("\n"))
		want := "// " + generatedBy
		switch {
		case o.path == verifiedPath:
			want = "// " + verifiedBy
		case !strings.HasSuffix(o.path, ".go"):
			want = "# " + generatedBy
		}
		if string(first) != want {
			t.Errorf("%s starts with %q", o.path, first)
		}
	}
}

func TestUdevRules(t *testing.T) {
	f, err := loadFacts(os.DirFS(factsDir))
	if err != nil {
		t.Fatal(err)
	}
	data, err := renderUdev(f)
	if err != nil {
		t.Fatal(err)
	}
	var pids int
	for _, cl := range pidClasses(&f.IDs) {
		pids += len(cl.class.Wireless) + len(cl.class.Wired)
	}
	var rules []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			rules = append(rules, line)
		}
	}
	if want := len(f.IDs.VIDs) * pids; len(rules) != want {
		t.Errorf("%d rules, want %d (every VID with every PID)", len(rules), want)
	}
	for _, r := range rules {
		if !strings.Contains(r, "ATTRS{idVendor}==") || !strings.Contains(r, "ATTRS{idProduct}==") || !strings.HasSuffix(r, `TAG+="uaccess"`) {
			t.Errorf("rule %q needs a VID, a PID and the uaccess tag", r)
		}
	}
	if !slices.Contains(rules, `SUBSYSTEM=="hidraw", ATTRS{idVendor}=="260d", ATTRS{idProduct}=="1282", TAG+="uaccess"`) {
		t.Error("no rule for the EM11 Pro receiver 260d:1282")
	}
}

func TestWriteAll(t *testing.T) {
	out, err := build(os.DirFS(factsDir), readVerified(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeAll(dir, out); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, filepath.FromSlash(out[0].path))
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAll(dir, out); err != nil {
		t.Fatal(err)
	}
	for _, o := range out {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(o.path)))
		if err != nil || !bytes.Equal(got, o.data) {
			t.Errorf("%s not written as generated (%v)", o.path, err)
		}
		temps, _ := filepath.Glob(filepath.Join(dir, filepath.Dir(o.path), ".gen-*"))
		if len(temps) > 0 {
			t.Errorf("temporary files left behind: %v", temps)
		}
	}
}

func at(x any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			x = x.(map[string]any)[k]
		case int:
			x = x.([]any)[k]
		}
	}
	return x
}

func obj(x any, path ...any) map[string]any { return at(x, path...).(map[string]any) }

func num(n int) json.Number { return json.Number(fmt.Sprint(n)) }

type schemaVerdict int

const (
	schemaSkip schemaVerdict = iota
	schemaRejects
	schemaAccepts
)

func TestRejectsBadFacts(t *testing.T) {
	cases := []struct {
		name   string
		file   string
		edit   func(root any)
		raw    func(files map[string][]byte)
		want   string
		schema schemaVerdict
	}{
		{name: "label over 40 characters", file: "labels.json", want: "has 41 characters", schema: schemaRejects,
			edit: func(r any) { obj(r, "labels")["action.ok"] = strings.Repeat("x", 41) }},
		{name: "label with trailing space", file: "labels.json", want: "leading or trailing space", schema: schemaAccepts,
			edit: func(r any) { obj(r, "labels")["action.ok"] = "OK " }},
		{name: "label with control character", file: "labels.json", want: "non-printable", schema: schemaAccepts,
			edit: func(r any) { obj(r, "labels")["action.ok"] = "O\tK" }},
		{name: "label key over 40 characters", file: "labels.json", want: "label key", schema: schemaRejects,
			edit: func(r any) { obj(r, "labels")["button."+strings.Repeat("x", 34)] = "X" }},
		{name: "free-text label key", file: "labels.json", want: "does not match", schema: schemaRejects,
			edit: func(r any) { obj(r, "labels")["tip.pair[Hold left and right for 3 s]"] = "X" }},
		{name: "language-file path as a label key", file: "labels.json", want: "does not match", schema: schemaRejects,
			edit: func(r any) { obj(r, "labels")["menu.Options[0]"] = "X" }},
		{name: "model with mouse and keyboard", file: "models.json", want: "needs mouse and no keyboard", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 0)["keyboard"] = at(r, "models", 6, "keyboard") }},
		{name: "keyboard without keyboard", file: "models.json", want: "needs keyboard and no mouse", schema: schemaRejects,
			edit: func(r any) { delete(obj(r, "models", 6), "keyboard") }},
		{name: "unknown group", file: "models.json", want: "is not one of", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 0)["group"] = "tablet" }},
		{name: "media default with a label", file: "models.json", want: "takes its label from media.json", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 4, "mouse", "keys", 7)["label"] = "button.disable" }},
		{name: "media default with a type", file: "models.json", want: "needs type 5 and param 0", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 4, "mouse", "keys", 7)["type"] = num(1) }},
		{name: "normal default without a label", file: "models.json", want: "required when media is absent", schema: schemaRejects,
			edit: func(r any) { delete(obj(r, "models", 0, "mouse", "keys", 0), "label") }},
		{name: "unknown label key", file: "models.json", want: "is not in labels.json", schema: schemaAccepts,
			edit: func(r any) { obj(r, "models", 0, "mouse", "keys", 0)["label"] = "button.nope" }},
		{name: "unknown sensor", file: "models.json", want: `unknown sensor "9999"`, schema: schemaAccepts,
			edit: func(r any) { obj(r, "models", 0, "mouse")["sensor"] = "9999" }},
		{name: "duplicate cid and mid", file: "models.json", want: "is also in models[0]", schema: schemaAccepts,
			edit: func(r any) { obj(r, "models", 1)["mids"] = []any{num(1)} }},
		{name: "unknown field", file: "models.json", want: `unknown field "colour"`, schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 0)["colour"] = num(1) }},
		{name: "missing required field", file: "models.json", want: "models[0].cid: required field is missing", schema: schemaRejects,
			edit: func(r any) { delete(obj(r, "models", 0), "cid") }},
		{name: "null cid", file: "models.json", want: "models[0].cid: null is not allowed", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 0)["cid"] = nil }},
		{name: "null mid", file: "models.json", want: "models[0].mids[1]: null is not allowed", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 0)["mids"] = []any{num(1), nil} }},
		{name: "null media", file: "models.json", want: "keys[7].media: null is not allowed", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 4, "mouse", "keys", 7)["media"] = nil }},
		{name: "short colour", file: "models.json", want: "want 3 items, have 2", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 0, "mouse", "dpis", 0)["color"] = []any{num(255), num(0)} }},
		{name: "cid over 255", file: "models.json", want: "cannot unmarshal number 256", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 0)["cid"] = num(256) }},
		{name: "string for a number", file: "models.json", want: "cannot unmarshal string", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 0, "mouse")["maxDpi"] = "8000" }},
		{name: "fractional number", file: "models.json", want: "cannot unmarshal number 8000.5", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 0, "mouse")["maxDpi"] = json.Number("8000.5") }},
		{name: "current stage outside the stages", file: "models.json", want: "is not a stage index", schema: schemaAccepts,
			edit: func(r any) { obj(r, "models", 4, "mouse")["currentDpi"] = num(3) }},
		{name: "too many stages", file: "models.json", want: "want 1..8", schema: schemaRejects,
			edit: func(r any) {
				m := obj(r, "models", 0, "mouse")
				dpis := m["dpis"].([]any)
				m["dpis"] = append(dpis, dpis[0], dpis[1])
			}},
		{name: "keyboard layer missing", file: "models.json", want: "want 4 (systems x layouts)", schema: schemaAccepts,
			edit: func(r any) {
				k := obj(r, "models", 6, "keyboard")
				k["maps"] = k["maps"].([]any)[:3]
			}},
		{name: "keyboard layers out of order", file: "models.json", want: "want win/fn (systems x layouts order)", schema: schemaAccepts,
			edit: func(r any) {
				maps := at(r, "models", 6, "keyboard", "maps").([]any)
				maps[1], maps[2] = maps[2], maps[1]
			}},
		{name: "keyboard layer short", file: "models.json", want: "rects has 128", schema: schemaAccepts,
			edit: func(r any) {
				l := obj(r, "models", 6, "keyboard", "maps", 0)
				l["slots"] = l["slots"].([]any)[:127]
			}},
		{name: "null offset without vendor", file: "offsets.json", want: "a null value needs the vendor value", schema: schemaRejects,
			edit: func(r any) { obj(r, "mouse", 0)["value"] = nil }},
		{name: "offset beyond flash", file: "offsets.json", want: "outside 0..16383", schema: schemaRejects,
			edit: func(r any) { obj(r, "mouse", 0)["value"] = num(16384) }},
		{name: "duplicate offset name", file: "offsets.json", want: "duplicate ReportRate", schema: schemaAccepts,
			edit: func(r any) { obj(r, "mouse", 1)["name"] = "ReportRate" }},
		{name: "offset names that collide in Go", file: "offsets.json", want: "duplicate MaxDpiStage", schema: schemaAccepts,
			edit: func(r any) { obj(r, "mouse", 0)["name"] = "MaxDpiStage" }},
		{name: "empty offset name", file: "offsets.json", want: `"" does not match`, schema: schemaRejects,
			edit: func(r any) { obj(r, "keyboard", 0)["name"] = "" }},
		{name: "empty mids", file: "models.json", want: "want at least 1", schema: schemaRejects,
			edit: func(r any) { obj(r, "models", 0)["mids"] = []any{} }},
		{name: "empty models", file: "models.json", want: "want at least 1", schema: schemaRejects,
			edit: func(r any) { r.(map[string]any)["models"] = []any{} }},
		{name: "invalid key code", file: "keys.json", want: "does not match", schema: schemaRejects,
			edit: func(r any) { obj(r, "keys", 0)["code"] = "Esc-ape" }},
		{name: "duplicate key code", file: "keys.json", want: `duplicate code "Escape"`, schema: schemaAccepts,
			edit: func(r any) { obj(r, "keys", 1)["code"] = "Escape" }},
		{name: "preset key disagrees with the key table", file: "presets.json", want: "keys.json has kind 0 value 8", schema: schemaAccepts,
			edit: func(r any) { obj(r, "shortcuts", "win", 0, "keys", 0)["value"] = num(9) }},
		{name: "preset with six keys", file: "presets.json", want: "want 1..5", schema: schemaRejects,
			edit: func(r any) {
				p := obj(r, "shortcuts", "win", 0)
				k := p["keys"].([]any)[0]
				p["keys"] = []any{k, k, k, k, k, k}
			}},
		{name: "unknown repeat mode", file: "presets.json", want: "is not one of", schema: schemaRejects,
			edit: func(r any) { obj(r, "macroRepeat", 0)["mode"] = "forever" }},
		{name: "duplicate brightness level", file: "brightness.json", want: "duplicate 1", schema: schemaAccepts,
			edit: func(r any) { obj(r, "levels", 1)["level"] = num(1) }},
		{name: "media list names an unknown usage", file: "media.json", want: "is not in usages", schema: schemaAccepts,
			edit: func(r any) {
				m := r.(map[string]any)
				m["mouse"] = append(m["mouse"].([]any), num(0x9999))
			}},
		{name: "media label over 40 characters", file: "media.json", want: "has 41 characters", schema: schemaRejects,
			edit: func(r any) { obj(r, "usages", 0)["label"] = strings.Repeat("m", 41) }},
		{name: "usages out of order", file: "media.json", want: "is out of order", schema: schemaAccepts,
			edit: func(r any) {
				u := obj(r)["usages"].([]any)
				u[0], u[1] = u[1], u[0]
			}},
		{name: "keyboard map names an unknown consumer usage", file: "models.json", want: "consumer usage 0x9999 is not in media.json", schema: schemaAccepts,
			edit: func(r any) { obj(r, "models", 6, "keyboard", "maps", 1, "slots", 1)["value"] = num(0x9999) }},
		{name: "range not a multiple of its step", file: "sensors.json", want: "do not form a range", schema: schemaAccepts,
			edit: func(r any) { obj(r, "sensors", 0, "ranges", 0)["max"] = num(16050) }},
		{name: "raw values do not cover the first range", file: "sensors.json", want: "the first range has 39 steps", schema: schemaAccepts,
			edit: func(r any) {
				s := obj(r, "sensors", 2)
				v := s["values"].([]any)
				s["values"] = v[:len(v)-1]
			}},
		{name: "gate names an unknown sensor", file: "sensors.json", want: `unknown sensor "1111"`, schema: schemaAccepts,
			edit: func(r any) {
				g := obj(r, "gates", 0)
				g["sensors"] = append(g["sensors"].([]any), "1111")
			}},
		{name: "pid in two classes", file: "ids.json", want: "pids (all classes)", schema: schemaAccepts,
			edit: func(r any) { obj(r, "pids", "keyboard")["wired"] = []any{num(0x1213)} }},
		{name: "duplicate vid", file: "ids.json", want: "duplicate", schema: schemaRejects,
			edit: func(r any) {
				m := r.(map[string]any)
				m["vids"] = append(m["vids"].([]any), num(0x260D))
			}},
		{name: "format version 2", file: "sources.json", want: "format version 2", schema: schemaRejects,
			edit: func(r any) { r.(map[string]any)["schema"] = num(2) }},
		{name: "uppercase sha256", file: "sources.json", want: "does not match", schema: schemaRejects,
			edit: func(r any) {
				in := obj(r, "inputs", 0)
				in["sha256"] = strings.ToUpper(in["sha256"].(string))
			}},
		{name: "tool without version or hash", file: "sources.json", want: "needs a version or a sha256", schema: schemaRejects,
			edit: func(r any) { delete(obj(r, "tools", 0), "version") }},
		{name: "missing file", want: "sources.json", schema: schemaRejects,
			raw: func(files map[string][]byte) { delete(files, "sources.json") }},
		{name: "unexpected file", want: "unexpected file", schema: schemaRejects,
			raw: func(files map[string][]byte) { files["extra.json"] = []byte("{}\n") }},
		{name: "duplicate object key", want: `duplicate object key "action.cancel"`,
			raw: func(files map[string][]byte) {
				files["labels.json"] = bytes.Replace(files["labels.json"], []byte(`"action.cancel": "Cancel",`), []byte(`"action.cancel": "Cancel", "action.cancel": "Other",`), 1)
			}},
		{name: "trailing data", want: "trailing data",
			raw: func(files map[string][]byte) { files["ids.json"] = append(files["ids.json"], "{}"...) }},
	}
	root := loadSchema(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := readFacts(t)
			if tc.raw != nil {
				tc.raw(files)
			} else {
				tree := parseTree(t, files[tc.file])
				tc.edit(tree)
				data, err := json.MarshalIndent(tree, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				files[tc.file] = data
			}
			_, err := build(toFS(files), []byte("[]"))
			switch {
			case err == nil:
				t.Errorf("build accepted the facts, want an error containing %q", tc.want)
			case !strings.Contains(err.Error(), tc.want):
				t.Errorf("build error does not mention %q:\n%v", tc.want, err)
			}
			if tc.schema == schemaSkip {
				return
			}
			errs := schemaErrors(root, factsTree(t, files))
			if tc.schema == schemaRejects && len(errs) == 0 {
				t.Error("schema.json accepts the facts, want a rejection")
			}
			if tc.schema == schemaAccepts && len(errs) > 0 {
				t.Errorf("schema.json rejects the facts, the case expects a generator-only check:\n%s", strings.Join(errs, "\n"))
			}
		})
	}
}
