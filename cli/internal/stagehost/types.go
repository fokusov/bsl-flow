package stagehost

import (
	"context"
	"time"
)

// Deps carries the trusted host bindings of one provider process. Every value
// is supplied by the host binary itself (embedded bundle extraction, own
// executable hash); none of them comes from provider JSON input.
type Deps struct {
	// SelfPath is the absolute path of the running bsl-flow executable. It is
	// executed inside the managed sandbox for filesystem capability probes.
	SelfPath string
	// SelfSHA256 is the hash of SelfPath and must equal the
	// provider_contract.host_sha256 of every accepted input.
	SelfSHA256 string
	// SkillsRoot is the extracted trusted bundle skill root (global/skills).
	SkillsRoot string
	// ProviderSHA256 is the hash of the packaged compatibility provider
	// script. The native host never executes it; the value binds the same
	// engine identity the controller persisted.
	ProviderSHA256 string
	// AssetManifestSHA256 is the embedded bundle manifest hash.
	AssetManifestSHA256 string
	// HostPolicyPath is the BSL_FLOW_HOST_PATH supplied by the trusted parent
	// process. Empty means the policy inventory carries no host entry, exactly
	// like the packaged provider.
	HostPolicyPath string
	// RunProcess launches managed child processes with immutable receipts.
	RunProcess ProcessRunner
	// Now supplies the current time; tests inject a deterministic clock.
	Now func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// runProcess dispatches through the injected seam or the production managed
// process runner. Every child process of the stage host goes through it, so
// all receipts share one immutable shape.
func (d Deps) runProcess(ctx context.Context, opts ProcessOptions) (ProcessResult, error) {
	if d.RunProcess != nil {
		return d.RunProcess(ctx, opts)
	}
	return runManagedProcess(ctx, opts)
}

// ProcessRunner mirrors Invoke-BFProcess: it launches one native executable
// with data arguments, streams bounded output into the receipt directory and
// returns the terminal exit receipt. It never fabricates an exit code for a
// process that did not terminate.
type ProcessRunner func(ctx context.Context, opts ProcessOptions) (ProcessResult, error)

// ProcessOptions is the Invoke-BFProcess parameter surface.
type ProcessOptions struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
	InputText        string
	OutputDirectory  string
	TimeoutSeconds   int
	// Cancelled is polled while the process runs; a true result terminates the
	// owned process tree with stop_reason "cancelled".
	Cancelled func() (bool, error)
	// Environment entries are added (or replace) the launch environment.
	Environment map[string]string
	// CleanEnvironment keeps only the trusted Windows launch/policy inputs,
	// never arbitrary host credentials.
	CleanEnvironment bool
	MaxOutputBytes   int64
	// Deadline is the task wall-time bound (BFRunDeadlineUtc). A zero value
	// means no additional bound beyond TimeoutSeconds.
	Deadline time.Time
}

// ProcessResult is the content of the immutable exit.json receipt. Stdout and
// Stderr are absolute paths of the retained stream files.
type ProcessResult struct {
	ExitCode       int
	StopReason     string // "", "cancelled", "timeout", "output_limit", "stdin_failure"
	ElapsedSeconds float64
	ProcessID      int
	Executable     string
	Stdout         string
	Stderr         string
}

// ExitObject renders the receipt exactly like the legacy exit.json document.
func (r ProcessResult) ExitObject() map[string]any {
	result := map[string]any{
		"exit_code":       int64(r.ExitCode),
		"stop_reason":     nil,
		"elapsed_seconds": r.ElapsedSeconds,
		"process_id":      int64(r.ProcessID),
		"executable":      r.Executable,
		"stdout":          r.Stdout,
		"stderr":          r.Stderr,
	}
	if r.StopReason != "" {
		result["stop_reason"] = r.StopReason
	}
	return result
}
