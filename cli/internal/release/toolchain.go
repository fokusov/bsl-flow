package release

import (
	"fmt"
	"os/exec"
	"strings"
)

// CanCrossCompile reports whether the local environment can build the
// release matrix. The check runs `go version` only: with CGO disabled every
// release target is pure-Go cross-compilation that needs no platform
// toolchain and no network access. The second return value is the toolchain
// identity on success or a human-readable reason on failure.
func CanCrossCompile() (bool, string) {
	out, err := exec.Command("go", "version").CombinedOutput()
	if err != nil {
		return false, strings.TrimSpace(fmt.Sprintf("go toolchain unavailable: %v: %s", err, out))
	}
	toolchain := strings.TrimSpace(string(out))
	if toolchain == "" {
		return false, "go toolchain reported an empty version"
	}
	return true, toolchain
}
