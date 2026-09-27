package platform

import (
	"regexp"
	"strconv"
)

// The registry and session values arrive as CoreFoundation property lists
// converted to Go: string, int64, bool, []any and map[string]any.

func asInt(v any) (int64, bool) {
	n, ok := v.(int64)
	return n, ok
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

const (
	keySecureInputPID = "kCGSSessionSecureInputPID"
	keyScreenLocked   = "CGSSessionScreenIsLocked"
	keySessionUID     = "kCGSSessionUserIDKey"
	keyOnConsole      = "kCGSSessionOnConsoleKey"
)

func parseConsole(d map[string]any) Console {
	c := Console{ScreenLocked: asBool(d[keyScreenLocked])}
	if pid, ok := asInt(d[keySecureInputPID]); ok && pid > 0 {
		c.SecureInput.PID = int(pid)
	}
	return c
}

// consoleSession picks uid's session from the registry root's IOConsoleUsers,
// preferring the one on the console.
func consoleSession(users []any, uid int) (map[string]any, bool) {
	var found map[string]any
	for _, u := range users {
		d, ok := u.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := asInt(d[keySessionUID]); !ok || id != int64(uid) {
			continue
		}
		if asBool(d[keyOnConsole]) {
			return d, true
		}
		if found == nil {
			found = d
		}
	}
	return found, found != nil
}

var creatorText = regexp.MustCompile(`^pid (\d+), (.*)$`)

// parseClient reads an IOHIDLibUserClient's IOUserClientCreator ("pid 409,
// karabiner_observ", the name cut to 16 bytes) and DebugState.
func parseClient(creator, debug any) (Client, bool) {
	s, _ := creator.(string)
	m := creatorText.FindStringSubmatch(s)
	if m == nil {
		return Client{}, false
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil || pid <= 0 {
		return Client{}, false
	}
	c := Client{Process: Process{PID: pid, Name: m[2]}}
	if d, ok := debug.(map[string]any); ok {
		opts, _ := asInt(d["ClientOptions"])
		c.Seized = asBool(d["ClientSeized"]) || opts&seizeOption != 0
	}
	return c, true
}

const seizeOption = 1 // kIOHIDOptionsTypeSeizeDevice
