package platform

import "testing"

type fakeProcs struct {
	responsible map[int]int // absent: the lookup is unavailable
	paths       map[int]string
	parents     map[int]int
}

func (f fakeProcs) responsibleFor(pid int) (int, bool) {
	rp, ok := f.responsible[pid]
	return rp, ok
}
func (f fakeProcs) path(pid int) string { return f.paths[pid] }
func (f fakeProcs) parent(pid int) int  { return f.parents[pid] }

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestResolveApp(t *testing.T) {
	const (
		self    = 500
		shell   = 400
		tmux    = 300
		ghostty = 2686
	)
	paths := map[int]string{
		self:    "/opt/homebrew/bin/arcctl",
		shell:   "/bin/zsh",
		tmux:    "/opt/homebrew/bin/tmux",
		ghostty: "/Applications/Ghostty.app/Contents/MacOS/ghostty",
		350:     "/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal",
	}
	tests := []struct {
		name   string
		procs  fakeProcs
		getenv func(string) string
		want   App
	}{
		{
			name:   "responsible terminal",
			procs:  fakeProcs{responsible: map[int]int{self: ghostty}, paths: paths, parents: map[int]int{self: shell, shell: ghostty}},
			getenv: env("TERM_PROGRAM", "ghostty"),
			want: App{Name: "Ghostty", Bundle: "/Applications/Ghostty.app", Via: ViaResponsibility,
				Proc: Process{PID: ghostty, Name: "ghostty", Path: paths[ghostty]}},
		},
		{
			name:   "inside tmux the server's app is named",
			procs:  fakeProcs{responsible: map[int]int{self: ghostty}, paths: paths, parents: map[int]int{self: shell, shell: tmux, tmux: 1}},
			getenv: env("TMUX", "/private/tmp/tmux-501/default,300,0", "TERM_PROGRAM", "tmux"),
			want: App{Name: "Ghostty", Bundle: "/Applications/Ghostty.app", Via: ViaResponsibility, Tmux: true,
				Proc: Process{PID: ghostty, Name: "ghostty", Path: paths[ghostty]}},
		},
		{
			name:   "tmux server outlived its terminal",
			procs:  fakeProcs{responsible: map[int]int{self: 9999}, paths: paths, parents: map[int]int{self: shell, shell: tmux, tmux: 1}},
			getenv: env("TERM_PROGRAM", "tmux"),
			want:   App{Tmux: true},
		},
		{
			name:   "not started from an app",
			procs:  fakeProcs{responsible: map[int]int{self: self}, paths: paths, parents: map[int]int{self: 1}},
			getenv: env(),
			want:   App{Name: "arcctl", Via: ViaSelf, Proc: Process{PID: self, Name: "arcctl", Path: paths[self]}},
		},
		{
			name:   "no responsibility lookup: nearest app ancestor",
			procs:  fakeProcs{paths: paths, parents: map[int]int{self: shell, shell: 350, 350: 1}},
			getenv: env("TERM_PROGRAM", "Apple_Terminal"),
			want: App{Name: "Terminal", Bundle: "/Applications/Utilities/Terminal.app", Via: ViaProcessTree,
				Proc: Process{PID: 350, Name: "Terminal", Path: paths[350]}},
		},
		{
			name:   "only TERM_PROGRAM",
			procs:  fakeProcs{paths: map[int]string{}, parents: map[int]int{self: shell}},
			getenv: env("TERM_PROGRAM", "WezTerm"),
			want:   App{Name: "WezTerm", Via: ViaTermProgram},
		},
		{
			name:   "nothing known",
			procs:  fakeProcs{},
			getenv: env(),
			want:   App{},
		},
		{
			name:   "a parent loop ends",
			procs:  fakeProcs{paths: map[int]string{}, parents: map[int]int{self: shell, shell: self}},
			getenv: env(),
			want:   App{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveApp(self, tt.procs, tt.getenv); got != tt.want {
				t.Fatalf("resolveApp =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}
