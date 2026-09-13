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
	script := filepath.Join(filepath.Dir(p.script), "Invoke-BFNativeMemory.ps1")
	if err := checkPath(script); err != nil {
		return nil, err
	}
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script}
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
