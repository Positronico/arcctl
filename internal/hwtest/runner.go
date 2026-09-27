//go:build hwtest

package hwtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

const (
	DefaultWait     = 60 * time.Second
	DefaultTraceFor = 20 * time.Second
	// abortCode is the exit code of --debug-abort-after-chunk, arcctl's
	// "aborted".
	abortCode = 8
)

var (
	ErrStage    = errors.New("hwtest: unknown stage")
	ErrDeclined = errors.New("hwtest: the user did not confirm the stage")
	ErrConfirm  = errors.New("hwtest: the typed confirmation does not match")
	ErrFlags    = errors.New("hwtest: the stage writes features the flags do not allow")
	ErrModel    = errors.New("hwtest: the stages are written for the EM11 Pro")
	ErrJournal  = errors.New("hwtest: the journal holds unfinished runs")
	ErrBackup   = errors.New("hwtest: the fresh backup is not complete")
	ErrRepo     = errors.New("hwtest: not an arcctl checkout")
	ErrNoDryRun = errors.New("hwtest: no dry run")
	// ErrEnded is what Run returns when --debug-abort-after-chunk ended the
	// stage as a crash would; a real run never returns it, because the
	// process is gone.
	ErrEnded = errors.New("hwtest: the debug abort ended the process")
	// ErrResume is a stage whose torn-write drill waits for the next run to
	// recover it and could not go on this time.
	ErrResume = errors.New("hwtest: the stage cannot resume after its drill")
	// ErrUnknownFeature is a stage whose feature this build cannot record:
	// running it would spend its drill for nothing.
	ErrUnknownFeature = errors.New("hwtest: this build cannot record the feature the stage verifies")
)

// Config is everything a stage needs from outside.
type Config struct {
	Raw RawDevices
	// Session is the template of every session a stage starts; the runner
	// sets Devices, Recorder and Device. Writes must be set for write stages.
	Session session.Options
	Host    Host // H0's questions to the OS; nil skips them
	Prompt  Prompter
	// Repo is the public checkout the redacted transcripts, the log entry
	// and verified.json go to; Logs holds the unredacted transcripts and
	// Backups the stage's backups.
	Repo    string
	Logs    string
	Backups string
	// Dump is the flash-dump.bin H0 compares with; empty is the one in Repo.
	Dump string
	// Source is backup.SourceDevice for a real device. Any other source is a
	// rehearsal: it is logged as one and promotes nothing.
	Source string
	Tool   string
	OS     keys.OS
	// Gates are the flags the user passed. With DryRun a write stage only
	// shows its preview; Confirm is the runner's own.
	Gates    safety.Gates
	Wait     time.Duration
	TraceFor time.Duration
	Poll     time.Duration // how often H0 checks whether the mouse sleeps
	// Listen is how long the raw path keeps listening after the first reply
	// to a write, for a second one.
	Listen          time.Duration
	AbortAfterChunk int
	Exit            func(code int)
	// Generate regenerates the catalog code after verified.json changed;
	// nil runs go generate in Repo.
	Generate func(ctx context.Context, repo string) error
	Now      func() time.Time

	// hook sees every op event of a write after the runner; afterReset runs
	// right after H7's reset went out. Tests act out the user in them.
	hook       func(safety.OpEvent)
	afterReset func()
	// pace replaces the pace of every H9 drill.
	pace time.Duration
}

func (c Config) withDefaults() Config {
	if c.Wait <= 0 {
		c.Wait = DefaultWait
	}
	if c.TraceFor <= 0 {
		c.TraceFor = DefaultTraceFor
	}
	if c.Poll <= 0 {
		c.Poll = time.Second
	}
	if c.Listen <= 0 {
		c.Listen = listenFor
	}
	if c.Exit == nil {
		c.Exit = os.Exit
	}
	if c.Generate == nil {
		c.Generate = goGenerate
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Source == "" {
		c.Source = backup.SourceDevice
	}
	if c.Dump == "" {
		c.Dump = filepath.Join(c.Repo, "testdata", "flash-dump.bin")
	}
	return c
}

// Result is what a stage did. Err is why it stopped early, if it did. A stage
// that stopped before the user confirmed it did nothing to record: Recorded
// is false, and only the local transcript exists. A dry run records nothing
// at all.
type Result struct {
	Stage       string
	Title       string
	Rehearsal   bool
	DryRun      bool
	Recorded    bool
	Passed      bool
	Started     time.Time
	Ended       time.Time
	Device      Device
	Steps       []StepResult
	Answers     []Answer
	Findings    []string
	Promoted    []catalog.Verification
	Transcripts []string // relative to the repo
	Backups     []string // local paths; never logged
	Err         error
}

type Device struct {
	Model    string
	Key      string
	MID      byte
	Mouse    string // firmware, cmd 18
	Receiver string // firmware, cmd 29
	Conn     string
	Profile  string
}

type StepResult struct {
	Title  string
	OK     bool
	Detail []string
}

type Answer struct {
	ID       string
	Question string
	Answer   string
	OK       bool
}

type runner struct {
	cfg    Config
	def    *stageDef
	devs   *devices
	res    *Result
	rec    *recording
	pauses int  // times watch saw a job paused for a sleeping mouse
	begun  bool // the user confirmed the stage
	// fresh is the stage's fresh full backup, as read back from its file.
	fresh *backup.File
	// at is the step running now. drills reports a torn-write drill among
	// the steps, drill is set while it writes, and ended once the debug
	// abort ended the stage.
	at     int
	drills bool
	drill  *drillState
	ended  bool
	// fingerprint identifies the stage's steps, for a resumed run.
	fingerprint string
	// seen are the extra features whose effect the user saw.
	seen []mouse.Feature
}

// Stages lists the stages this build runs.
func Stages() []string {
	out := make([]string, len(stages))
	for i, s := range stages {
		out[i] = s.name
	}
	return out
}

// Run runs one stage and records it. The error is set only when the stage
// could not start or its records could not be written; a stage that ran and
// failed returns a Result with Passed false.
func Run(ctx context.Context, cfg Config, stage string) (*Result, error) {
	i := slices.IndexFunc(stages, func(s *stageDef) bool { return strings.EqualFold(s.name, stage) })
	if i < 0 {
		return nil, fmt.Errorf("%w %q (have %s)", ErrStage, stage, strings.Join(Stages(), ", "))
	}
	cfg = cfg.withDefaults()
	def := stages[i]
	r := &runner{cfg: cfg, def: def, devs: newDevices(cfg.Raw), res: &Result{
		Stage: def.name, Title: def.title, Rehearsal: cfg.Source != backup.SourceDevice, DryRun: cfg.Gates.DryRun, Started: cfg.Now(),
	}}
	if !r.res.DryRun {
		for _, f := range slices.Concat(def.promotes, def.extras) {
			if !slices.Contains(knownFeatures(), f) {
				return nil, fmt.Errorf("%w: stage %s verifies %s, which internal/mouse does not list, so its run would record nothing", ErrUnknownFeature, def.name, f)
			}
		}
	}
	cp, err := loadCheckpoint(cfg.Logs, def.name)
	if err != nil {
		return nil, err
	}
	if r.res.DryRun {
		if cp != nil {
			return nil, fmt.Errorf("%w: stage %s has a run to finish (%s); run it again without --dry-run", ErrResume, def.name, checkpointPath(cfg.Logs, def.name))
		}
		return r.dryRun(ctx)
	}
	if err := r.checkRepo(); err != nil {
		return nil, err
	}
	rec, err := r.startRecording(strings.ToLower(def.name))
	if err != nil {
		return nil, err
	}
	r.rec = rec
	r.say(fmt.Sprintf("Stage %s: %s", def.name, def.title))
	if r.res.Rehearsal {
		r.say("Rehearsal on the " + cfg.Source + ": nothing this run finds is promoted.")
	}
	var runErr error
	switch {
	case cp != nil && cp.Kind == checkpointReset:
		runErr = r.resumeReset(ctx, cp)
	case cp != nil:
		runErr = r.resume(ctx, cp)
	case def.run != nil:
		runErr = def.run(ctx, r)
	default:
		runErr = r.writeStage(ctx)
	}
	switch {
	case r.ended && r.res.Rehearsal && r.drills && cp == nil:
		if err := r.rec.close(); err != nil {
			return r.res, err
		}
		r.say("Rehearsal: the emulated mouse lives in this process, so the stage resumes here, as its next run would.")
		return Run(ctx, cfg, stage)
	case r.ended:
		r.res.Err = ErrEnded
		r.res.Ended = r.cfg.Now()
		return r.res, r.rec.close()
	case errors.Is(runErr, ErrResume):
		r.res.Err = runErr
		r.res.Ended = r.cfg.Now()
		r.say("Stopped: " + runErr.Error())
		r.say("Nothing recorded yet; the stage resumes on its next run.")
		return r.res, r.rec.close()
	}
	res, err := r.finish(ctx, runErr)
	switch {
	case cp != nil && cp.Kind != checkpointReset:
		err = errors.Join(err, removeCheckpoint(cfg.Logs, def.name))
	case res.Recorded:
		err = errors.Join(err, r.markRecorded())
	}
	return res, err
}

func (r *runner) say(text string) {
	if r.cfg.Prompt != nil {
		r.cfg.Prompt.Say(text)
	}
}

// note puts text in the stage's transcript. Notes are kept as written by
// the redaction, so they never hold an address or the user's key bytes.
func (r *runner) note(text string) {
	if r.rec != nil && r.rec.rec != nil {
		r.rec.rec.Note(scrub(text))
	}
}

func (r *runner) step(title string, ok bool, detail ...string) {
	r.res.Steps = append(r.res.Steps, StepResult{Title: title, OK: ok, Detail: detail})
	mark := "ok"
	if !ok {
		mark = "FAILED"
	}
	r.say(fmt.Sprintf("  %s: %s", title, mark))
	for _, d := range detail {
		r.say("    " + d)
	}
	r.note(fmt.Sprintf("step %q: %s", title, mark))
}

func (r *runner) finding(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	r.res.Findings = append(r.res.Findings, s)
	r.note("finding: " + s)
}

// ask asks a yes/no question; the answer passes when it equals want.
func (r *runner) ask(id, text string, want bool) (bool, error) {
	got, err := r.cfg.Prompt.Ask(id, text)
	if err != nil {
		return false, err
	}
	ok := got == want
	r.res.Answers = append(r.res.Answers, Answer{ID: id, Question: text, Answer: yesNo(got), OK: ok})
	r.note(fmt.Sprintf("answer %s: %s", id, yesNo(got)))
	return ok, nil
}

// line asks for a line of text, which is recorded as the answer.
func (r *runner) line(id, text string) (string, error) {
	got, err := r.cfg.Prompt.Line(id, text)
	if err != nil {
		return "", err
	}
	got = strings.TrimSpace(got)
	r.res.Answers = append(r.res.Answers, Answer{ID: id, Question: text, Answer: got, OK: true})
	r.note(fmt.Sprintf("answer %s: %q", id, got))
	return got, nil
}

func (r *runner) wait(id, text string) error {
	r.note("instruction " + id)
	return r.cfg.Prompt.Wait(id, text)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (r *runner) describe(sn *session.Snapshot) {
	d := Device{Mouse: sn.Versions.Mouse, Receiver: sn.Versions.Receiver, Profile: "none"}
	if sn.Model != nil {
		d.Model, d.Key = sn.Model.Name, sn.Model.Key
	}
	if h := sn.Handshake; h != nil {
		d.MID, d.Conn = h.MID, h.ConnString()
	}
	if sn.Profile.Supported {
		d.Profile = fmt.Sprint(sn.Profile.Value)
	}
	r.res.Device = d
	r.note(fmt.Sprintf("device %s mid %d, mouse %s, receiver %s, %s, profile %s", d.Key, d.MID, d.Mouse, d.Receiver, d.Conn, d.Profile))
}

// finish closes the transcript and writes the records: the redacted
// transcripts, the verified features and the log entry.
func (r *runner) finish(ctx context.Context, runErr error) (*Result, error) {
	res := r.res
	res.Ended = r.cfg.Now()
	res.Err = runErr
	res.Passed = runErr == nil && !slices.ContainsFunc(res.Steps, func(s StepResult) bool { return !s.OK }) &&
		!slices.ContainsFunc(res.Answers, func(a Answer) bool { return !a.OK })
	if runErr != nil {
		r.note("stopped: " + runErr.Error())
		r.say("Stopped: " + runErr.Error())
	}
	var errs []error
	if err := r.rec.close(); err != nil {
		errs = append(errs, err)
	}
	if !r.begun {
		r.say("Nothing recorded: the stage did not start. The transcript stays in " + r.rec.path)
		return res, errors.Join(errs...)
	}
	res.Recorded = true
	for _, rc := range append(r.rec.extra, r.rec) {
		path, err := r.commit(rc)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		res.Transcripts = append(res.Transcripts, path)
	}
	if res.Passed && !res.Rehearsal && len(r.def.promotes)+len(r.def.extras) > 0 {
		vs, err := r.promote(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		res.Promoted = vs
	}
	if err := r.log(); err != nil {
		errs = append(errs, err)
	}
	return res, errors.Join(errs...)
}
