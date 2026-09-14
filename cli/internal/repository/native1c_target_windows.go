//go:build windows

package repository

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// This file ports the Windows physical identity resolution of
// Get-BFRuntimeTargetIdentity (kernel32!GetFinalPathNameByHandle) and the
// runtime journal root (Get-BFNativeJournalRoot).

var (
	kernel32Native       = syscall.NewLazyDLL("kernel32.dll")
	procFinalPathName    = kernel32Native.NewProc("GetFinalPathNameByHandleW")
)

// native1CPhysicalTargetIdentity resolves the physical path of the database
// marker and requires it to be the requested target directory: aliased,
// substituted or UNC FILE targets are not admitted.
func native1CPhysicalTargetIdentity(target, marker string) (string, error) {
	handle, err := os.OpenFile(marker, os.O_RDONLY, 0)
	if err != nil {
		return "", blocked("FILE target marker is required to resolve physical target identity.")
	}
	defer handle.Close()
	buffer := make([]uint16, 32768)
	length, _, callErr := procFinalPathName.Call(handle.Fd(), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
	if length == 0 || callErr != syscall.Errno(0) || length >= uintptr(len(buffer)) {
		return "", blocked("physical FILE target identity could not be resolved.")
	}
	physical := syscall.UTF16ToString(buffer[:length])
	if strings.HasPrefix(strings.ToUpper(physical), `\\?\UNC\`) {
		return "", blocked("UNC FILE targets are not supported by the first native adapter.")
	}
	if strings.HasPrefix(physical, `\\?\`) {
		physical = physical[4:]
	}
	resolved := filepath.Dir(physical)
	if !strings.EqualFold(target, resolved) {
		return "", blocked("aliased FILE target paths are not admitted; use the resolved physical path.")
	}
	return resolved, nil
}

// Native1CJournalRoot mirrors Get-BFNativeJournalRoot: the trusted local app
// data journal keyed by the physical target identity.
func Native1CJournalRoot(key string) (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if strings.TrimSpace(root) == "" {
		return "", blocked("local application data root is unavailable for the native runtime journal.")
	}
	return SafePath(filepath.Join(root, "BSLFlow", "runtime", key))
}
