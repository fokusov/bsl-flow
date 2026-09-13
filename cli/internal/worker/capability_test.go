package worker

import (
	"strings"
	"testing"
)

func TestCheckCapabilityCases(t *testing.T) {
	cases := []struct {
		name      string
		requested Capability
		observed  Capability
		wantBlock bool
		wantText  string
	}{
		{
			name:      "exact capability match",
			requested: Capability{Sandbox: "unelevated", Tools: []string{"unica.meta.validate", "unica.runtime.job.start"}, Executable: "C:/host/codex.exe"},
			observed:  Capability{Sandbox: "unelevated", Tools: []string{"unica.runtime.job.start", "unica.meta.validate"}, Executable: "C:/host/codex.exe"},
		},
		{
			name:      "no sandbox and no tools requested",
			requested: Capability{},
			observed:  Capability{Sandbox: "unelevated"},
		},
		{
			name:      "unsupported sandbox is blocked",
			requested: Capability{Sandbox: "elevated"},
			observed:  Capability{Sandbox: "unelevated"},
			wantBlock: true,
			wantText:  "sandbox capability was not demonstrated",
		},
		{
			name:      "missing demonstrated sandbox is blocked",
			requested: Capability{Sandbox: "unelevated"},
			observed:  Capability{},
			wantBlock: true,
			wantText:  "sandbox capability was not demonstrated",
		},
		{
			name:      "executable identity mismatch is blocked",
			requested: Capability{Executable: "C:/host/codex.exe"},
			observed:  Capability{Executable: "C:/other/codex.exe"},
			wantBlock: true,
			wantText:  "identity mismatch",
		},
		{
			name:      "missing tool is blocked",
			requested: Capability{Tools: []string{"unica.meta.validate", "unica.runtime.job.start"}},
			observed:  Capability{Tools: []string{"unica.meta.validate"}},
			wantBlock: true,
			wantText:  "exact registered allowlist",
		},
		{
			name:      "extra tool is blocked",
			requested: Capability{Tools: []string{"unica.meta.validate"}},
			observed:  Capability{Tools: []string{"unica.meta.validate", "unica.runtime.job.start"}},
			wantBlock: true,
			wantText:  "exact registered allowlist",
		},
		{
			name:      "tools present but none requested is blocked",
			requested: Capability{},
			observed:  Capability{Tools: []string{"unica.meta.validate"}},
			wantBlock: true,
			wantText:  "exact registered allowlist",
		},
		{
			name:      "duplicate requested tool is blocked",
			requested: Capability{Tools: []string{"unica.meta.validate", "unica.meta.validate"}},
			observed:  Capability{Tools: []string{"unica.meta.validate", "unica.meta.validate"}},
			wantBlock: true,
			wantText:  "duplicate tool",
		},
		{
			name:      "duplicate observed tool is blocked",
			requested: Capability{Tools: []string{"unica.meta.validate"}},
			observed:  Capability{Tools: []string{"unica.meta.validate", "unica.meta.validate"}},
			wantBlock: true,
			wantText:  "duplicate tool",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			blocker := Check(test.requested, test.observed)
			if !test.wantBlock {
				if blocker != nil {
					t.Fatalf("expected sealed launch, got blocker: %v", blocker)
				}
				return
			}
			if blocker == nil {
				t.Fatal("expected a BLOCKED refusal, got a weakened launch")
			}
			if !strings.HasPrefix(blocker.Error(), "BF_BLOCKED: ") {
				t.Fatalf("blocker must carry the BF_BLOCKED class: %v", blocker)
			}
			if !strings.Contains(blocker.Error(), test.wantText) {
				t.Fatalf("blocker reason mismatch: %v", blocker)
			}
		})
	}
}
