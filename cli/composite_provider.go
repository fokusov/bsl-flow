package main

import (
	"context"
	"fmt"

	"bsl-flow/cli/internal/repository"
)

// compositeNativeProvider routes every operation to the native Go stage
// host: activation measures, the deterministic verify stage and every
// worker-dispatching stage (inspect, spec, spec_review, implement,
// code_review, diagnose) run through the same trusted binary re-executed
// with `__provider`. The packaged PowerShell provider stays in the tree for
// the legacy engine, but this composite no longer routes to it: a native
// failure is an error, never an automatic PowerShell reroute.
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
	return p.native.Execute(ctx, input)
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
