//go:build !windows

package repository

import "time"

// On every non-windows platform native process ownership checks are moot:
// the native 1C runtime is a Windows-only capability and recovery never
// reaches this function.

func native1CProcessStartTime(pid int64) (time.Time, error) {
	return time.Time{}, nil
}

func native1CProcessDead(pid int64, startTimeUTC string) bool {
	return true
}
