//go:build hidapi && !windows

package hidio

/*
#include <pthread.h>
#include <signal.h>

static int arcctl_block_sigurg(void) {
	sigset_t s;
	sigemptyset(&s);
	sigaddset(&s, SIGURG);
	return pthread_sigmask(SIG_BLOCK, &s, NULL);
}
*/
import "C"

// blockSIGURG keeps the runtime's preemption signal off the calling thread,
// which must be locked to its goroutine for good.
func blockSIGURG() { C.arcctl_block_sigurg() }
