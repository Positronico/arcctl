package tui

import "charm.land/bubbles/v2/key"

// shellKeys are the keys the shell takes before the active tab (§7.2).
type shellKeys struct {
	Quit, Help, Next, Prev, Review, Discard, Revert, Reload, Backup key.Binding
	Up, Down, Choose, Clear, Settle, Settings                       key.Binding
}

func newShellKeys() shellKeys {
	b := func(k, help string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(k, help))
	}
	return shellKeys{
		Quit:     b("q", "quit", "q", "ctrl+c"),
		Help:     b("?", "help", "?"),
		Next:     b("tab", "next tab", "tab"),
		Prev:     b("shift+tab", "previous tab", "shift+tab"),
		Review:   b("a", "review & apply", "a"),
		Discard:  b("u", "discard", "u"),
		Revert:   b("U", "revert last apply", "U"),
		Reload:   b("r", "reload", "r"),
		Backup:   b("b", "back up", "b"),
		Up:       b("↑/k", "up", "up", "k"),
		Down:     b("↓/j", "down", "down", "j"),
		Choose:   b("enter", "use this device", "enter"),
		Clear:    b("c", "clear conflict", "c"),
		Settle:   b("R", "settle unfinished write", "R"),
		Settings: b("o", "open settings", "o"),
	}
}
