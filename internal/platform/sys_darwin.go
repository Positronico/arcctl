package platform

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/ebitengine/purego"
)

const (
	iokitPath = "/System/Library/Frameworks/IOKit.framework/IOKit"
	cfPath    = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	cgPath    = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"

	cfStringEncodingUTF8 = 0x08000100
	cfNumberSInt64Type   = 4

	ioRegistryIterateRecursively = 1
	ioRegistryIterateParents     = 2

	procPidTBSDInfo     = 3
	procBSDInfoSize     = 136
	procBSDInfoPPIDAt   = 16
	procPidPathInfoSize = 4096
)

var (
	ioHIDCheckAccess                func(requestType uint32) uint32
	ioHIDRequestAccess              func(requestType uint32) bool
	ioServiceMatching               func(name string) uintptr
	ioServiceGetMatchingServices    func(mainPort uint32, matching uintptr, iterator *uint32) int32
	ioIteratorNext                  func(iterator uint32) uint32
	ioObjectRelease                 func(object uint32) int32
	ioObjectConformsTo              func(object uint32, className string) uint32
	ioRegistryGetRootEntry          func(mainPort uint32) uint32
	ioRegistryEntryCreateCFProperty func(entry uint32, key uintptr, allocator uintptr, options uint32) uintptr
	ioRegistryEntrySearchCFProperty func(entry uint32, plane string, key uintptr, allocator uintptr, options uint32) uintptr
	ioRegistryEntryGetChildIterator func(entry uint32, plane string, iterator *uint32) int32
	ioRegistryEntryGetPath          func(entry uint32, plane string, path []byte) int32
	ioRegistryEntryGetRegistryID    func(entry uint32, id *uint64) int32

	cfRelease                      func(cf uintptr)
	cfGetTypeID                    func(cf uintptr) uintptr
	cfStringGetTypeID              func() uintptr
	cfNumberGetTypeID              func() uintptr
	cfBooleanGetTypeID             func() uintptr
	cfDictionaryGetTypeID          func() uintptr
	cfArrayGetTypeID               func() uintptr
	cfStringCreateWithCString      func(allocator uintptr, s string, encoding uint32) uintptr
	cfStringGetLength              func(s uintptr) int
	cfStringGetMaximumSizeForEnc   func(length int, encoding uint32) int
	cfStringGetCString             func(s uintptr, buf []byte, size int, encoding uint32) bool
	cfNumberGetValue               func(n uintptr, typ int, out *int64) bool
	cfBooleanGetValue              func(b uintptr) bool
	cfDictionaryGetCount           func(d uintptr) int
	cfDictionaryGetKeysAndValues   func(d uintptr, keys []uintptr, values []uintptr)
	cfArrayGetCount                func(a uintptr) int
	cfArrayGetValueAtIndex         func(a uintptr, i int) uintptr
	cgSessionCopyCurrentDictionary func() uintptr

	procPidPath             func(pid int32, buf []byte, size uint32) int32
	procPidInfo             func(pid int32, flavor int32, arg uint64, buf []byte, size int32) int32
	responsibilityPIDForPID func(pid int32) int32
)

var cfTypes struct{ str, num, boolean, dict, array uintptr }

var (
	loadOnce sync.Once
	loadErr  error
)

// load binds the macOS functions through purego. CoreGraphics and the
// responsibility lookup are optional; everything else is required.
func load() error {
	loadOnce.Do(func() { loadErr = bind() })
	return loadErr
}

type symbol struct {
	fn       any
	lib      string
	name     string
	optional bool
}

func bind() error {
	syms := []symbol{
		{&ioHIDCheckAccess, iokitPath, "IOHIDCheckAccess", false},
		{&ioHIDRequestAccess, iokitPath, "IOHIDRequestAccess", false},
		{&ioServiceMatching, iokitPath, "IOServiceMatching", false},
		{&ioServiceGetMatchingServices, iokitPath, "IOServiceGetMatchingServices", false},
		{&ioIteratorNext, iokitPath, "IOIteratorNext", false},
		{&ioObjectRelease, iokitPath, "IOObjectRelease", false},
		{&ioObjectConformsTo, iokitPath, "IOObjectConformsTo", false},
		{&ioRegistryGetRootEntry, iokitPath, "IORegistryGetRootEntry", false},
		{&ioRegistryEntryCreateCFProperty, iokitPath, "IORegistryEntryCreateCFProperty", false},
		{&ioRegistryEntrySearchCFProperty, iokitPath, "IORegistryEntrySearchCFProperty", false},
		{&ioRegistryEntryGetChildIterator, iokitPath, "IORegistryEntryGetChildIterator", false},
		{&ioRegistryEntryGetPath, iokitPath, "IORegistryEntryGetPath", false},
		{&ioRegistryEntryGetRegistryID, iokitPath, "IORegistryEntryGetRegistryEntryID", false},
		{&cfRelease, cfPath, "CFRelease", false},
		{&cfGetTypeID, cfPath, "CFGetTypeID", false},
		{&cfStringGetTypeID, cfPath, "CFStringGetTypeID", false},
		{&cfNumberGetTypeID, cfPath, "CFNumberGetTypeID", false},
		{&cfBooleanGetTypeID, cfPath, "CFBooleanGetTypeID", false},
		{&cfDictionaryGetTypeID, cfPath, "CFDictionaryGetTypeID", false},
		{&cfArrayGetTypeID, cfPath, "CFArrayGetTypeID", false},
		{&cfStringCreateWithCString, cfPath, "CFStringCreateWithCString", false},
		{&cfStringGetLength, cfPath, "CFStringGetLength", false},
		{&cfStringGetMaximumSizeForEnc, cfPath, "CFStringGetMaximumSizeForEncoding", false},
		{&cfStringGetCString, cfPath, "CFStringGetCString", false},
		{&cfNumberGetValue, cfPath, "CFNumberGetValue", false},
		{&cfBooleanGetValue, cfPath, "CFBooleanGetValue", false},
		{&cfDictionaryGetCount, cfPath, "CFDictionaryGetCount", false},
		{&cfDictionaryGetKeysAndValues, cfPath, "CFDictionaryGetKeysAndValues", false},
		{&cfArrayGetCount, cfPath, "CFArrayGetCount", false},
		{&cfArrayGetValueAtIndex, cfPath, "CFArrayGetValueAtIndex", false},
		{&cgSessionCopyCurrentDictionary, cgPath, "CGSessionCopyCurrentDictionary", true},
		{&procPidPath, "", "proc_pidpath", false},
		{&procPidInfo, "", "proc_pidinfo", false},
		{&responsibilityPIDForPID, "", "responsibility_get_pid_responsible_for_pid", true},
	}
	handles := map[string]uintptr{"": purego.RTLD_DEFAULT}
	failed := map[string]error{}
	for _, s := range syms {
		h, ok := handles[s.lib]
		if !ok && failed[s.lib] == nil {
			var err error
			if h, err = purego.Dlopen(s.lib, purego.RTLD_LAZY|purego.RTLD_GLOBAL); err != nil {
				failed[s.lib] = err
			} else {
				handles[s.lib], ok = h, true
			}
		}
		var addr uintptr
		err := failed[s.lib]
		if ok {
			addr, err = purego.Dlsym(h, s.name)
		}
		if err != nil {
			if s.optional {
				continue
			}
			return fmt.Errorf("platform: %s: %w", s.name, err)
		}
		purego.RegisterFunc(s.fn, addr)
	}
	cfTypes.str = cfStringGetTypeID()
	cfTypes.num = cfNumberGetTypeID()
	cfTypes.boolean = cfBooleanGetTypeID()
	cfTypes.dict = cfDictionaryGetTypeID()
	cfTypes.array = cfArrayGetTypeID()
	return nil
}

const maxDepth = 8

// goValue converts a CoreFoundation property list to Go: string, int64, bool,
// []any or map[string]any. Other types (data, dates, floats) become nil.
func goValue(ref uintptr, depth int) any {
	if ref == 0 || depth > maxDepth {
		return nil
	}
	switch cfGetTypeID(ref) {
	case cfTypes.str:
		return goString(ref)
	case cfTypes.num:
		var v int64
		if cfNumberGetValue(ref, cfNumberSInt64Type, &v) {
			return v
		}
	case cfTypes.boolean:
		return cfBooleanGetValue(ref)
	case cfTypes.array:
		n := cfArrayGetCount(ref)
		out := make([]any, n)
		for i := range n {
			out[i] = goValue(cfArrayGetValueAtIndex(ref, i), depth+1)
		}
		return out
	case cfTypes.dict:
		n := cfDictionaryGetCount(ref)
		out := make(map[string]any, n)
		if n == 0 {
			return out
		}
		keys, values := make([]uintptr, n), make([]uintptr, n)
		cfDictionaryGetKeysAndValues(ref, keys, values)
		for i, k := range keys {
			if k != 0 && cfGetTypeID(k) == cfTypes.str {
				out[goString(k)] = goValue(values[i], depth+1)
			}
		}
		return out
	}
	return nil
}

func goString(ref uintptr) string {
	size := cfStringGetMaximumSizeForEnc(cfStringGetLength(ref), cfStringEncodingUTF8) + 1
	if size <= 1 {
		return ""
	}
	buf := make([]byte, size)
	if !cfStringGetCString(ref, buf, size, cfStringEncodingUTF8) {
		return ""
	}
	return cString(buf)
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func withCFString(s string, f func(uintptr) uintptr) any {
	key := cfStringCreateWithCString(0, s, cfStringEncodingUTF8)
	if key == 0 {
		return nil
	}
	defer cfRelease(key)
	ref := f(key)
	if ref == 0 {
		return nil
	}
	defer cfRelease(ref)
	return goValue(ref, 0)
}

func property(entry uint32, key string) any {
	return withCFString(key, func(k uintptr) uintptr { return ioRegistryEntryCreateCFProperty(entry, k, 0, 0) })
}

// inheritedProperty looks for key on entry and then on its parents.
func inheritedProperty(entry uint32, key string) any {
	return withCFString(key, func(k uintptr) uintptr {
		return ioRegistryEntrySearchCFProperty(entry, "IOService", k, 0, ioRegistryIterateRecursively|ioRegistryIterateParents)
	})
}

type darwinProcs struct{}

func (darwinProcs) responsibleFor(pid int) (int, bool) {
	if responsibilityPIDForPID == nil {
		return 0, false
	}
	rp := int(responsibilityPIDForPID(int32(pid)))
	return rp, rp > 0
}

func (darwinProcs) path(pid int) string {
	buf := make([]byte, procPidPathInfoSize)
	if n := procPidPath(int32(pid), buf, uint32(len(buf))); n > 0 && int(n) <= len(buf) {
		return string(buf[:n])
	}
	return ""
}

func (darwinProcs) parent(pid int) int {
	buf := make([]byte, procBSDInfoSize)
	if procPidInfo(int32(pid), procPidTBSDInfo, 0, buf, int32(len(buf))) != procBSDInfoSize {
		return 0
	}
	return int(binary.LittleEndian.Uint32(buf[procBSDInfoPPIDAt:]))
}

func process(pid int, fallback string) Process {
	p := Process{PID: pid, Name: fallback, Path: darwinProcs{}.path(pid)}
	if p.Path != "" {
		p.Name = filepath.Base(p.Path)
	}
	return p
}
