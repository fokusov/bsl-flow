//go:build windows

package repository

import (
	"syscall"
	"time"
	"unsafe"
)

// This file ports Test-BFNativeProcessDead: a stored process identity is
// dead unless a live process matches both its pid and its exact start time.

var (
	kernel32Recovery    = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess     = kernel32Recovery.NewProc("OpenProcess")
	procGetProcessTimes = kernel32Recovery.NewProc("GetProcessTimes")
	procCloseHandle     = kernel32Recovery.NewProc("CloseHandle")
)

const processQueryLimitedInformation = 0x1000

// filetimeToTime converts a FILETIME (100-ns units since 1601-01-01) into a
// wall-clock time.
func filetimeToTime(units uint64) time.Time {
	nanos := (int64(units) - 116444736000000000) * 100
	return time.Unix(0, nanos)
}

// native1CProcessStartTime reads the exact process start time of one pid
// through GetProcessTimes; the offline recovery tests use it to build a live
// identity fixture.
func native1CProcessStartTime(pid int64) (time.Time, error) {
	handle, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return time.Time{}, syscall.Errno(0x5)
	}
	defer procCloseHandle.Call(handle)
	var creation, exit, kernel, user uint64
	result, _, _ := procGetProcessTimes.Call(handle,
		uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if result == 0 {
		return time.Time{}, syscall.Errno(0x5)
	}
	return filetimeToTime(creation), nil
}

func native1CProcessDead(pid int64, startTimeUTC string) bool {
	if pid <= 0 {
		return true
	}
	handle, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return true
	}
	defer procCloseHandle.Call(handle)
	var creation, exit, kernel, user uint64
	result, _, _ := procGetProcessTimes.Call(handle,
		uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if result == 0 {
		return true
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.0000000Z", startTimeUTC)
	if err != nil {
		return true
	}
	actual := filetimeToTime(creation)
	return !actual.UTC().Equal(parsed.UTC())
}
