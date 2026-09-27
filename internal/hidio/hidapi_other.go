//go:build hidapi && !darwin

package hidio

// Only the macOS hidapi opens devices exclusively by default.
func openShared() {}
