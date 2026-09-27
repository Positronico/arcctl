package safety

import (
	"os"
	"testing"

	"github.com/positronico/arcctl/internal/plan"
)

// SkipSync makes j skip fsync, for tests of the executor rather than the
// disk. No release code can reach it or SetSync.
func SkipSync(j *Journal) { SetSync(j, func(*os.File) error { return nil }) }

// SetSync replaces the fsync of j's appends.
func SetSync(j *Journal, sync func(*os.File) error) { j.sync = sync }

// MkdirSynced is the folder creation OpenJournal uses, with sync in place of
// the directory fsync.
func MkdirSynced(dir string, sync func(string) error) error { return mkdirSynced(dir, sync) }

// Begin journals p as a new apply run without sending anything.
func Begin(t testing.TB, j *Journal, p plan.Plan) string {
	t.Helper()
	id, err := j.begin(KindApply, "", p)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Parse reads one journal file's bytes as Load would, without the disk.
func Parse(key string, b []byte) (*Status, error) {
	p := parser{key: key, runs: map[string]*Run{}}
	if err := p.file("fuzz"+fileExt, b); err != nil {
		return nil, err
	}
	return p.status()
}
