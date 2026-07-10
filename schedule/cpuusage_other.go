//go:build !linux

package schedule

// CPUUsagePercent is a stub for non-Linux platforms.
func CPUUsagePercent() float64 {
	return 0
}
