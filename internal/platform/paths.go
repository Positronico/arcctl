package platform

import (
	"os"
	"path/filepath"
	"runtime"
)

// Paths are arcctl's files. Data is os.UserConfigDir()/arcctl.
type Paths struct {
	Data    string
	Backups string
	Journal string
	Macros  string // macro library
	Clients string // HID clients the user marked benign
	Lock    string // single-instance lock
	Cache   string // last-known images, <identity>.json
	Logs    string // packet log and unredacted transcripts
}

// DefaultPaths resolves Paths for this user. Nothing is created.
func DefaultPaths() (Paths, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return pathsFor(runtime.GOOS, config, cache, home, os.Getenv), nil
}

func pathsFor(goos, config, cache, home string, getenv func(string) string) Paths {
	data := filepath.Join(config, "arcctl")
	p := Paths{
		Data:    data,
		Backups: filepath.Join(data, "backups"),
		Journal: filepath.Join(data, "journal"),
		Macros:  filepath.Join(data, "macros.json"),
		Clients: filepath.Join(data, "clients.json"),
		Lock:    filepath.Join(data, "arcctl.lock"),
		Cache:   filepath.Join(cache, "arcctl"),
	}
	switch goos {
	case "darwin":
		p.Logs = filepath.Join(home, "Library", "Logs", "arcctl")
	case "windows":
		p.Logs = filepath.Join(cache, "arcctl", "logs")
	default:
		state := getenv("XDG_STATE_HOME")
		if !filepath.IsAbs(state) {
			state = filepath.Join(home, ".local", "state")
		}
		p.Logs = filepath.Join(state, "arcctl", "logs")
	}
	return p
}
