//go:build windows

package schedule

// processCPUMs is a no-op stub on Windows (where syscall.Getrusage is not
// available). The dev box runs Windows but the production target is Linux,
// so per-job CPU stats only need to work there. Returns 0 so the CPU delta
// is always 0 and the per-job log line shows "cpu=0ms" rather than panicking.
func processCPUMs() int64 {
	return 0
}
