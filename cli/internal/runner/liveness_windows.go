//go:build windows

package runner

import (
	"syscall"
	"time"
	"unsafe"
)

// Port of the GetProcessTimes-based identity check (Test-BFNativeProcessDead):
// a stored identity is alive only when a live process matches both its pid and
// its exact start time, so a recycled pid never masquerades as the owner.

var (
	kernel32Liveness    = syscall.NewLazyDLL("kernel32.dll")
	livenessOpenProcess = kernel32Liveness.NewProc("OpenProcess")
	livenessProcessTime = kernel32Liveness.NewProc("GetProcessTimes")
	livenessCloseHandle = kernel32Liveness.NewProc("CloseHandle")
)

const livenessQueryLimitedInformation = 0x1000

func osProcessAlive(pid int64, startTimeUTC string) bool {
	if pid <= 0 {
		return false
	}
	handle, _, _ := livenessOpenProcess.Call(livenessQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return false
	}
	defer livenessCloseHandle.Call(handle)
	var creation, exit, kernel, user uint64
	result, _, _ := livenessProcessTime.Call(handle,
		uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if result == 0 {
		return false
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.0000000Z", startTimeUTC)
	if err != nil {
		return false
	}
	actual := time.Unix(0, (int64(creation)-116444736000000000)*100)
	return actual.UTC().Equal(parsed.UTC())
}
