package stagehost

import (
	"os"
	"path/filepath"
	"strings"

	"bsl-flow/cli/internal/repository"
)

// This file ports Assert-BFProviderInput and its helpers from
// Task.Provider.ps1 with the exact legacy diagnostics.

var providerStages = map[string]bool{
	"inspect": true, "spec": true, "spec_review": true,
	"implement": true, "code_review": true, "verify": true, "diagnose": true,
}

var providerArtifactKinds = map[string]bool{
	"raw": true, "process": true, "model": true,
	"verification": true, "review": true, "budget": true, "failure": true,
}

// providerInput is the decoded, validated wire input of one provider call.
type providerInput struct {
	object           map[string]any
	operation        string
	taskID           string
	stateView        map[string]any
	attempt          map[string]any
	contextRoot      string
	artifactRoot     string
	canonicalStore   string
	cancelSignal     string
	providerContract map[string]any
	priorArtifacts   map[string]map[string]any
}

func validateProviderInput(deps Deps, object map[string]any) (*providerInput, error) {
	if _, err := assertFields(object,
		[]string{"schema_version", "contract", "operation", "task_id", "state_view", "attempt", "context_root", "artifact_root", "canonical_store_root", "cancel_signal", "provider_contract", "prior_artifacts"},
		nil, "provider_input"); err != nil {
		return nil, err
	}
	version, ok := asInteger(object["schema_version"])
	contract, isContract := object["contract"].(string)
	if !ok || version != 1 || !isContract || contract != Contract {
		return nil, invalidf("unsupported provider contract.")
	}
	operation, _ := object["operation"].(string)
	if operation != "measure" && operation != "execute" {
		return nil, invalidf("unsupported provider operation.")
	}
	taskID, _ := object["task_id"].(string)
	if err := assertUUID(taskID); err != nil {
		return nil, err
	}
	identity, err := assertFields(object["provider_contract"], []string{"name", "version", "host_sha256", "provider_sha256", "asset_manifest_sha256"}, nil, "provider_contract")
	if err != nil {
		return nil, err
	}
	identityVersion, versionOK := asInteger(identity["version"])
	if identity["name"] != Contract || !versionOK || identityVersion != 1 {
		return nil, invalidf("provider contract identity mismatch.")
	}
	for _, name := range []string{"host_sha256", "provider_sha256", "asset_manifest_sha256"} {
		if err := assertSHA256(identity[name], "provider_contract."+name); err != nil {
			return nil, err
		}
	}
	// Native-only defense in depth: the process proves the caller bound the
	// exact host, packaged provider and asset manifest it shipped with.
	if identity["host_sha256"] != deps.SelfSHA256 ||
		identity["provider_sha256"] != deps.ProviderSHA256 ||
		identity["asset_manifest_sha256"] != deps.AssetManifestSHA256 {
		return nil, blockedf("provider contract does not match the packaged native host.")
	}
	stateView, err := assertStateObject(object["state_view"])
	if err != nil {
		return nil, err
	}
	if stateView["task_id"] != taskID {
		return nil, conflictf("state view/task identity mismatch.")
	}
	request, _ := asObject(stateView["request"])
	if request["request_id"] != taskID {
		return nil, conflictf("state view/task identity mismatch.")
	}
	projectPath, err := safePath(asStringOr(stateView["project_path"]))
	if err != nil {
		return nil, err
	}
	workerPath, err := safePath(asStringOr(stateView["worker_path"]))
	if err != nil {
		return nil, err
	}
	canonicalStore, err := safePath(asStringOr(object["canonical_store_root"]))
	if err != nil {
		return nil, err
	}
	if err := assertProviderGitStore(projectPath, canonicalStore); err != nil {
		return nil, err
	}
	contextRoot, err := safePath(asStringOr(object["context_root"]))
	if err != nil {
		return nil, err
	}
	artifactRoot, err := safePath(asStringOr(object["artifact_root"]))
	if err != nil {
		return nil, err
	}
	for _, check := range []struct{ left, right, name string }{
		{contextRoot, canonicalStore, "context/canonical store"},
		{artifactRoot, canonicalStore, "artifact/canonical store"},
		{contextRoot, artifactRoot, "context/artifact"},
		{contextRoot, workerPath, "context/worker"},
		{artifactRoot, workerPath, "artifact/worker"},
	} {
		if err := disjointPath(check.left, check.right, check.name); err != nil {
			return nil, err
		}
	}
	cancelSignal, err := safePath(asStringOr(object["cancel_signal"]))
	if err != nil {
		return nil, err
	}
	if nestedPath(cancelSignal, canonicalStore) {
		return nil, invalidf("cancellation signal is inside canonical store.")
	}
	rawPrior, ok := asArray(object["prior_artifacts"])
	if !ok {
		return nil, invalidf("prior_artifacts must be an array.")
	}
	prior, err := assertPriorArtifacts(rawPrior, contextRoot)
	if err != nil {
		return nil, err
	}
	attempt, err := assertProviderAttempt(object, operation, taskID, stateView, workerPath)
	if err != nil {
		return nil, err
	}
	return &providerInput{
		object: object, operation: operation, taskID: taskID, stateView: stateView,
		attempt: attempt, contextRoot: contextRoot, artifactRoot: artifactRoot,
		canonicalStore: canonicalStore, cancelSignal: cancelSignal,
		providerContract: identity, priorArtifacts: prior,
	}, nil
}

func assertStateObject(value any) (map[string]any, error) {
	if err := assertState(value); err != nil {
		return nil, err
	}
	object, _ := asObject(value)
	return object, nil
}

// assertProviderGitStore mirrors Assert-BFProviderGitStore: the declared
// canonical store must be the verified Git common-dir store of the project.
func assertProviderGitStore(projectPath, declared string) error {
	common, err := repository.StageHostGitOutput(projectPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return blockedf("unable to verify the Git common directory for the provider store.")
	}
	common = strings.TrimSpace(common)
	commonPath := common
	if !isAbsolutePath(commonPath) {
		commonPath = filepath.Join(projectPath, common)
	}
	commonPath, err = safePath(commonPath)
	if err != nil {
		return blockedf("unable to verify the Git common directory for the provider store.")
	}
	actual, err := safePath(filepath.Join(commonPath, "bsl-flow"))
	if err != nil {
		return err
	}
	if actual != declared {
		return blockedf("declared canonical store is not the verified Git common-dir store.")
	}
	return nil
}

func assertPriorArtifacts(values []any, contextRoot string) (map[string]map[string]any, error) {
	seen := map[string]bool{}
	result := map[string]map[string]any{}
	for _, raw := range values {
		artifact, err := assertFields(raw, []string{"path", "sha256", "size_bytes", "kind"}, nil, "prior_artifact")
		if err != nil {
			return nil, err
		}
		path := asStringOr(artifact["path"])
		if err := assertRelativePath(path); err != nil {
			return nil, err
		}
		path = filepath.ToSlash(path)
		if seen[path] {
			return nil, invalidf("duplicate prior artifact: %s", path)
		}
		seen[path] = true
		if err := assertSHA256(artifact["sha256"], "prior_artifact["+path+"].sha256"); err != nil {
			return nil, err
		}
		size, ok := asInteger(artifact["size_bytes"])
		if !ok || size < 0 {
			return nil, invalidf("invalid prior artifact size: %s", path)
		}
		kind, isString := artifact["kind"].(string)
		if !isString || !providerArtifactKinds[kind] {
			return nil, invalidf("invalid prior artifact kind: %s", path)
		}
		full, err := safePath(filepath.Join(contextRoot, filepath.FromSlash(path)))
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(strings.ToLower(full), strings.ToLower(strings.TrimRight(contextRoot, `\/`)+string(filepath.Separator))) {
			return nil, invalidf("prior artifact escaped context_root.")
		}
		if !isRegularFile(full) {
			return nil, blockedf("declared prior artifact is missing: %s", path)
		}
		info, err := os.Lstat(full)
		if err != nil || info.Size() != size {
			return nil, blockedf("declared prior artifact changed: %s", path)
		}
		hash, err := hashFile(full)
		if err != nil {
			return nil, err
		}
		if hash != asStringOr(artifact["sha256"]) {
			return nil, blockedf("declared prior artifact changed: %s", path)
		}
		result[path] = map[string]any{"path": path, "sha256": artifact["sha256"], "size_bytes": size, "kind": kind}
	}
	return result, nil
}

func assertProviderAttempt(object map[string]any, operation, taskID string, stateView map[string]any, workerPath string) (map[string]any, error) {
	isMeasure := operation == "measure"
	raw := getValue(object, "attempt", nil)
	if raw == nil {
		if !isMeasure {
			return nil, invalidf("execute requires a registered attempt.")
		}
		if getValue(stateView, "active_attempt", nil) != nil {
			return nil, blockedf("measure view contains an active attempt without its binding.")
		}
		return nil, nil
	}
	attempt, err := assertFields(raw,
		[]string{"schema_version", "task_id", "attempt_id", "stage", "intent_revision", "authorization_revision", "dependencies", "source_manifest", "worker_path", "executable", "requested_models", "started_at", "operation_id"},
		[]string{"memory", "controller_process"}, "attempt")
	if err != nil {
		return nil, err
	}
	if version, ok := asInteger(attempt["schema_version"]); !ok || version != 1 {
		return nil, invalidf("unsupported attempt schema.")
	}
	if err := assertUUID(attempt["attempt_id"]); err != nil {
		return nil, err
	}
	if attempt["task_id"] != taskID {
		return nil, conflictf("attempt is not bound to the provider task.")
	}
	active := getValue(stateView, "active_attempt", nil)
	if isMeasure {
		if active != nil && attempt["attempt_id"] != active {
			return nil, conflictf("attempt is not bound to the state view.")
		}
	} else if attempt["attempt_id"] != active {
		return nil, conflictf("attempt is not bound to the state view.")
	}
	stage, _ := attempt["stage"].(string)
	if !providerStages[stage] {
		return nil, invalidf("unsupported provider stage.")
	}
	if stateView["stage"] != attempt["stage"] {
		return nil, conflictf("attempt stage differs from state view.")
	}
	attemptWorker, err := safePath(asStringOr(attempt["worker_path"]))
	if err != nil {
		return nil, err
	}
	if attemptWorker != workerPath {
		return nil, conflictf("attempt worker root differs from state view.")
	}
	executable, err := safePath(asStringOr(attempt["executable"]))
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(strings.ToLower(executable), ".exe") {
		return nil, blockedf("provider execution requires a native executable.")
	}
	return attempt, nil
}

// providerCancelled mirrors Test-BFProviderCancelled.
func providerCancelled(signalPath, taskID, attemptID string) (bool, error) {
	if strings.TrimSpace(signalPath) == "" {
		return false, nil
	}
	path, err := safePath(signalPath)
	if err != nil {
		return false, err
	}
	if !isRegularFile(path) {
		return false, nil
	}
	signal, err := readJSONObject(path)
	if err != nil {
		return false, blockedf("cancellation signal is malformed.")
	}
	if _, err := assertFields(signal, []string{"schema_version", "task_id", "attempt_id", "cancelled"}, []string{"reason"}, "cancel_signal"); err != nil {
		return false, err
	}
	version, ok := asInteger(signal["schema_version"])
	if !ok || version != 1 || signal["task_id"] != taskID || signal["attempt_id"] != attemptID {
		return false, blockedf("cancellation signal identity mismatch.")
	}
	cancelled, isBool := asBool(signal["cancelled"])
	if !isBool {
		return false, blockedf("cancellation signal has an invalid cancelled flag.")
	}
	return cancelled, nil
}
