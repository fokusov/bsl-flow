package worker

import (
	"fmt"
	"sort"
	"strings"
)

// Capability is the sealed launch capability of a worker host: the sandbox
// class the controller proved, the exact tool allowlist and the verified
// executable identity.
type Capability struct {
	Sandbox    string
	Tools      []string
	Executable string
}

// Blocker is a typed BLOCKED refusal: an unsupported capability must never be
// weakened into a launch with narrower guarantees. Its Error text carries the
// BF_BLOCKED class of the PowerShell adapters (for example "BF_BLOCKED: write
// sandbox capability was not demonstrated", Codex.ps1:46).
type Blocker struct {
	Reason string
}

func (b *Blocker) Error() string {
	if b == nil {
		return "<nil>"
	}
	return "BF_BLOCKED: " + b.Reason
}

// Check compares the requested launch capability with the observed, verified
// host capability and returns a Blocker when the host cannot honor the
// request exactly. A nil Blocker means the launch is sealed with full
// guarantees. Mirrors:
//
//   - the sandbox probes of Test-BFCodexCapability, where an undemonstrated
//     read/write sandbox profile blocks instead of degrading
//     (Codex.ps1:41-47);
//   - the executable identity check "managed Codex/sandbox identity mismatch"
//     (ProfiledCodex.ps1:204, case-sensitive -cne comparison);
//   - the exact MCP tool allowlist comparison over the sorted inventory
//     (ProfiledCodex.ps1:318,344-346): a missing or extra tool is a BLOCKED
//     refusal, never a silent subset launch.
func Check(requested Capability, observed Capability) *Blocker {
	if requested.Sandbox != "" && observed.Sandbox != requested.Sandbox {
		return &Blocker{Reason: fmt.Sprintf("sandbox capability was not demonstrated: requested %q, observed %q", requested.Sandbox, observed.Sandbox)}
	}
	if requested.Executable != "" && observed.Executable != requested.Executable {
		return &Blocker{Reason: fmt.Sprintf("managed worker/sandbox identity mismatch: requested %q, observed %q", requested.Executable, observed.Executable)}
	}
	if blockage := compareTools(requested.Tools, observed.Tools); blockage != nil {
		return blockage
	}
	return nil
}

func compareTools(requested, observed []string) *Blocker {
	if len(requested) == 0 && len(observed) == 0 {
		return nil
	}
	if blockage := rejectAmbiguousTools("requested", requested); blockage != nil {
		return blockage
	}
	if blockage := rejectAmbiguousTools("observed", observed); blockage != nil {
		return blockage
	}
	requestedSorted := append([]string(nil), requested...)
	observedSorted := append([]string(nil), observed...)
	sort.Strings(requestedSorted)
	sort.Strings(observedSorted)
	if strings.Join(requestedSorted, "\x00") != strings.Join(observedSorted, "\x00") {
		return &Blocker{Reason: fmt.Sprintf("worker tools differ from the exact registered allowlist: requested %v, observed %v", requestedSorted, observedSorted)}
	}
	return nil
}

// rejectAmbiguousTools refuses duplicated tool identities before the set
// comparison, mirroring the ambiguity refusals over worker inventories (for
// example the duplicate skill refusal of ConvertTo-BFCodexSkillInventory,
// Codex.Skills.ps1:150).
func rejectAmbiguousTools(origin string, tools []string) *Blocker {
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool) == "" {
			return &Blocker{Reason: origin + " tool allowlist contains an empty tool identity"}
		}
		if _, duplicate := seen[tool]; duplicate {
			return &Blocker{Reason: origin + " tool allowlist contains a duplicate tool: " + tool}
		}
		seen[tool] = struct{}{}
	}
	return nil
}
