package repository

import (
	"context"
	"testing"
)

type nativeMemoryTestProvider struct {
	*nativeTestProvider
	t          *testing.T
	operations []string
}

func (provider *nativeMemoryTestProvider) InvokeMemory(_ context.Context, input map[string]any) (map[string]any, error) {
	provider.t.Helper()
	state := asMap(input["state"])
	repository, err := OpenRepository(asStringOr(state["project_path"]))
	if err != nil {
		provider.t.Fatal(err)
	}
	task, err := repository.ReadTask(asStringOr(state["task_id"]))
	if err != nil {
		provider.t.Fatal(err)
	}
	if task.Revision != asIntOr(state["revision"]) {
		provider.t.Fatal("memory saw an uncommitted revision")
	}
	operation := asStringOr(input["operation"])
	provider.operations = append(provider.operations, operation)
	var value any = true
	switch operation {
	case "bind":
		value = map[string]any{"schema_version": int64(1), "available": true, "bundle_id": nil, "bundle_sha256": nil, "records": []any{}, "excluded": []any{}, "disabled_reason": nil}
	case "extract-attempt":
		result := asMap(input["result"])
		found := false
		for _, raw := range anyItems(asMap(task.State["controller"])["evidence"]) {
			entry := asMap(raw)
			if entry["attempt_id"] == result["attempt_id"] && entry["result_sha256"] == input["result_hash"] {
				found = true
			}
		}
		if !found {
			provider.t.Fatal("memory extraction preceded result CAS")
		}
	case "extract-acceptance":
		payload := asMap(task.State["controller"])
		if payload["status"] != "completed" || len(anyItems(payload["acceptances"])) == 0 {
			provider.t.Fatal("memory extraction preceded acceptance CAS")
		}
	}
	return map[string]any{"schema_version": int64(1), "available": true, "value": value, "disabled_reason": nil}, nil
}

func TestNativeMemoryHooksFollowCommittedControllerEvidence(t *testing.T) {
	fixture := newNativeTestFixture(t, nil)
	memory := &nativeMemoryTestProvider{nativeTestProvider: fixture.provider, t: t}
	fixture.host.Provider = memory
	nativeTestRunUntilAccept(t, fixture)
	if _, err := commandControllerAccept(fixture.project, fixture.taskID, fixture.host); err != nil {
		t.Fatal(err)
	}
	if len(memory.operations) != 7 || memory.operations[6] != "extract-acceptance" {
		t.Fatalf("missing lifecycle memory hooks: %v", memory.operations)
	}
	for i := 0; i < 6; i += 2 {
		if memory.operations[i] != "bind" || memory.operations[i+1] != "extract-attempt" {
			t.Fatalf("invalid memory ordering: %v", memory.operations)
		}
	}
	payload, repository, _ := nativeTestTaskPayload(t, fixture)
	for _, raw := range anyItems(payload["attempts"]) {
		path, err := attemptDirectory(repository, fixture.taskID, asStringOr(raw))
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := readStoredAttempt(path)
		if err != nil {
			t.Fatal(err)
		}
		if !asBoolOr(asMap(attempt["memory"])["available"]) {
			t.Fatal("attempt lost its bound memory envelope")
		}
	}
}
