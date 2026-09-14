package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bsl-flow/cli/internal/repository"
	"bsl-flow/cli/internal/strictjson"
)

const (
	nativeProviderEntrypoint = "global/skills/1c-task/scripts/Invoke-BFNativeProvider.ps1"
	nativeProviderContract   = "bsl-flow.native-provider.windows-ps.v1"

	// The provider contract bounds the input document at 16 MiB. Keep output
	// bounded too: a provider cannot turn a task invocation into an unbounded
	// memory sink or an accidental transcript collector.
	nativeProviderInputLimit  int64 = 16 << 20
	nativeProviderOutputLimit int64 = 16 << 20

	// Core normally supplies a deadline. The fallback keeps an accidentally
	// unbounded direct invocation bounded without changing a shorter caller
	// deadline.
	nativeProviderDefaultTimeout = 5 * time.Minute

	// Provider JSON is bounded independently of the process stream limit. The
	// limits keep validation stack- and allocation-bounded for untrusted output
	// while leaving room for ordinary source manifests and dependency maps.
	nativeProviderJSONMaxDepth         = strictjson.MaxDepth
	nativeProviderJSONMaxObjectMembers = strictjson.MaxObjectMembers
	nativeProviderJSONMaxKeyBytes      = strictjson.MaxKeyBytes
)

// nativeProcessReceipt is the Go-owned terminal observation for the outer
// provider process. An exit code is present only when the operating system
// reported one; timeout/cancel/start failures never receive a fabricated code.
type nativeProcessReceipt struct {
	SchemaVersion int      `json:"schema_version"`
	Executable    string   `json:"executable"`
	Argv          []string `json:"argv"`
	Started       bool     `json:"started"`
	Terminal      bool     `json:"terminal"`
	ExitCode      *int     `json:"exit_code"`
	StopReason    string   `json:"stop_reason"`
	StdoutBytes   int64    `json:"stdout_bytes"`
	StderrBytes   int64    `json:"stderr_bytes"`
	StdoutSHA256  string   `json:"stdout_sha256"`
	StderrSHA256  string   `json:"stderr_sha256"`
	DurationMS    int64    `json:"duration_ms"`
}

type nativeProcessResult struct {
	Receipt nativeProcessReceipt
	Stdout  []byte
	Stderr  []byte
}

// nativeProviderError retains the process receipt and bounded streams for the
// controller. The controller may classify it as an unknown/recovery result;
// this adapter never turns a failed or interrupted process into an observation.
type nativeProviderError struct {
	Message  string
	Receipt  nativeProcessReceipt
	Stdout   []byte
	Stderr   []byte
	ExitCode *int
}

func (e *nativeProviderError) Error() string { return e.Message }

func (e *nativeProviderError) TransportEvidence() repository.ProviderTransportEvidence {
	return providerTransportEvidence(e.Receipt, e.Stdout, e.Stderr)
}

// boundedBuffer retains at most limit bytes while allowing the child process
// to finish. The overflow bit is checked after Wait, so the caller gets a
// deterministic size-limit failure instead of an arbitrary pipe error.
type boundedBuffer struct {
	mu         sync.Mutex
	data       bytes.Buffer
	limit      int64
	overflow   bool
	onOverflow func()
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	wasOverflow := b.overflow
	remaining := b.limit - int64(b.data.Len())
	if remaining > 0 {
		count := len(p)
		if int64(count) > remaining {
			count = int(remaining)
		}
		_, _ = b.data.Write(p[:count])
	}
	if int64(len(p)) > remaining {
		b.overflow = true
	}
	onOverflow := b.onOverflow != nil && b.overflow && !wasOverflow
	b.mu.Unlock()
	if onOverflow {
		b.onOverflow()
	}
	// Returning len(p), nil deliberately drains the child's stream. We reject
	// the completed invocation below, after both stdout and stderr are retained.
	return len(p), nil
}

func (b *boundedBuffer) Snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data.Bytes()...), b.overflow
}

type nativeProcessRunner func(context.Context, string, []string, []byte, []string, int64) (nativeProcessResult, error)

// nativeProvider is a packaged, fixed-entrypoint provider adapter. Its
// command runner is injectable only from package-private tests; production
// callers cannot select an arbitrary provider executable or fixture.
//
// selfHost selects the native Go stage host mode: instead of launching
// PowerShell with the packaged provider script, the adapter re-executes the
// trusted host binary itself with the fixed `__provider` subcommand. The
// script file is still hash-bound (it stays part of the persisted engine
// identity) but it is never executed from this mode.
type nativeProvider struct {
	selfHost    bool
	shell       string
	script      string
	hostPath    string
	hostSHA256  string
	providerSHA string
	assetsSHA   string
	timeout     time.Duration
	outputLimit int64
	runProcess  nativeProcessRunner
	shellMu     sync.Mutex
}

func newNativeProvider(root string, b bundle, hostPath string) (*nativeProvider, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("native provider bundle root is empty")
	}
	if err := checkPath(root); err != nil {
		return nil, fmt.Errorf("native provider bundle root: %w", err)
	}
	if strings.TrimSpace(hostPath) == "" {
		return nil, errors.New("native provider host path is empty")
	}
	hostPath, err := filepath.Abs(hostPath)
	if err != nil {
		return nil, fmt.Errorf("native provider host path: %w", err)
	}
	if err := checkPath(hostPath); err != nil {
		return nil, fmt.Errorf("native provider host path: %w", err)
	}
	hostSHA, err := fileSHA256(hostPath)
	if err != nil {
		return nil, fmt.Errorf("native provider host hash: %w", err)
	}
	script := filepath.Join(root, filepath.FromSlash(nativeProviderEntrypoint))
	if err := checkPath(script); err != nil {
		return nil, fmt.Errorf("native provider script: %w", err)
	}
	info, err := os.Stat(script)
	if err != nil {
		return nil, fmt.Errorf("native provider script: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("native provider script is not a regular file")
	}
	providerSHA, err := fileSHA256(script)
	if err != nil {
		return nil, fmt.Errorf("native provider script hash: %w", err)
	}
	assetsSHA := bundleManifestSHA256(b)
	return &nativeProvider{
		script:      script,
		hostPath:    hostPath,
		hostSHA256:  hostSHA,
		providerSHA: providerSHA,
		assetsSHA:   assetsSHA,
		timeout:     nativeProviderDefaultTimeout,
		outputLimit: nativeProviderOutputLimit,
		runProcess:  runNativeProcess,
	}, nil
}

// newGoNativeProvider builds the adapter for the native Go stage host: the
// provider process is this same binary running the hidden `__provider`
// subcommand, so execution never needs PowerShell.
func newGoNativeProvider(root string, b bundle, hostPath string) (*nativeProvider, error) {
	packaged, err := newNativeProvider(root, b, hostPath)
	if err != nil {
		return nil, err
	}
	packaged.selfHost = true
	return packaged, nil
}

// newNativeProviderForTest is deliberately unexported. It supports process
// integration tests without exposing an arbitrary provider option through the
// public CLI.
func newNativeProviderForTest(shell, script string, runner nativeProcessRunner) *nativeProvider {
	return &nativeProvider{
		shell:       shell,
		script:      script,
		timeout:     nativeProviderDefaultTimeout,
		outputLimit: nativeProviderOutputLimit,
		runProcess:  runner,
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, readErr := io.Copy(h, f)
	closeErr := f.Close()
	if readErr != nil {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func bundleManifestSHA256(b bundle) string {
	files := append([]bundleFile(nil), b.files...)
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].name) < strings.ToLower(files[j].name)
	})
	h := sha256.New()
	for _, file := range files {
		_, _ = io.WriteString(h, file.name)
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(file.hash[:])
		_, _ = io.WriteString(h, strconv.FormatInt(int64(len(file.data)), 10))
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (p *nativeProvider) contract() map[string]any {
	return map[string]any{
		"name":                  nativeProviderContract,
		"version":               1,
		"host_sha256":           p.hostSHA256,
		"provider_sha256":       p.providerSHA,
		"asset_manifest_sha256": p.assetsSHA,
	}
}

func (p *nativeProvider) contractIdentity() repository.ProviderContractIdentity {
	return repository.ProviderContractIdentity{
		Name:                nativeProviderContract,
		Version:             1,
		HostSHA256:          p.hostSHA256,
		ProviderSHA256:      p.providerSHA,
		AssetManifestSHA256: p.assetsSHA,
	}
}

func (p *nativeProvider) engineIdentity() repository.EngineIdentity {
	return repository.EngineIdentity{
		PolicyRoot:          filepath.Dir(filepath.Dir(filepath.Dir(p.script))),
		HostPath:            p.hostPath,
		Name:                "native",
		ContractVersion:     1,
		Provider:            nativeProviderContract,
		HostSHA256:          p.hostSHA256,
		ProviderSHA256:      p.providerSHA,
		AssetManifestSHA256: p.assetsSHA,
	}
}

func (p *nativeProvider) Measure(ctx context.Context, input repository.MeasureInput) (repository.MeasureObservation, error) {
	if p == nil {
		return repository.MeasureObservation{}, errors.New("native provider is nil")
	}
	if err := p.validateInput(input, "measure"); err != nil {
		return repository.MeasureObservation{}, err
	}
	raw, receipt, stdout, stderr, err := p.invoke(ctx, "measure", input)
	if err != nil {
		return repository.MeasureObservation{}, err
	}
	var observation repository.MeasureObservation
	if err := decodeProviderObject(raw, &observation); err != nil {
		return repository.MeasureObservation{}, &nativeProviderError{Message: fmt.Sprintf("decode native measure observation: %v", err), Receipt: receipt, Stdout: stdout, Stderr: stderr, ExitCode: receipt.ExitCode}
	}
	if err := validateMeasureObservation(observation, input); err != nil {
		return repository.MeasureObservation{}, &nativeProviderError{Message: err.Error(), Receipt: receipt, Stdout: stdout, Stderr: stderr, ExitCode: receipt.ExitCode}
	}
	evidence := providerTransportEvidence(receipt, stdout, stderr)
	observation.Transport = &evidence
	return observation, nil
}

func (p *nativeProvider) Execute(ctx context.Context, input repository.ExecuteInput) (repository.ExecuteObservation, error) {
	if p == nil {
		return repository.ExecuteObservation{}, errors.New("native provider is nil")
	}
	if err := p.validateInput(input, "execute"); err != nil {
		return repository.ExecuteObservation{}, err
	}
	raw, receipt, stdout, stderr, err := p.invoke(ctx, "execute", input)
	if err != nil {
		return repository.ExecuteObservation{}, err
	}
	var observation repository.ExecuteObservation
	if err := decodeProviderObject(raw, &observation); err != nil {
		return repository.ExecuteObservation{}, &nativeProviderError{Message: fmt.Sprintf("decode native execute observation: %v", err), Receipt: receipt, Stdout: stdout, Stderr: stderr, ExitCode: receipt.ExitCode}
	}
	if err := validateExecuteObservation(observation, input); err != nil {
		return repository.ExecuteObservation{}, &nativeProviderError{Message: err.Error(), Receipt: receipt, Stdout: stdout, Stderr: stderr, ExitCode: receipt.ExitCode}
	}
	evidence := providerTransportEvidence(receipt, stdout, stderr)
	observation.Transport = &evidence
	return observation, nil
}

func (p *nativeProvider) validateInput(input repository.ProviderInput, operation string) error {
	if input.SchemaVersion != 1 {
		return errors.New("native provider input schema_version must be 1")
	}
	if input.Contract != nativeProviderContract {
		return fmt.Errorf("native provider input contract must be %q", nativeProviderContract)
	}
	if input.Operation != operation {
		return fmt.Errorf("native provider input operation must be %q", operation)
	}
	if input.TaskID == "" {
		return errors.New("native provider input task_id is required")
	}
	identity := p.contractIdentity()
	if input.ProviderContract != identity {
		return errors.New("native provider input provider_contract does not match packaged provider")
	}
	if operation == "execute" && (input.Attempt == nil || len(input.Attempt) == 0) {
		return errors.New("native provider execute input attempt is required")
	}
	return nil
}

func decodeProviderObject(data []byte, destination any) error {
	trimmed, err := strictJSONDocument(data)
	if err != nil {
		return err
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("provider observation must be a JSON object")
	}
	if err := validateProviderJSONKeys(trimmed, reflect.TypeOf(destination), "$"); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return nil
}

// validateProviderJSONKeys closes the struct portions of the provider wire
// contract before encoding/json sees them. encoding/json matches struct
// fields case-insensitively, so DisallowUnknownFields alone would accept
// aliases such as SCHEMA_VERSION. Maps with interface values remain open by
// design because policy, dependency, and proposal payloads are delegated
// objects rather than controller-owned schemas.
func validateProviderJSONKeys(data []byte, typ reflect.Type, path string) error {
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == nil || typ.Kind() == reflect.Interface {
		return nil
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		if trimmed[0] != '{' {
			return nil // Let encoding/json report the type mismatch.
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &members); err != nil {
			return fmt.Errorf("provider JSON object at %s: %w", path, err)
		}
		fields := providerJSONFields(typ)
		for key, value := range members {
			field, ok := fields[key]
			if !ok {
				return fmt.Errorf("provider JSON field %q at %s is unsupported or not canonical", key, path)
			}
			if err := validateProviderJSONKeys(value, field.Type, path+"."+key); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if trimmed[0] != '[' {
			return nil
		}
		var values []json.RawMessage
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return fmt.Errorf("provider JSON array at %s: %w", path, err)
		}
		for index, value := range values {
			if err := validateProviderJSONKeys(value, typ.Elem(), fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case reflect.Map:
		// A map with a concrete value type can still contain typed structs. A
		// map[string]any is intentionally open and needs no recursive walk.
		if typ.Key().Kind() != reflect.String || typ.Elem().Kind() == reflect.Interface || trimmed[0] != '{' {
			return nil
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &members); err != nil {
			return fmt.Errorf("provider JSON map at %s: %w", path, err)
		}
		for key, value := range members {
			if err := validateProviderJSONKeys(value, typ.Elem(), path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func providerJSONFields(typ reflect.Type) map[string]reflect.StructField {
	fields := make(map[string]reflect.StructField, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath != "" && !field.Anonymous { // unexported
			continue
		}
		tag, hasTag := field.Tag.Lookup("json")
		name := field.Name
		if hasTag {
			name = strings.Split(tag, ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
		}
		fields[name] = field
	}
	return fields
}

func validateMeasureObservation(observation repository.MeasureObservation, input repository.ProviderInput) error {
	if observation.SchemaVersion != 1 {
		return errors.New("native measure observation schema_version must be 1")
	}
	if observation.Contract != nativeProviderContract {
		return errors.New("native measure observation contract mismatch")
	}
	if observation.TaskID != input.TaskID {
		return errors.New("native measure observation task_id mismatch")
	}
	if observation.Operation != "measure" {
		return errors.New("native measure observation operation mismatch")
	}
	return nil
}

func validateExecuteObservation(observation repository.ExecuteObservation, input repository.ProviderInput) error {
	if observation.SchemaVersion != 1 {
		return errors.New("native execute observation schema_version must be 1")
	}
	if observation.Contract != nativeProviderContract {
		return errors.New("native execute observation contract mismatch")
	}
	if observation.TaskID != input.TaskID {
		return errors.New("native execute observation task_id mismatch")
	}
	if observation.AttemptID == "" {
		return errors.New("native execute observation attempt_id is required")
	}
	if attemptID, ok := input.Attempt["attempt_id"].(string); ok && attemptID != "" && observation.AttemptID != attemptID {
		return errors.New("native execute observation attempt_id mismatch")
	}
	if observation.Stage == "" {
		return errors.New("native execute observation stage is required")
	}
	if observation.Status != "completed" && observation.Status != "failed" && observation.Status != "blocked" && observation.Status != "needs_input" {
		return fmt.Errorf("native execute observation has unsupported status %q", observation.Status)
	}
	if observation.SideEffects != "none" && observation.SideEffects != "source_changed" && observation.SideEffects != "unknown" {
		return fmt.Errorf("native execute observation has unsupported side_effects %q", observation.SideEffects)
	}
	if observation.ProviderContract != input.ProviderContract {
		return errors.New("native execute observation provider_contract mismatch")
	}
	return nil
}

func providerTransportEvidence(receipt nativeProcessReceipt, stdout, stderr []byte) repository.ProviderTransportEvidence {
	data, _ := json.Marshal(receipt)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	return repository.ProviderTransportEvidence{
		Receipt: object,
		Stdout:  append([]byte(nil), stdout...),
		Stderr:  append([]byte(nil), stderr...),
	}
}

func (p *nativeProvider) executable() (string, error) {
	if p == nil {
		return "", errors.New("native provider is nil")
	}
	if p.runProcess == nil {
		return "", errors.New("native provider process runner is missing")
	}
	if p.selfHost {
		if p.hostPath == "" {
			return "", errors.New("native provider host path is missing")
		}
		return p.hostPath, nil
	}
	if p.script == "" {
		return "", errors.New("native provider script is missing")
	}
	p.shellMu.Lock()
	defer p.shellMu.Unlock()
	if p.shell != "" {
		return p.shell, nil
	}
	shell, err := systemPowerShell()
	if err != nil {
		return "", err
	}
	p.shell = shell
	return shell, nil
}

// providerArgs returns the fixed invocation argv for the selected provider
// mode: the packaged PowerShell entrypoint or the hidden native subcommand.
func (p *nativeProvider) providerArgs() []string {
	if p.selfHost {
		return []string{"__provider"}
	}
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", p.script}
}

func (p *nativeProvider) invoke(ctx context.Context, operation string, input any) ([]byte, nativeProcessReceipt, []byte, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := providerInputJSON(operation, input)
	if err != nil {
		return nil, nativeProcessReceipt{SchemaVersion: 1, StopReason: "input-invalid"}, nil, nil, err
	}
	if int64(len(payload)) > nativeProviderInputLimit {
		return nil, nativeProcessReceipt{SchemaVersion: 1, StopReason: "input-size-limit"}, nil, nil, fmt.Errorf("native provider input exceeds %d bytes", nativeProviderInputLimit)
	}
	shell, err := p.executable()
	if err != nil {
		return nil, nativeProcessReceipt{SchemaVersion: 1, StopReason: "capability"}, nil, nil, err
	}
	args := p.providerArgs()
	env := providerEnvironment(p.hostPath)
	limit := p.outputLimit
	if limit <= 0 || limit > nativeProviderOutputLimit {
		limit = nativeProviderOutputLimit
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = nativeProviderDefaultTimeout
	}
	callCtx := ctx
	cancel := func() {}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > timeout {
		callCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	result, runErr := p.runProcess(callCtx, shell, args, payload, env, limit)
	if runErr != nil {
		if result.Receipt.SchemaVersion == 0 {
			result.Receipt.SchemaVersion = 1
		}
		return nil, result.Receipt, result.Stdout, result.Stderr, &nativeProviderError{
			Message:  runErr.Error(),
			Receipt:  result.Receipt,
			Stdout:   append([]byte(nil), result.Stdout...),
			Stderr:   append([]byte(nil), result.Stderr...),
			ExitCode: result.Receipt.ExitCode,
		}
	}
	if result.Receipt.SchemaVersion == 0 {
		result.Receipt.SchemaVersion = 1
	}
	oversize := int64(len(result.Stdout)) > limit || int64(len(result.Stderr)) > limit
	if result.Receipt.StopReason != "" || !result.Receipt.Terminal || oversize {
		message := "native provider process has no successful terminal receipt"
		if oversize {
			result.Receipt.StopReason = "size-limit"
			result.Receipt.Terminal = false
			message = "native provider output exceeds configured limit"
		}
		return nil, result.Receipt, result.Stdout, result.Stderr, &nativeProviderError{
			Message:  message,
			Receipt:  result.Receipt,
			Stdout:   append([]byte(nil), result.Stdout...),
			Stderr:   append([]byte(nil), result.Stderr...),
			ExitCode: result.Receipt.ExitCode,
		}
	}
	raw, err := strictJSONDocument(result.Stdout)
	if err != nil {
		return nil, result.Receipt, result.Stdout, result.Stderr, &nativeProviderError{
			Message:  fmt.Sprintf("invalid native provider output: %v", err),
			Receipt:  result.Receipt,
			Stdout:   append([]byte(nil), result.Stdout...),
			Stderr:   append([]byte(nil), result.Stderr...),
			ExitCode: result.Receipt.ExitCode,
		}
	}
	return raw, result.Receipt, result.Stdout, result.Stderr, nil
}

func providerInputJSON(operation string, input any) ([]byte, error) {
	if operation != "measure" && operation != "execute" {
		return nil, fmt.Errorf("unsupported native provider operation %q", operation)
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode native provider input: %w", err)
	}
	data, err = strictJSONDocument(data)
	if err != nil {
		return nil, fmt.Errorf("native provider input is not strict JSON: %w", err)
	}
	var object map[string]json.RawMessage
	if len(data) == 0 || data[0] != '{' || json.Unmarshal(data, &object) != nil || object == nil {
		return nil, errors.New("native provider input must be a JSON object")
	}
	if current, ok := object["operation"]; ok {
		var value string
		if err := json.Unmarshal(current, &value); err != nil || value != operation {
			return nil, fmt.Errorf("native provider input operation must be %q", operation)
		}
	} else {
		object["operation"], _ = json.Marshal(operation)
	}
	if current, ok := object["contract"]; ok {
		var value string
		if err := json.Unmarshal(current, &value); err != nil || value != nativeProviderContract {
			return nil, fmt.Errorf("native provider input contract must be %q", nativeProviderContract)
		}
	} else {
		object["contract"], _ = json.Marshal(nativeProviderContract)
	}
	return canonicalJSONMap(object)
}

func canonicalJSONMap(object map[string]json.RawMessage) ([]byte, error) {
	// encoding/json sorts string map keys, giving repeatable stdin bytes while
	// preserving each value exactly as supplied by the typed core request.
	return json.Marshal(object)
}

func strictJSONDocument(data []byte) ([]byte, error) {
	return strictjson.Document(data)
}

func providerEnvironment(hostPath string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(key, "BSL_FLOW_HOST_PATH") {
			continue
		}
		env = append(env, item)
	}
	if hostPath != "" {
		env = append(env, "BSL_FLOW_HOST_PATH="+hostPath)
	}
	return env
}

func runNativeProcess(ctx context.Context, shell string, args []string, input []byte, env []string, limit int64) (nativeProcessResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > nativeProviderOutputLimit {
		limit = nativeProviderOutputLimit
	}
	startedAt := time.Now()
	receipt := nativeProcessReceipt{
		SchemaVersion: 1,
		Executable:    shell,
		Argv:          append([]string(nil), args...),
	}
	command := exec.Command(shell, args...)
	command.Env = append([]string(nil), env...)
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: limit}
	var killOnce sync.Once
	var receiptMu sync.Mutex
	setStopReason := func(reason string) {
		receiptMu.Lock()
		if receipt.StopReason == "" {
			receipt.StopReason = reason
		}
		receiptMu.Unlock()
	}
	getStopReason := func() string {
		receiptMu.Lock()
		defer receiptMu.Unlock()
		return receipt.StopReason
	}
	stopProcess := func(reason string) {
		setStopReason(reason)
		killOnce.Do(func() {
			if command.Process != nil {
				_ = command.Process.Kill()
			}
		})
	}
	stdout.onOverflow = func() { stopProcess("size-limit") }
	stderr.onOverflow = func() { stopProcess("size-limit") }
	command.Stdout = stdout
	command.Stderr = stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		receipt.StopReason = "stdin"
		receipt.DurationMS = time.Since(startedAt).Milliseconds()
		return nativeProcessResult{Receipt: receipt}, err
	}
	if err := ctx.Err(); err != nil {
		receipt.StopReason = contextStopReason(ctx)
		receipt.DurationMS = time.Since(startedAt).Milliseconds()
		_ = stdin.Close()
		return nativeProcessResult{Receipt: receipt}, err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		receipt.StopReason = "start"
		receipt.DurationMS = time.Since(startedAt).Milliseconds()
		return nativeProcessResult{Receipt: receipt}, fmt.Errorf("start native provider: %w", err)
	}
	receipt.Started = true
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := stdin.Write(input)
		closeErr := stdin.Close()
		if writeErr != nil {
			writeDone <- writeErr
			return
		}
		writeDone <- closeErr
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitDone:
		if writeErr := <-writeDone; writeErr != nil {
			waitErr = fmt.Errorf("write native provider input: %w", writeErr)
		}
	case <-ctx.Done():
		stopProcess(contextStopReason(ctx))
		waitErr = <-waitDone
		_ = stdin.Close()
		<-writeDone
	}
	output, outputOverflow := stdout.Snapshot()
	errorOutput, errorOverflow := stderr.Snapshot()
	receipt.StdoutBytes = int64(len(output))
	receipt.StderrBytes = int64(len(errorOutput))
	receipt.StdoutSHA256 = bytesSHA256(output)
	receipt.StderrSHA256 = bytesSHA256(errorOutput)
	receipt.DurationMS = time.Since(startedAt).Milliseconds()
	if outputOverflow || errorOverflow {
		setStopReason("size-limit")
	}
	stopReason := getStopReason()
	if waitErr == nil && stopReason == "" {
		receipt.Terminal = true
		code := command.ProcessState.ExitCode()
		receipt.ExitCode = &code
		if code != 0 {
			setStopReason("exit")
		}
	} else if waitErr != nil && stopReason == "" {
		setStopReason("exit")
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) && exitErr.ProcessState != nil {
			code := exitErr.ProcessState.ExitCode()
			if code >= 0 {
				receipt.ExitCode = &code
			}
		}
	}
	result := nativeProcessResult{Receipt: receipt, Stdout: output, Stderr: errorOutput}
	if waitErr != nil {
		return result, fmt.Errorf("native provider process failed: %w", waitErr)
	}
	if receipt.ExitCode != nil && *receipt.ExitCode != 0 {
		return result, fmt.Errorf("native provider exited with code %d", *receipt.ExitCode)
	}
	if outputOverflow || errorOverflow {
		return result, errors.New("native provider output exceeds configured limit")
	}
	return result, nil
}

func contextStopReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return "cancel"
	}
	return "cancel"
}

func bytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
