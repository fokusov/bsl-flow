package repository

import (
	"context"
	"path/filepath"
	"time"
)

// Memory is an advisory projection owned by a separate trusted helper. It
// cannot return controller decisions and never changes success/failure gates.
type MemoryProvider interface {
	InvokeMemory(context.Context, map[string]any) (map[string]any, error)
}

func invokeNativeMemory(outer, payload map[string]any, operation, stage string, result map[string]any, resultHash string, receipt map[string]any, receiptHash string, next map[string]any, hosts ...*ControllerHost) map[string]any {
	disabled := func(reason string) map[string]any {
		return map[string]any{"schema_version": int64(1), "available": false, "value": nil, "disabled_reason": safeProjectionText(reason)}
	}
	if len(hosts) == 0 || hosts[0] == nil {
		return disabled("memory helper is unavailable on this host")
	}
	if err := assertNativePolicyFresh(payload); err != nil {
		return disabled("bound memory policy is unavailable or changed")
	}
	provider, engine, err := resolveControllerHost(hosts[0])
	if err != nil || !equalEngine(engine, engineFromMap(payload["engine"])) {
		return disabled("bound memory host is unavailable or changed")
	}
	helper, ok := provider.(MemoryProvider)
	if !ok {
		return disabled("memory helper is unavailable on this host")
	}
	input := map[string]any{
		"schema_version": int64(1), "operation": operation, "state": controllerStateView(outer, payload), "stage": stage,
		"result": result, "result_hash": resultHash, "receipt": receipt, "receipt_hash": receiptHash, "next": next,
		"memory_root":            filepath.Join(asStringOr(payload["project_path"]), ".bsl-flow", "memory"),
		"pending_failure_result": nil, "pending_failure_result_hash": "",
	}
	if (operation == "bind" && stage == "diagnose") || (operation == "projection" && asStringOr(next["stage"]) == "diagnose") {
		if err := validateNativeRepairFailure(outer, payload); err != nil {
			return disabled("diagnostic memory requires a current verified failure")
		}
		repository, err := openReadOnly(asStringOr(payload["project_path"]))
		if err != nil {
			return disabled("diagnostic failure store is unavailable")
		}
		failureID := asStringOr(asMap(payload["repair"])["pending_failure"])
		path, err := attemptDirectory(repository, asStringOr(outer["task_id"]), failureID)
		if err != nil {
			return disabled("diagnostic failure path is unavailable")
		}
		data, err := ReadFileBytes(filepath.Join(path, "terminal.json"))
		if err != nil {
			return disabled("diagnostic failure result is unavailable")
		}
		terminal, err := DecodeObject(data)
		if err != nil {
			return disabled("diagnostic failure result is invalid")
		}
		hash, err := Hash(terminal)
		matched := false
		for _, raw := range anyItems(payload["evidence"]) {
			entry := asMap(raw)
			matched = matched || (entry["attempt_id"] == failureID && entry["result_sha256"] == hash)
		}
		if err != nil || !matched || fileSHA256(data) != hash {
			return disabled("diagnostic failure result changed")
		}
		input["pending_failure_result"], input["pending_failure_result_hash"] = terminal, hash
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	envelope, err := helper.InvokeMemory(ctx, input)
	if err != nil {
		return disabled("memory helper did not complete: " + safeErrorMessage(err.Error()))
	}
	if _, err := nativeObject(envelope, []string{"schema_version", "available", "value", "disabled_reason"}, nil, "memory envelope"); err != nil {
		return disabled("invalid memory helper envelope")
	}
	if asIntOr(envelope["schema_version"]) != 1 {
		return disabled("unsupported memory helper envelope")
	}
	if _, ok := asBool(envelope["available"]); !ok {
		return disabled("invalid memory helper availability")
	}
	return envelope
}

func nativeAttemptMemory(outer, payload map[string]any, stage string, hosts ...*ControllerHost) map[string]any {
	envelope := invokeNativeMemory(outer, payload, "bind", stage, nil, "", nil, "", nil, hosts...)
	if asBoolOr(envelope["available"]) {
		value, ok := envelope["value"].(map[string]any)
		if ok && asIntOr(value["schema_version"]) == 1 {
			return value
		}
	}
	return map[string]any{"schema_version": int64(1), "available": false, "bundle_id": nil, "bundle_sha256": nil, "records": []any{}, "excluded": []any{}, "disabled_reason": envelope["disabled_reason"]}
}

func commandControllerContext(project, id string, host *ControllerHost) (any, error) {
	result, err := commandControllerStatus(project, id)
	if err != nil {
		return nil, err
	}
	repository, err := openReadOnly(project)
	if err != nil {
		return nil, err
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if asIntOr(asMap(result)["revision"]) != task.Revision {
		return nil, conflict("task changed during context projection; read it again")
	}
	payload := asMap(task.State["controller"])
	next, err := controllerNext(task.State, payload)
	if err != nil {
		return nil, err
	}
	output := asMap(result)
	envelope := invokeNativeMemory(task.State, payload, "projection", "", nil, "", nil, "", next, host)
	if asBoolOr(envelope["available"]) {
		output["memory"] = envelope["value"]
	} else {
		output["memory"] = map[string]any{"available": false, "blocker": envelope["disabled_reason"]}
	}
	return output, nil
}
