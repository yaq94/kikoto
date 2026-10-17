//go:build !windows

package httpapi

import (
	"time"

	"golang.org/x/sys/unix"
)

// calibrationProcessCPUSeconds reports this process's cumulative user plus
// system CPU time. The runtime's own CPU metric is not populated on every
// supported platform, so the calibration experiment reads the operating
// system's process accounting instead. A read that fails contributes zero.
func calibrationProcessCPUSeconds() float64 {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	user := time.Duration(usage.Utime.Sec)*time.Second + time.Duration(usage.Utime.Usec)*time.Microsecond
	system := time.Duration(usage.Stime.Sec)*time.Second + time.Duration(usage.Stime.Usec)*time.Microsecond
	return (user + system).Seconds()
}
