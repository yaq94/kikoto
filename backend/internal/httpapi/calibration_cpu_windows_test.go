//go:build windows

package httpapi

import "golang.org/x/sys/windows"

// calibrationProcessCPUSeconds reports this process's cumulative user plus
// kernel CPU time. The runtime's own CPU metric is not populated on every
// supported platform, so the calibration experiment reads the operating
// system's process accounting instead. A read that fails contributes zero.
//
// A Filetime counts 100-nanosecond ticks from 1601, so its own Nanoseconds
// multiplication overflows today; this converts the tick count directly.
func calibrationProcessCPUSeconds() float64 {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user); err != nil {
		return 0
	}
	ticks := filetimeTicks(kernel) + filetimeTicks(user)
	return float64(ticks) / 1e7
}

func filetimeTicks(value windows.Filetime) int64 {
	return int64(value.HighDateTime)<<32 + int64(value.LowDateTime)
}
