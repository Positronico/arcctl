//go:build hidapi

package hidio

// Windows preempts goroutines without signals.
func blockSIGURG() {}
