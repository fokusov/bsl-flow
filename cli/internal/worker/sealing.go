package worker

import (
	"strings"
)

// SealInput produces the canonical sealed worker-input bytes and their SHA-256
// (lowercase hex, mirroring Get-BFHash). The sealed input covers exactly the
// prompt and the worktree path — secrets are excluded by construction, the
// same boundary as the PS prompt_sha256 binding (ProfiledCodex.ps1:227). The
// worktree is serialized with forward slashes, mirroring the path
// normalization of the adapter overrides (Codex.ps1:7,
// ProfiledCodex.ps1:8), and object members are key-sorted per the canonical
// JSON contract.
func SealInput(prompt, worktree string) ([]byte, string) {
	normalized := strings.ReplaceAll(worktree, `\`, "/")
	dst := []byte(`{"prompt":`)
	dst = appendCanonicalString(dst, prompt)
	dst = append(dst, `,"worktree":`...)
	dst = appendCanonicalString(dst, normalized)
	dst = append(dst, '}')
	return dst, sha256Hex(dst)
}
