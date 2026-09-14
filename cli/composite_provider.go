package main

import (
	"context"
	"fmt"

	"bsl-flow/cli/internal/repository"
)

// compositeNativeProvider routes each operation to the narrowest provider
// implementation the migration has already ported: activation measures and
// the verify stage run through the native Go stage host (the same binary
// re-executed with `__provider`), while every worker-dispatching stage stays
// on the packaged compatibility provider. A native failure never falls back
// automatically to PowerShell, and the memory helper keeps its current
// compatibility behavior.
type compositeNativeProvider struct {
	native repository.Provider
	inner  repository.Provider
}

func (p *compositeNativeProvider) Measure(ctx context.Context, input repository.MeasureInput) (repository.MeasureObservation, error) {
	if p == nil || p.native == nil {
		return repository.MeasureObservation{}, fmt.Errorf("native stage host is unavailable")
	}
	return p.native.Measure(ctx, input)
}

func (p *compositeNativeProvider) Execute(ctx context.Context, input repository.ExecuteInput) (repository.ExecuteObservation, error) {
	if p == nil || p.native == nil {
		return repository.ExecuteObservation{}, fmt.Errorf("native stage host is unavailable")
	}
	if input.Attempt["stage"] == "verify" {
		return p.native.Execute(ctx, input)
	}
	if p.inner == nil {
		return repository.ExecuteObservation{}, &nativeProviderError{
			Message: fmt.Sprintf("BF_BLOCKED: native provider does not serve stage %q without the packaged compatibility provider.", fmt.Sprintf("%v", input.Attempt["stage"])),
		}
	}
	return p.inner.Execute(ctx, input)
}

// InvokeMemory routes the advisory memory helper through the native stage
// host binary (`__memory`), which needs no PowerShell and therefore works on
// every platform; the controller still degrades memory to an advisory
// disabled envelope when the helper fails.
func (p *compositeNativeProvider) InvokeMemory(ctx context.Context, input map[string]any) (map[string]any, error) {
	if p == nil || p.native == nil {
		return nil, fmt.Errorf("memory helper is unavailable on this host")
	}
	helper, ok := p.native.(repository.MemoryProvider)
	if !ok {
		return nil, fmt.Errorf("memory helper is unavailable on this host")
	}
	return helper.InvokeMemory(ctx, input)
}
