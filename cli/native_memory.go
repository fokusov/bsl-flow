package main

import (
	"context"
	"fmt"
	"path/filepath"

	"bsl-flow/cli/internal/repository"
)

func (p *nativeProvider) InvokeMemory(ctx context.Context, input map[string]any) (map[string]any, error) {
	data, err := repository.Canonical(input)
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("memory input exceeds its bound")
	}
	shell, err := p.executable()
	if err != nil {
		return nil, err
	}
	if !p.selfHost {
		script := filepath.Join(filepath.Dir(p.script), "Invoke-BFNativeMemory.ps1")
		if err := checkPath(script); err != nil {
			return nil, err
		}
	}
	args := p.memoryArgs()
	result, err := p.runProcess(ctx, shell, args, data, providerEnvironment(p.hostPath), 1<<20)
	if err != nil {
		return nil, err
	}
	if !result.Receipt.Started || !result.Receipt.Terminal || result.Receipt.StopReason != "" || result.Receipt.ExitCode == nil || *result.Receipt.ExitCode != 0 {
		return nil, fmt.Errorf("memory helper has no successful terminal receipt")
	}
	if len(result.Stdout) > 1<<20 {
		return nil, fmt.Errorf("memory output exceeds its bound")
	}
	return repository.DecodeObject(result.Stdout)
}

// memoryArgs returns the fixed invocation argv for the memory helper: the
// packaged PowerShell bridge or the hidden native subcommand of this same
// trusted binary. The script file stays hash-bound as part of the persisted
// engine identity in both modes.
func (p *nativeProvider) memoryArgs() []string {
	if p.selfHost {
		return []string{"__memory"}
	}
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(filepath.Dir(p.script), "Invoke-BFNativeMemory.ps1")}
}
