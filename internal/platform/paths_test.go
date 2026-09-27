package platform

import (
	"path/filepath"
	"testing"
)

func TestPathsFor(t *testing.T) {
	j := filepath.Join
	tests := []struct {
		name                string
		goos                string
		config, cache, home string
		env                 func(string) string
		data, cacheDir, log string
	}{
		{"darwin", "darwin", "/Users/u/Library/Application Support", "/Users/u/Library/Caches", "/Users/u", env(),
			j("/Users/u/Library/Application Support", "arcctl"), j("/Users/u/Library/Caches", "arcctl"), j("/Users/u", "Library", "Logs", "arcctl")},
		{"linux default state", "linux", "/home/u/.config", "/home/u/.cache", "/home/u", env(),
			j("/home/u/.config", "arcctl"), j("/home/u/.cache", "arcctl"), j("/home/u", ".local", "state", "arcctl", "logs")},
		{"linux XDG_STATE_HOME", "linux", "/home/u/.config", "/home/u/.cache", "/home/u", env("XDG_STATE_HOME", "/var/state/u"),
			j("/home/u/.config", "arcctl"), j("/home/u/.cache", "arcctl"), j("/var/state/u", "arcctl", "logs")},
		{"linux relative XDG_STATE_HOME is ignored", "linux", "/home/u/.config", "/home/u/.cache", "/home/u", env("XDG_STATE_HOME", "state"),
			j("/home/u/.config", "arcctl"), j("/home/u/.cache", "arcctl"), j("/home/u", ".local", "state", "arcctl", "logs")},
		{"windows", "windows", "C:/Users/u/AppData/Roaming", "C:/Users/u/AppData/Local", "C:/Users/u", env(),
			j("C:/Users/u/AppData/Roaming", "arcctl"), j("C:/Users/u/AppData/Local", "arcctl"), j("C:/Users/u/AppData/Local", "arcctl", "logs")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := Paths{
				Data:    tt.data,
				Backups: j(tt.data, "backups"),
				Journal: j(tt.data, "journal"),
				Macros:  j(tt.data, "macros.json"),
				Clients: j(tt.data, "clients.json"),
				Lock:    j(tt.data, "arcctl.lock"),
				Cache:   tt.cacheDir,
				Logs:    tt.log,
			}
			if got := pathsFor(tt.goos, tt.config, tt.cache, tt.home, tt.env); got != want {
				t.Fatalf("pathsFor =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

func TestDefaultPaths(t *testing.T) {
	p, err := DefaultPaths()
	if err != nil {
		t.Skip(err)
	}
	for _, s := range []string{p.Data, p.Backups, p.Journal, p.Macros, p.Clients, p.Lock, p.Cache, p.Logs} {
		if !filepath.IsAbs(s) {
			t.Errorf("%q is not absolute", s)
		}
	}
}
