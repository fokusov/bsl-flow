package platform

// BlockerCode enumerates machine-readable blocker classes. A blocker is a
// terminal refusal: it must not be relabelled or skipped into a PASS.
type BlockerCode string

const BlockerUnsupportedPlatform BlockerCode = "BLOCKED_UNSUPPORTED_PLATFORM"

// Blocker is the machine-readable reason a request cannot proceed.
type Blocker struct {
	Code    BlockerCode
	Message string
}

// Status values of the native 1C platform/runtime capability.
const (
	Native1CStatusWindowsOnly = "windows-only"
	Native1CStatusUnsupported = "unsupported"
)

type FilesystemCapability struct {
	AtomicRename bool `json:"atomic_rename"`
	Locks        bool `json:"locks"`
}

type Native1CCapability struct {
	Status string `json:"status"`
}

// MachineCapabilities is the machine-readable platform model consumed by
// routing; text/config claims cannot enable a capability that is absent here.
type MachineCapabilities struct {
	GOOS         string               `json:"goos"`
	GOArch       string               `json:"goarch"`
	Filesystem   FilesystemCapability `json:"filesystem"`
	GitInstalled bool                 `json:"git_installed"`
	Native1C     Native1CCapability   `json:"native_1c"`
	Engines      []string             `json:"engines"`
}

// Detect assembles the capability model from observed inputs: probed
// filesystem behavior, a resolved git executable and the target platform.
func Detect(goos, goarch string, fs Capability, gitPath string) MachineCapabilities {
	engines := EnginesForGOOS(goos)
	names := make([]string, len(engines))
	for i, engine := range engines {
		names[i] = string(engine)
	}
	status := Native1CStatusWindowsOnly
	if goos != "windows" {
		status = Native1CStatusUnsupported
	}
	return MachineCapabilities{
		GOOS:         goos,
		GOArch:       goarch,
		Filesystem:   FilesystemCapability{AtomicRename: fs.AtomicRenameReliable, Locks: fs.LockSupported},
		GitInstalled: gitPath != "",
		Native1C:     Native1CCapability{Status: status},
		Engines:      names,
	}
}

// Native1CBlocker returns nil on windows. On every other platform a criterion
// that needs the native 1C runtime is blocked: it cannot be relabelled or
// skipped for PASS.
func Native1CBlocker(goos string) *Blocker {
	if goos == "windows" {
		return nil
	}
	return &Blocker{Code: BlockerUnsupportedPlatform, Message: "native 1C runtime requires windows"}
}
