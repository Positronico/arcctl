package platform

import "testing"

func TestParseConsole(t *testing.T) {
	tests := []struct {
		name string
		d    map[string]any
		want Console
	}{
		{"locked", map[string]any{keyScreenLocked: true, keySecureInputPID: int64(200)}, Console{ScreenLocked: true, SecureInput: Process{PID: 200}}},
		{"secure input only", map[string]any{keySecureInputPID: int64(2686)}, Console{SecureInput: Process{PID: 2686}}},
		{"clear", map[string]any{keyScreenLocked: false}, Console{}},
		{"zero pid", map[string]any{keySecureInputPID: int64(0)}, Console{}},
		{"wrong types", map[string]any{keyScreenLocked: "Yes", keySecureInputPID: "200"}, Console{}},
		{"empty", nil, Console{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseConsole(tt.d); got != tt.want {
				t.Fatalf("parseConsole = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestConsoleSession(t *testing.T) {
	other := map[string]any{keySessionUID: int64(502), keyOnConsole: true, "id": "other"}
	background := map[string]any{keySessionUID: int64(501), keyOnConsole: false, "id": "background"}
	console := map[string]any{keySessionUID: int64(501), keyOnConsole: true, "id": "console"}
	tests := []struct {
		name  string
		users []any
		want  string
	}{
		{"on console wins", []any{other, background, console}, "console"},
		{"any session of the user", []any{other, background}, "background"},
		{"other users only", []any{other}, ""},
		{"junk", []any{"x", int64(1), nil}, ""},
		{"none", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := consoleSession(tt.users, 501)
			if ok != (tt.want != "") || ok && d["id"] != tt.want {
				t.Fatalf("consoleSession = %v, %v; want %q", d, ok, tt.want)
			}
		})
	}
}

func TestParseClient(t *testing.T) {
	tests := []struct {
		name    string
		creator any
		debug   any
		want    Client
		ok      bool
	}{
		{"shared", "pid 409, karabiner_observ", map[string]any{"ClientSeized": false, "ClientOptions": int64(0)}, Client{Process: Process{PID: 409, Name: "karabiner_observ"}}, true},
		{"seized flag", "pid 408, karabiner_grabbe", map[string]any{"ClientSeized": true}, Client{Process: Process{PID: 408, Name: "karabiner_grabbe"}, Seized: true}, true},
		{"seize option", "pid 12, tool", map[string]any{"ClientOptions": int64(1)}, Client{Process: Process{PID: 12, Name: "tool"}, Seized: true}, true},
		{"no debug state", "pid 77, Google Chrome He", nil, Client{Process: Process{PID: 77, Name: "Google Chrome He"}}, true},
		{"name with comma", "pid 5, a, b", nil, Client{Process: Process{PID: 5, Name: "a, b"}}, true},
		{"no creator", nil, nil, Client{}, false},
		{"bad text", "task 5, x", nil, Client{}, false},
		{"zero pid", "pid 0, kernel", nil, Client{}, false},
		{"huge pid", "pid 99999999999999999999, x", nil, Client{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseClient(tt.creator, tt.debug)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("parseClient = %+v, %v; want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
