package platform

import "path/filepath"

type procInfo interface {
	responsibleFor(pid int) (int, bool) // false when the lookup is unavailable or fails
	path(pid int) string                // "" when unknown
	parent(pid int) int                 // 0 when unknown
}

const maxAncestors = 64

// resolveApp names the app macOS charges self's Input Monitoring grant to. The
// kernel's responsible-process record comes first; the nearest ancestor inside
// an app bundle and then TERM_PROGRAM are fallbacks for when it is missing or
// names a process that has exited (a tmux server outliving its terminal).
func resolveApp(self int, p procInfo, getenv func(string) string) App {
	app := App{Tmux: getenv("TMUX") != ""}
	if rp, ok := p.responsibleFor(self); ok && rp > 0 {
		if path := p.path(rp); path != "" {
			app.Proc = Process{PID: rp, Name: filepath.Base(path), Path: path}
			app.Via = ViaResponsibility
			if rp == self {
				app.Via = ViaSelf
			}
			app.Bundle, app.Name = bundleOf(path)
			if app.Name == "" {
				app.Name = app.Proc.Name
			}
			return app
		}
	}
	pid := p.parent(self)
	for range maxAncestors {
		if pid <= 1 {
			break
		}
		path := p.path(pid)
		if bundle, name := bundleOf(path); bundle != "" {
			app.Proc = Process{PID: pid, Name: filepath.Base(path), Path: path}
			app.Bundle, app.Name, app.Via = bundle, name, ViaProcessTree
			return app
		}
		if filepath.Base(path) == "tmux" {
			app.Tmux = true
		}
		pid = p.parent(pid)
	}
	if tp := getenv("TERM_PROGRAM"); tp != "" && tp != "tmux" {
		app.Name, app.Via = tp, ViaTermProgram
	}
	return app
}
