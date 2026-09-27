//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/hidio"
)

const (
	logPath        = "docs/hardware-tests.md"
	verifiedPath   = "internal/catalog/verified.json"
	transcriptsDir = "testdata/transcripts"
	module         = "module github.com/positronico/arcctl"
)

// checkRepo makes sure the records have somewhere to go. A rehearsal may
// start from an empty folder.
func (r *runner) checkRepo() error {
	repo := r.cfg.Repo
	if repo == "" {
		return fmt.Errorf("%w: no folder for the records", ErrRepo)
	}
	if r.res.Rehearsal {
		if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(logPath)), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(repo, logPath)); errors.Is(err, fs.ErrNotExist) {
			return os.WriteFile(filepath.Join(repo, logPath), []byte(rehearsalHeader), 0o644)
		}
		return nil
	}
	mod, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil || !slices.Contains(strings.Split(string(mod), "\n"), module) {
		return fmt.Errorf("%w: %s has no go.mod for %s", ErrRepo, repo, strings.TrimPrefix(module, "module "))
	}
	for _, p := range []string{logPath, verifiedPath} {
		if _, err := os.Stat(filepath.Join(repo, p)); err != nil {
			return fmt.Errorf("%w: %w", ErrRepo, err)
		}
	}
	return nil
}

const rehearsalHeader = "# Hardware tests (rehearsals)\n\n## Log\n\nNo runs yet.\n"

// recording is one unredacted transcript in the logs folder. extra are the
// other transcripts a stage recorded, such as H0's replayable session.
type recording struct {
	name  string
	path  string
	f     *os.File
	rec   *hidio.Recorder
	extra []*recording
}

func (r *runner) startRecording(name string) (*recording, error) {
	if err := os.MkdirAll(r.cfg.Logs, 0o700); err != nil {
		return nil, err
	}
	stamp := r.cfg.Now().UTC().Format("20060102T150405Z")
	var f *os.File
	var path string
	for i := 1; ; i++ {
		path = filepath.Join(r.cfg.Logs, stamp+"-hwtest-"+name+numbered(i)+".jsonl")
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i >= 100 {
			return nil, err
		}
	}
	rec, err := hidio.NewRecorder(f, hidio.Header{Source: r.cfg.Tool + " hwtest " + r.def.name, Started: r.cfg.Now()})
	if err != nil {
		f.Close()
		return nil, err
	}
	return &recording{name: name, path: path, f: f, rec: rec}, nil
}

func numbered(i int) string {
	if i == 1 {
		return ""
	}
	return "-" + strconv.Itoa(i)
}

func (rc *recording) close() error {
	var errs []error
	for _, x := range append(rc.extra, rc) {
		if x.f == nil {
			continue
		}
		errs = append(errs, x.rec.Err(), x.f.Close())
		x.f = nil
	}
	return errors.Join(errs...)
}

// commit writes the redacted copy of rc under the repo's transcripts and
// returns its path relative to the repo.
func (r *runner) commit(rc *recording) (string, error) {
	raw, err := os.ReadFile(rc.path)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := hidio.RedactTranscript(&b, bytes.NewReader(raw)); err != nil {
		return "", fmt.Errorf("%s: %w", rc.path, err)
	}
	dir := filepath.Join(r.cfg.Repo, filepath.FromSlash(transcriptsDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	date := r.res.Started.UTC().Format("2006-01-02")
	for i := 1; ; i++ {
		name := date + "-" + rc.name + numbered(i) + ".jsonl"
		err := writeRepoFile(filepath.Join(dir, name), b.Bytes(), false)
		if err == nil {
			return transcriptsDir + "/" + name, nil
		}
		if !errors.Is(err, fs.ErrExist) || i >= 100 {
			return "", err
		}
	}
}

var firmwarePattern = regexp.MustCompile(`^v[0-9]+\.[0-9a-f]{2}$`)

type verification struct {
	Model    string `json:"model"`
	Feature  string `json:"feature"`
	Firmware string `json:"firmware"`
	Stage    string `json:"stage"`
	Date     string `json:"date"`
}

// promote adds the stage's features to verified.json for this model and
// firmware, then regenerates the catalog code from it. When that fails the
// file is put back, so the two never disagree.
func (r *runner) promote(ctx context.Context) ([]catalog.Verification, error) {
	d := r.res.Device
	if !firmwarePattern.MatchString(d.Mouse) {
		return nil, fmt.Errorf("hwtest: nothing promoted: the mouse firmware %q is not a version verified.json takes", d.Mouse)
	}
	var add []catalog.Verification
	for _, f := range r.def.promotes {
		add = append(add, catalog.Verification{Model: d.Key, Feature: string(f), Firmware: d.Mouse, Stage: r.def.name,
			Date: r.res.Started.UTC().Format("2006-01-02")})
	}
	return promote(ctx, r.cfg.Repo, add, r.cfg.Generate)
}

func promote(ctx context.Context, repo string, add []catalog.Verification, generate func(context.Context, string) error) ([]catalog.Verification, error) {
	path := filepath.Join(repo, filepath.FromSlash(verifiedPath))
	old, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var vs []verification
	if err := json.Unmarshal(old, &vs); err != nil {
		return nil, fmt.Errorf("%s: %w", verifiedPath, err)
	}
	var added []catalog.Verification
	for _, a := range add {
		if slices.ContainsFunc(vs, func(v verification) bool {
			return v.Model == a.Model && v.Feature == a.Feature && v.Firmware == a.Firmware
		}) {
			continue
		}
		vs = append(vs, verification{a.Model, a.Feature, a.Firmware, a.Stage, a.Date})
		added = append(added, a)
	}
	if len(added) == 0 {
		return nil, nil
	}
	slices.SortStableFunc(vs, func(a, b verification) int {
		return strings.Compare(a.Model+"\x00"+a.Feature+"\x00"+a.Firmware, b.Model+"\x00"+b.Feature+"\x00"+b.Firmware)
	})
	data, err := json.MarshalIndent(vs, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeRepoFile(path, append(data, '\n'), true); err != nil {
		return nil, err
	}
	if err := generate(ctx, repo); err != nil {
		if rerr := writeRepoFile(path, old, true); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return nil, fmt.Errorf("hwtest: nothing promoted: regenerating the catalog failed: %w", err)
	}
	return added, nil
}

// goGenerate runs the catalog generator, which compiles verified.json into
// zz_verified.go; it needs the Go toolchain.
func goGenerate(ctx context.Context, repo string) error {
	cmd := exec.CommandContext(ctx, "go", "generate", "./internal/catalog")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go generate ./internal/catalog: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// FindRepo returns the arcctl checkout that holds dir.
func FindRepo(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		if mod, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil && slices.Contains(strings.Split(string(mod), "\n"), module) {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("%w: no checkout around %s", ErrRepo, dir)
		}
	}
}
