//go:build !windows

package schedule

import (
	"syscall"
)

// processCPUMs returns the total CPU time (user+system) the process has
// consumed since startup, in milliseconds. Used for per-job CPU spike
// reporting: by capturing this at SetRunning() and again at SetIdle() /
// SetError() we can attribute a CPU delta to each background job.
//
// This is process-wide CPU, not goroutine-isolated, so the number is most
// meaningful when one heavy job runs at a time. For mixed workloads it
// still gives a useful "this run consumed N seconds of CPU vs M seconds
// of wall clock" signal — anything where CPU/wall > 1 is a spike worth
// looking at.
func processCPUMs() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	utimeMs := ru.Utime.Sec*1000 + int64(ru.Utime.Usec)/1000
	stimeMs := ru.Stime.Sec*1000 + int64(ru.Stime.Usec)/1000
	return utimeMs + stimeMs
}
