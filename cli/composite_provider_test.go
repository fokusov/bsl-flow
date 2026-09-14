package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"bsl-flow/cli/internal/repository"
)

func compositeInput(operation, stage string) repository.ProviderInput {
	attempt := map[string]any{}
	if stage != "" {
		attempt["stage"] = stage
	}
	return repository.ProviderInput{
		SchemaVersion: 1, Contract: nativeProviderContract, Operation: operation,
		TaskID:    "10000000-0000-0000-0000-00000000000a",
		StateView: map[string]any{"schema_version": int64(1), "task_id": "10000000-0000-0000-0000-00000000000a"},
		Attempt:   attempt,
	}
}

func TestCompositeProviderRouting(t *testing.T) {
	measured := repository.MeasureObservation{SchemaVersion: 1, Contract: nativeProviderContract, TaskID: "t", Operation: "measure", RequestValid: true}
	completed := repository.ExecuteObservation{SchemaVersion: 1, Contract: nativeProviderContract, Status: "completed"}
	routes := &routingRecorder{measureResult: measured, executeResult: completed, executeStages: map[string]int{}}
	composite := &compositeNativeProvider{native: &stubMemoryProvider{Provider: routes}, inner: routes}
	if _, err := composite.Measure(context.Background(), compositeInput("measure", "")); err != nil {
		t.Fatalf("measure routing: %v", err)
	}
	for _, stage := range []string{"verify", "inspect", "spec", "spec_review", "implement", "code_review", "diagnose"} {
		if _, err := composite.Execute(context.Background(), compositeInput("execute", stage)); err != nil {
			t.Fatalf("%s routing: %v", stage, err)
		}
	}
	for _, stage := range []string{"verify", "inspect", "spec", "spec_review", "implement", "code_review", "diagnose"} {
		if routes.executeStages[stage] != 1 {
			t.Fatalf("unexpected routing: %v", routes.executeStages)
		}
	}
	if _, err := composite.InvokeMemory(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("memory delegation: %v", err)
	}
}

// stubMemoryProvider satisfies the MemoryProvider seam used by the composite.
type stubMemoryProvider struct {
	repository.Provider
}

// routingRecorder exercises the routing contract without launching any
// process: every stage and the measure operation must reach the native host
// only — the composite no longer routes to the packaged compatibility
// provider.
type routingRecorder struct {
	measureResult repository.MeasureObservation
	executeResult repository.ExecuteObservation
	executeStages map[string]int
}

func (r *routingRecorder) Measure(ctx context.Context, input repository.MeasureInput) (repository.MeasureObservation, error) {
	return r.measureResult, nil
}

func (r *routingRecorder) Execute(ctx context.Context, input repository.ExecuteInput) (repository.ExecuteObservation, error) {
	r.executeStages[input.Attempt["stage"].(string)]++
	return r.executeResult, nil
}

func (p *stubMemoryProvider) InvokeMemory(ctx context.Context, input map[string]any) (map[string]any, error) {
	return map[string]any{"schema_version": int64(1), "available": true, "value": map[string]any{}, "disabled_reason": nil}, nil
}

func TestCompositeProviderMemoryDegradation(t *testing.T) {
	composite := &compositeNativeProvider{native: nil, inner: nil}
	if _, err := composite.InvokeMemory(context.Background(), map[string]any{}); err == nil {
		t.Fatal("memory without the native helper must fail closed")
	}
}

// TestCompositeProviderFailsClosedWithoutNative asserts the composite fails
// closed when the native host is unavailable: execute must never reroute to
// the packaged PowerShell provider.
func TestCompositeProviderFailsClosedWithoutNative(t *testing.T) {
	recorder := &routingRecorder{executeStages: map[string]int{}}
	composite := &compositeNativeProvider{native: nil, inner: recorder}
	if _, err := composite.Execute(context.Background(), compositeInput("execute", "inspect")); err == nil {
		t.Fatal("execute without the native host must fail closed")
	}
	if _, err := composite.Measure(context.Background(), compositeInput("measure", "")); err == nil {
		t.Fatal("measure without the native host must fail closed")
	}
	if len(recorder.executeStages) != 0 {
		t.Fatalf("the packaged provider must not be consulted: %v", recorder.executeStages)
	}
}

var _ = json.Marshal
var _ = filepath.Join
var _ = strings.TrimSpace
